package api

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// importFormatVersion is the only FleetConfig.format_version the hub
// applies (config.proto).
const importFormatVersion = 1

// importPlanLineCap is the 02b cap on ImportConfig plan lines.
const importPlanLineCap = 2000

// errImportPlanTooLarge is a document with more plan lines than 02b allows.
var errImportPlanTooLarge = errors.New("api: the import plan is over 2000 lines")

// importSpecFields is the fixed order of compared repo spec paths, the same
// vocabulary as updateRepoAllow.
var importSpecFields = []string{
	"spec.enabled",
	"spec.auto_dispatch",
	"spec.consent",
	"spec.max_parallel_goals",
	"spec.worktree_mode",
	"spec.branch_pattern",
	"spec.base_branch",
	"spec.hooks_mode",
	"spec.tracker_projects",
	"spec.trusted_gate",
}

// importHas reports whether an optional spec field is set, through the
// proto API rather than the generated struct (protogetter).
func importHas(spec *hubv1.RepoSpec, name string) bool {
	fd := spec.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	return fd != nil && spec.ProtoReflect().Has(fd)
}

// importSpecEquals compares one spec path of two repo specs, presence and
// all: when the whole specs differ, at least one path here differs too, so
// an UPDATE always names its changed fields.
var importSpecEquals = map[string]func(stored, want *hubv1.RepoSpec) bool{
	"spec.enabled": func(stored, want *hubv1.RepoSpec) bool { return stored.GetEnabled() == want.GetEnabled() },
	"spec.auto_dispatch": func(stored, want *hubv1.RepoSpec) bool {
		return importHas(stored, "auto_dispatch") == importHas(want, "auto_dispatch") &&
			stored.GetAutoDispatch() == want.GetAutoDispatch()
	},
	"spec.consent": func(stored, want *hubv1.RepoSpec) bool {
		return importHas(stored, "consent") == importHas(want, "consent") &&
			proto.Equal(stored.GetConsent(), want.GetConsent())
	},
	"spec.max_parallel_goals": func(stored, want *hubv1.RepoSpec) bool {
		return importHas(stored, "max_parallel_goals") == importHas(want, "max_parallel_goals") &&
			stored.GetMaxParallelGoals() == want.GetMaxParallelGoals()
	},
	"spec.worktree_mode":  func(stored, want *hubv1.RepoSpec) bool { return stored.GetWorktreeMode() == want.GetWorktreeMode() },
	"spec.branch_pattern": func(stored, want *hubv1.RepoSpec) bool { return stored.GetBranchPattern() == want.GetBranchPattern() },
	"spec.base_branch":    func(stored, want *hubv1.RepoSpec) bool { return stored.GetBaseBranch() == want.GetBaseBranch() },
	"spec.hooks_mode":     func(stored, want *hubv1.RepoSpec) bool { return stored.GetHooksMode() == want.GetHooksMode() },
	"spec.tracker_projects": func(stored, want *hubv1.RepoSpec) bool {
		return equalTrackerProjects(stored.GetTrackerProjects(), want.GetTrackerProjects())
	},
	"spec.trusted_gate": func(stored, want *hubv1.RepoSpec) bool {
		return importHas(stored, "trusted_gate") == importHas(want, "trusted_gate") &&
			proto.Equal(stored.GetTrustedGate(), want.GetTrustedGate())
	},
}

// planConfigHash is the plan_hash of an import plan (config.proto): SHA-256
// in hex over the canonical plan. Lines sort by entity then name, each
// length-prefixed and deterministically marshalled, so the hash is stable
// whatever order the plan was built in. The problem detail is hashed too:
// the same lines with different problems are a different plan.
func planConfigHash(changes []*hubv1.ConfigChange) string {
	sorted := make([]*hubv1.ConfigChange, len(changes))
	copy(sorted, changes)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].GetEntity() != sorted[j].GetEntity() {
			return sorted[i].GetEntity() < sorted[j].GetEntity()
		}
		return sorted[i].GetName() < sorted[j].GetName()
	})
	h := sha256.New()
	var ln [8]byte
	for _, c := range sorted {
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
		if err != nil {
			b = []byte(fmt.Sprintf("unmarshallable plan line: %v", err))
		}
		binary.LittleEndian.PutUint64(ln[:], uint64(len(b)))
		h.Write(ln[:])
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// invalidImportLine is a plan line for a document entry the hub cannot
// apply: kind SKIPPED with the problem, so an apply answers applied false
// with something to fix (config.proto). A CONFIG_INVALID problem always
// blocks the apply; any other reason is a waivable skip the owner clears
// with allow_skipped. field is the path in the document
// (config.repos[2].spec.repo.path); descriptions never echo the request.
func invalidImportLine(entity hubv1.ConfigEntity, name, field string, reason typev1.ReasonCode, desc string) *hubv1.ConfigChange {
	return &hubv1.ConfigChange{
		Entity: entity, Name: name, Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED,
		Problem: &typev1.ErrorDetail{
			Reason: reason,
			Violations: []*typev1.FieldViolation{{
				Field: field, Reason: reason, Description: desc,
			}},
		},
	}
}

// blockedImportLine is a plan line for a document entry that fails
// validation: never waivable, even with allow_skipped.
func blockedImportLine(entity hubv1.ConfigEntity, name, field, desc string) *hubv1.ConfigChange {
	return invalidImportLine(entity, name, field, typev1.ReasonCode_REASON_CODE_CONFIG_INVALID, desc)
}

// firstViolationText is the first field violation of a field-check error,
// for a plan line problem.
func firstViolationText(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if ed, ok := v.(*typev1.ErrorDetail); ok {
					for _, fv := range ed.GetViolations() {
						return fv.GetField() + ": " + fv.GetDescription()
					}
				}
			}
		}
	}
	return "the entry is invalid"
}

// fallbackRepoName names a repo entry whose identity did not parse.
func fallbackRepoName(r *hubv1.RepoSpec, i int) string {
	if host, path := r.GetRepo().GetHost(), r.GetRepo().GetPath(); host != "" || path != "" {
		return host + "/" + path
	}
	return fmt.Sprintf("repos[%d]", i)
}

// diffRepoSpec lists the spec paths where want differs from stored, in
// importSpecFields order (plus spec.repo when the code host moved).
func diffRepoSpec(stored, want *hubv1.RepoSpec) []string {
	var out []string
	if stored.GetRepo().GetCodeHost().GetValue() != want.GetRepo().GetCodeHost().GetValue() {
		out = append(out, "spec.repo")
	}
	for _, path := range importSpecFields {
		if equal, ok := importSpecEquals[path]; ok && !equal(stored, want) {
			out = append(out, path)
		}
	}
	return out
}

func equalTrackerProjects(a, b []*hubv1.TrackerProject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !proto.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// classifyRepo plans one document repo against the watched set: the line
// kind, the normalized spec to store, and the spec's changed fields for an
// update. existing maps "host/path" to the stored spec. codeHosts maps a
// host to its integration id; a missing host has no code host. seen holds
// the document names already planned, to refuse duplicates.
func classifyRepo(r *hubv1.RepoSpec, i int, existing map[string]*hubv1.RepoSpec,
	codeHosts map[string]string, tables *refTables, seen map[string]bool,
) (kind hubv1.ConfigChangeKind, name string, normalized *hubv1.RepoSpec, changed []string, problem *hubv1.ConfigChange) {
	field := fmt.Sprintf("config.repos[%d]", i)
	host, path, err := checkRepoIdentity(&hubv1.Repo{Spec: r})
	if err != nil {
		return 0, fallbackRepoName(r, i), nil, nil,
			blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_REPO, fallbackRepoName(r, i), field, firstViolationText(err))
	}
	name = host + "/" + path
	if seen[name] {
		return 0, name, nil, nil,
			blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_REPO, name, field, "the repo is duplicated in the document")
	}
	seen[name] = true
	if err := checkRepoSpec(r); err != nil {
		return 0, name, nil, nil,
			blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_REPO, name, field, firstViolationText(err))
	}
	if r.GetCodeHostRef() == nil {
		if _, ok := codeHosts[host]; !ok {
			// A missing integration is a skip, not a validation failure
			// (config.proto): allow_skipped applies the lines that can apply,
			// and the owner connects the integration first.
			return 0, name, nil, nil,
				invalidImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_REPO, name, field,
					typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
					"no code-host integration reaches this repo; connect it first")
		}
	}
	normalized = proto.Clone(r).(*hubv1.RepoSpec) //nolint:forcetypeassert // Clone keeps the message type
	if tables == nil {
		tables = newRefTables(nil)
	}
	if p := tables.normalizeRepoRefs(field, name, host, normalized, codeHosts); p != nil {
		return 0, name, nil, nil, p
	}
	stored, ok := existing[name]
	if !ok {
		return hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE, name, normalized, nil, nil
	}
	if proto.Equal(stored, normalized) {
		return hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED, name, normalized, nil, nil
	}
	return hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE, name, normalized, diffRepoSpec(stored, normalized), nil
}

// unsupportedImportLine is the plan line for a document section the hub
// does not apply yet (later slices of row 13a): SKIPPED with
// CONFIG_INVALID, so it blocks an apply even with allow_skipped.
func unsupportedImportLine(entity hubv1.ConfigEntity, name, field, section string) *hubv1.ConfigChange {
	return blockedImportLine(entity, name, field, section+" imports are not built yet")
}

// isEmptyHub is an unset or default hub section: nothing to import.
func isEmptyHub(h *hubv1.HubSettings) bool {
	return h == nil || proto.Equal(h, &hubv1.HubSettings{})
}

// isEmptyFleetValues is an unset or default fleet values layer.
func isEmptyFleetValues(v *typev1.SettingValues) bool {
	return v == nil || proto.Equal(v, &typev1.SettingValues{})
}

// importEntryCount is the number of plan lines a document can produce.
func importEntryCount(doc *hubv1.FleetConfig) int {
	n := len(doc.GetRepos()) + len(doc.GetGroups()) + len(doc.GetMachines()) +
		len(doc.GetIntegrations()) + len(doc.GetRoutines()) + len(doc.GetAccounts())
	if !isEmptyHub(doc.GetHub()) {
		n++
	}
	if !isEmptyFleetValues(doc.GetFleetValues()) {
		n++
	}
	return n
}

// configRest is the stored configuration an import plan diffs the
// non-repo sections against: the fleet layer's hub settings and values
// with the integrations and accounts by their document keys, the
// enrolled machines by display name and the kept machine entries by
// name. A nil rest is an empty hub: every non-empty section plans as
// skipped, as before, except machines, which plan as pending creates.
type configRest struct {
	hub          *hubv1.HubSettings
	fleetValues  *typev1.SettingValues
	integrations map[string]*hubv1.IntegrationConfig
	// integrationIDs resolves a document integration ref to the local
	// id, by the same provider/base-URL key as integrations (#397).
	integrationIDs map[string]string
	accounts       map[string]*hubv1.AccountConfig
	machines       map[string][]machineBaseline
	pending        map[string]pendingBaseline
}

// restHub is the stored hub settings, or an empty one when the owner
// never wrote the fleet layer.
func restHub(rest *configRest) *hubv1.HubSettings {
	if rest == nil || rest.hub == nil {
		return &hubv1.HubSettings{}
	}
	return rest.hub
}

// restFleetValues is the stored fleet values, or empty ones when the
// owner never wrote the fleet layer.
func restFleetValues(rest *configRest) *typev1.SettingValues {
	if rest == nil || rest.fleetValues == nil {
		return &typev1.SettingValues{}
	}
	return rest.fleetValues
}

// diffStoredLine plans one stored section against the document: unchanged
// when the document carries what the hub holds, or the skipped line
// otherwise. Empty document sections plan nothing, so a document that
// names only repos still applies on a configured hub.
func diffStoredLine(entity hubv1.ConfigEntity, name string, equal bool) *hubv1.ConfigChange {
	if equal {
		return &hubv1.ConfigChange{Entity: entity, Name: name, Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED}
	}
	return nil
}

// planConfigImport diffs a FleetConfig document against the watched repos
// (existing maps "host/path" to the stored spec; codeHosts maps a host to
// its integration id) and the stored configuration (rest). Repos plan
// create, update and unchanged; machines matching an enrolled display
// name plan the labels and settings update, other machine entries plan
// the kept pending create, update or unchanged; the hub settings, fleet
// values, integrations and accounts plan unchanged when the document
// carries what the hub holds, so an export imports to an empty plan. Any
// other non-empty section plans one SKIPPED line per entry with
// CONFIG_INVALID. A document over the 02b line cap is errImportPlanTooLarge.
func planConfigImport(doc *hubv1.FleetConfig, existing map[string]*hubv1.RepoSpec,
	codeHosts map[string]string, rest *configRest,
) ([]*hubv1.ConfigChange, error) {
	if importEntryCount(doc) > importPlanLineCap {
		return nil, errImportPlanTooLarge
	}
	var changes []*hubv1.ConfigChange
	tables := newRefTables(rest)
	seen := map[string]bool{}
	for i, r := range doc.GetRepos() {
		kind, name, _, changed, problem := classifyRepo(r, i, existing, codeHosts, tables, seen)
		if problem != nil {
			changes = append(changes, problem)
			continue
		}
		changes = append(changes, &hubv1.ConfigChange{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_REPO, Name: name,
			Kind: kind, ChangedFields: changed,
		})
	}
	if !isEmptyHub(doc.GetHub()) {
		hubClone := proto.Clone(doc.GetHub()).(*hubv1.HubSettings) //nolint:forcetypeassert // Clone keeps the message type
		if p := tables.normalizeGateRefs("config.hub", hubClone.GetTrustedGate()); p != nil {
			changes = append(changes, p)
		} else if line := diffStoredLine(hubv1.ConfigEntity_CONFIG_ENTITY_HUB_SETTINGS, "hub",
			proto.Equal(hubClone, restHub(rest))); line != nil {
			changes = append(changes, line)
		} else {
			changes = append(changes, unsupportedImportLine(
				hubv1.ConfigEntity_CONFIG_ENTITY_HUB_SETTINGS, "hub", "config.hub", "hub settings"))
		}
	}
	if !isEmptyFleetValues(doc.GetFleetValues()) {
		if line := diffStoredLine(hubv1.ConfigEntity_CONFIG_ENTITY_FLEET_VALUES, "fleet",
			proto.Equal(doc.GetFleetValues(), restFleetValues(rest))); line != nil {
			changes = append(changes, line)
		} else {
			changes = append(changes, unsupportedImportLine(
				hubv1.ConfigEntity_CONFIG_ENTITY_FLEET_VALUES, "fleet", "config.fleet_values", "fleet values"))
		}
	}
	for i, g := range doc.GetGroups() {
		name := g.GetLabel()
		if name == "" {
			name = fmt.Sprintf("groups[%d]", i)
		}
		changes = append(changes, unsupportedImportLine(
			hubv1.ConfigEntity_CONFIG_ENTITY_GROUP, name, fmt.Sprintf("config.groups[%d]", i), "group"))
	}
	machineSeen := map[string]bool{}
	for i, mc := range doc.GetMachines() {
		var enrolled map[string][]machineBaseline
		var kept map[string]pendingBaseline
		if rest != nil {
			enrolled, kept = rest.machines, rest.pending
		}
		changes = append(changes, classifyMachine(mc, i, enrolled, kept, machineSeen).line)
	}
	for i, ig := range doc.GetIntegrations() {
		name := ig.GetProviderName()
		if name == "" {
			name = ig.GetBaseUrl()
		}
		if name == "" {
			name = fmt.Sprintf("integrations[%d]", i)
		}
		var stored *hubv1.IntegrationConfig
		if rest != nil {
			stored = rest.integrations[ig.GetProviderName()+"\x00"+ig.GetBaseUrl()]
		}
		if line := diffStoredLine(hubv1.ConfigEntity_CONFIG_ENTITY_INTEGRATION, name,
			stored != nil && proto.Equal(ig, stored)); line != nil {
			changes = append(changes, line)
		} else {
			changes = append(changes, unsupportedImportLine(
				hubv1.ConfigEntity_CONFIG_ENTITY_INTEGRATION, name, fmt.Sprintf("config.integrations[%d]", i), "integration"))
		}
	}
	for i, rt := range doc.GetRoutines() {
		name := rt.GetDisplayName()
		if name == "" {
			name = fmt.Sprintf("routines[%d]", i)
		}
		changes = append(changes, unsupportedImportLine(
			hubv1.ConfigEntity_CONFIG_ENTITY_ROUTINE, name, fmt.Sprintf("config.routines[%d]", i), "routine"))
	}
	for i, ac := range doc.GetAccounts() {
		name := ac.GetSpec().GetDisplayName()
		if name == "" {
			name = fmt.Sprintf("accounts[%d]", i)
		}
		field := fmt.Sprintf("config.accounts[%d]", i)
		acClone := proto.Clone(ac).(*hubv1.AccountConfig) //nolint:forcetypeassert // Clone keeps the message type
		if p := tables.normalizeAccountRefs(name, field, acClone.GetSpec()); p != nil {
			changes = append(changes, p)
			continue
		}
		var stored *hubv1.AccountConfig
		if rest != nil {
			stored = rest.accounts[ac.GetHarness().GetName()+"\x00"+ac.GetSpec().GetDisplayName()]
		}
		if line := diffStoredLine(hubv1.ConfigEntity_CONFIG_ENTITY_ACCOUNT, name,
			stored != nil && proto.Equal(acClone, stored)); line != nil {
			changes = append(changes, line)
		} else {
			changes = append(changes, unsupportedImportLine(
				hubv1.ConfigEntity_CONFIG_ENTITY_ACCOUNT, name, field, "account"))
		}
	}
	return changes, nil
}
