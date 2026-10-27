package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// fleetHubSpec reads the fleet settings layer into a HubSettings proto,
// nil when the owner never wrote one (defaults.md fills the gaps).
func fleetHubSpec(ctx context.Context, tx *sql.Tx) *hubv1.HubSettings {
	raw, err := statedb.ReadFleetHubSpecTx(ctx, tx)
	if err != nil || raw == "" {
		return nil
	}
	var hub hubv1.HubSettings
	if err := protojson.Unmarshal([]byte(raw), &hub); err != nil {
		return nil
	}
	return &hub
}

// dispatchConsent resolves the goal's consent (ADR 0008, 03c planning
// rule 5): the operator's explicit list wins outright; otherwise repo
// consent (else HubSettings.default_consent) intersected with account
// consent. An empty result is CONSENT_EMPTY, so the goal could do nothing
// but edit files. It returns the account id's stored spec for the
// caller, so the account checks read the same row.
func (f *Fleet) dispatchConsent(ctx context.Context, tx *sql.Tx, in *hubv1.GoalSpecInput,
	spec *hubv1.RepoSpec,
) (account *hubv1.AccountSpec, consent []typev1.ConsentAct, err error) {
	accountID := in.GetPlacement().GetAccount().GetValue()
	if accountID == "" {
		return nil, nil, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"placement.account is required: the scheduler that ranks accounts is row 23", nil,
			[]*typev1.FieldViolation{{Field: "input.placement.account", Description: "an account is required"}})
	}
	stored, err := statedb.ReadAccountTx(ctx, tx, accountID)
	if err != nil {
		if errors.Is(err, statedb.ErrAccountUnknown) {
			return nil, nil, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "unknown account", nil,
				[]*typev1.FieldViolation{{Field: "input.placement.account", Description: "unknown account"}})
		}
		return nil, nil, fmt.Errorf("dispatch: read account: %w", err)
	}
	var aspec hubv1.AccountSpec
	if err := protojson.Unmarshal(stored.Spec, &aspec); err != nil {
		return nil, nil, fmt.Errorf("dispatch: read account spec: %w", err)
	}
	if len(in.GetConsent()) > 0 {
		return &aspec, dedupeActs(in.GetConsent()), nil
	}
	repoActs := spec.GetConsent().GetActs()
	if spec.GetConsent() == nil {
		if hub := fleetHubSpec(ctx, tx); hub != nil {
			repoActs = hub.GetDefaultConsent()
		}
	}
	have := map[typev1.ConsentAct]bool{}
	for _, a := range repoActs {
		have[a] = true
	}
	var out []typev1.ConsentAct
	for _, a := range aspec.GetConsent() {
		if have[a] {
			out = append(out, a)
		}
	}
	return &aspec, dedupeActs(out), nil
}

// dedupeActs keeps the first of each consent act, in wire order.
func dedupeActs(acts []typev1.ConsentAct) []typev1.ConsentAct {
	seen := map[typev1.ConsentAct]bool{}
	out := acts[:0]
	for _, a := range acts {
		if a == typev1.ConsentAct_CONSENT_ACT_UNSPECIFIED || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// hasConsent reports whether MERGE (or any act) is in the goal's consent.
func hasConsent(consent []typev1.ConsentAct, act typev1.ConsentAct) bool {
	for _, a := range consent {
		if a == act {
			return true
		}
	}
	return false
}

// dispatchMachine resolves the placement machine: pinned wins, empty
// first-fits over ONLINE undrained machines with free slots in machine-id
// order (row 22 best effort; row 23 ranks). It fills the machine checks:
// OFFLINE, DRAINING, a fresh UNFIT marker for the harness scope, and
// SLOTS_FULL against the machine layer caps (else the fleet defaults).
func (f *Fleet) dispatchMachine(ctx context.Context, tx *sql.Tx, in *hubv1.GoalSpecInput,
	input *goals.Input, fx *dispatchFacts, nowMs int64,
) error {
	harness := in.GetPlacement().GetHarness().GetName()
	if harness == "" {
		return apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"placement.harness is required: the scheduler that picks it is row 23", nil,
			[]*typev1.FieldViolation{{Field: "input.placement.harness", Description: "a harness is required"}})
	}
	fx.harness = harness
	fx.accountID = in.GetPlacement().GetAccount().GetValue()
	machineID := in.GetPlacement().GetMachine().GetValue()
	if machineID == "" {
		machines, err := statedb.ListMachineDispatchTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("dispatch: list machines: %w", err)
		}
		if m, ok := firstFitMachine(ctx, tx, machines); ok {
			machineID = m.ID
		} else {
			input.NoFitMachine = true
			input.MachineDetail = "no ONLINE undrained machine with free slots"
		}
	}
	if machineID == "" {
		return nil
	}
	m, err := statedb.ReadMachineDispatchTx(ctx, tx, machineID)
	if err != nil {
		if errors.Is(err, statedb.ErrMachineUnknown) {
			return apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "unknown machine", nil,
				[]*typev1.FieldViolation{{Field: "input.placement.machine", Description: "unknown machine"}})
		}
		return fmt.Errorf("dispatch: read machine: %w", err)
	}
	fx.machine, fx.machineID = m, machineID
	switch m.State {
	case statedb.MachineStateOnline:
	case statedb.MachineStateStale:
		input.MachineOffline = true
	default:
		input.MachineOffline = true
	}
	if m.Draining {
		input.Draining = true
	}
	repo := in.GetRepo().GetHost() + "/" + in.GetRepo().GetPath()
	for _, mark := range m.Unfit {
		if statedb.UnfitCovers(mark, harness, repo) && statedb.UnfitFresh(mark, nowMs) {
			input.MachineUnfit = true
			input.AgentDetail = mark.Reason
			break
		}
	}
	maxActive, maxTotal := int64(defaultMaxActive), int64(defaultMaxTotal)
	if raw, found, err := statedb.ReadSettingsLayerTx(ctx, tx, statedb.MachineScopeKey(machineID)); err != nil {
		return fmt.Errorf("dispatch: read machine settings: %w", err)
	} else if found {
		var lspec hubv1.SettingsSpec
		if err := protojson.Unmarshal([]byte(raw), &lspec); err == nil {
			if vs := lspec.GetValues(); vs != nil {
				// Zero means unset and falls back to the defaults above:
				// zero slots would run nothing, and draining the machine
				// is the supported off switch.
				if vs.GetMaxActive() != 0 {
					maxActive = int64(vs.GetMaxActive())
				}
				if vs.GetMaxTotal() != 0 {
					maxTotal = int64(vs.GetMaxTotal())
				}
			}
		}
	}
	if m.Active >= maxActive || m.Total >= maxTotal {
		input.SlotsFull = true
	}
	return nil
}

// firstFitMachine picks the first ONLINE undrained machine with a free
// total slot (the row-22 placement; row 23 ranks by waste risk instead).
func firstFitMachine(ctx context.Context, tx *sql.Tx, machines []statedb.MachineDispatch) (statedb.MachineDispatch, bool) {
	for _, m := range machines {
		if m.State != statedb.MachineStateOnline || m.Draining {
			continue
		}
		if m.Total >= int64(defaultMaxTotal) {
			continue
		}
		return m, true
	}
	return statedb.MachineDispatch{}, false
}

// dispatchIntegration fills the integration checks for the item's
// integration: DEGRADED health warns, and sync lag past max_sync_lag
// warns (03c planning rule 1: claims may be stale). A missing
// integration row warns too: freshness cannot be proven.
func (f *Fleet) dispatchIntegration(ctx context.Context, tx *sql.Tx, in *hubv1.GoalSpecInput,
	input *goals.Input, now time.Time,
) error {
	id := in.GetWorkItem().GetIntegration().GetValue()
	raw, err := statedb.ReadIntegrationStatusTx(ctx, tx, id)
	if err != nil {
		// Freshness cannot be proven without the row (missing or a
		// read failure alike), so the pass warns instead of failing.
		input.IntegrationLag = true
	} else {
		var st hubv1.IntegrationStatus
		if err := protojson.Unmarshal([]byte(raw), &st); err != nil {
			return fmt.Errorf("dispatch: read integration status: %w", err)
		}
		if st.GetHealth() == hubv1.IntegrationHealth_INTEGRATION_HEALTH_DEGRADED {
			input.IntegrationDegraded = true
		}
		maxLag := f.maxSyncLag()
		if lag := st.GetSyncLag().AsDuration(); lag > maxLag {
			input.IntegrationLag = true
		}
	}
	return nil
}

// dispatchAccount fills the account checks: INTERACTIVE_ONLY is refused
// outright, a dollar-cap account spends money and needs a CONFIRM, and a
// missing, failed or stale usage reading warns (row 23 owns pacing
// states; the probe verdicts own overage billing).
func (f *Fleet) dispatchAccount(ctx context.Context, tx *sql.Tx, in *hubv1.GoalSpecInput,
	input *goals.Input, fx *dispatchFacts, now time.Time,
) error {
	accountID := in.GetPlacement().GetAccount().GetValue()
	stored, err := statedb.ReadAccountTx(ctx, tx, accountID)
	if err != nil {
		return fmt.Errorf("dispatch: read account: %w", err)
	}
	var aspec hubv1.AccountSpec
	if err := protojson.Unmarshal(stored.Spec, &aspec); err != nil {
		return fmt.Errorf("dispatch: read account spec: %w", err)
	}
	switch aspec.GetMode() {
	case hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY:
		input.AccountBlocked = goals.AccountBlockedInteractive
		input.AccountCause = "interactive_only"
	case hubv1.AccountMode_ACCOUNT_MODE_FLEET:
		if aspec.GetBudgetKind() == typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP {
			input.AccountBlocked = goals.AccountBlockedSpend
			input.AccountCause = "dollar_cap"
		}
	default:
		// Unspecified mode blocks nothing; usage checks below still apply.
	}
	readMs, parse, found, err := statedb.LatestUsageTx(ctx, tx, accountID)
	if err != nil {
		return fmt.Errorf("dispatch: read usage: %w", err)
	}
	switch {
	case !found:
		input.AccountUsageUnknown = true
		input.AccountCause = "no_reading"
	case parse == "FAILED":
		input.AccountUsageUnknown = true
		input.AccountCause = "parse_failed"
	case now.Sub(time.UnixMilli(readMs)) > staleAfter:
		input.AccountUsageUnknown = true
		input.AccountCause = "stale"
	}
	return nil
}

// workItemDisplay renders the short display the branch pattern fills
// `{work}` with: "eshu#7398", "eshu!12", "PROJ-123" (02k window names).
func workItemDisplay(in *hubv1.GoalSpecInput) string {
	key := in.GetWorkItem()
	switch key.GetKind() {
	case typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST:
		return key.GetProject() + "!" + key.GetKey()
	case typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE:
		return key.GetProject() + "#" + key.GetKey()
	default:
		return key.GetKey()
	}
}

// renderBranch fills `{work}` (the display slug) and `{goal}` in the
// branch pattern; the rendered name is lowercase [a-z0-9/._-], anything
// else a dash (repo.proto branch_pattern).
func renderBranch(pattern, display, goalID string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '/', r == '.', r == '_', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, display)
	out := strings.ReplaceAll(pattern, "{work}", slug)
	out = strings.ReplaceAll(out, "{goal}", goalID)
	return out
}
