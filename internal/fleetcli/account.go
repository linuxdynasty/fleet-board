package fleetcli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type accountFlags struct {
	json            bool
	requestID       string
	updateMask      string
	expectedVersion uint64
	yes             bool
	specJSON        string
	harness         string
	provider        string
	displayName     string
	mode            string
	budgetKind      string
	reservePercent  uint32
	consent         string
	spendCapMicros  int64
}

func (i *invocation) addAccountCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "account", Short: "Own harness accounts", Args: cobra.NoArgs}
	var cf, uf, df accountFlags
	create := &cobra.Command{Use: "create", Short: "Add an owner API-key account", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return i.accountCreate(c, &cf)
	}}
	update := &cobra.Command{Use: "update ID", Short: "Change an account", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.accountUpdate(c, &uf, argv[0])
	}}
	deleteCmd := &cobra.Command{Use: "delete ID", Short: "Remove an unused account", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.accountDelete(c, &df, argv[0])
	}}
	bindWriteFlags(create.Flags(), &cf.json, &cf.requestID, nil, nil, nil)
	create.Flags().StringVar(&cf.harness, "harness", "", "harness name, e.g. opencode (required)")
	create.Flags().StringVar(&cf.provider, "provider", "", "open provider name, e.g. anthropic (required)")
	bindAccountSpecFlags(create.Flags(), &cf)
	bindWriteFlags(update.Flags(), &uf.json, &uf.requestID, &uf.updateMask, &uf.expectedVersion, nil)
	bindAccountSpecFlags(update.Flags(), &uf)
	bindWriteFlags(deleteCmd.Flags(), &df.json, &df.requestID, nil, &df.expectedVersion, &df.yes)
	parent.AddCommand(create, update, deleteCmd)
	root.AddCommand(parent)
}

func bindAccountSpecFlags(fs *pflag.FlagSet, f *accountFlags) {
	fs.StringVar(&f.displayName, "display-name", "", "owner-chosen name")
	fs.StringVar(&f.mode, "mode", "", "FLEET or INTERACTIVE_ONLY")
	fs.StringVar(&f.budgetKind, "budget-kind", "", "DOLLAR_CAP for an API-key account")
	fs.StringVar(&f.consent, "consent", "", "comma list like PUSH,PR_OPEN,MERGE")
	fs.StringVar(&f.specJSON, "spec-json", "", "proto JSON of AccountSpec for bindings and the rest")
	fs.Uint32Var(&f.reservePercent, "reserve-percent", 0, "quota kept for interactive use")
	fs.Int64Var(&f.spendCapMicros, "spend-cap-micros", 0, "owner dollar cap in micro-units, 0 for none")
}

func parseAccountMode(raw string) (hubv1.AccountMode, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if short, ok := map[string]string{
		"FLEET": "ACCOUNT_MODE_FLEET", "INTERACTIVE_ONLY": "ACCOUNT_MODE_INTERACTIVE_ONLY",
	}[name]; ok {
		name = short
	}
	v, ok := hubv1.AccountMode_value[name]
	if !ok || name == "ACCOUNT_MODE_UNSPECIFIED" {
		return 0, errUsage
	}
	return hubv1.AccountMode(v), nil
}

func parseBudgetKind(raw string) (typev1.BudgetKind, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if short, ok := map[string]string{
		"DOLLAR_CAP": "BUDGET_KIND_DOLLAR_CAP", "SUBSCRIPTION_PERCENT": "BUDGET_KIND_SUBSCRIPTION_PERCENT",
	}[name]; ok {
		name = short
	}
	v, ok := typev1.BudgetKind_value[name]
	if !ok || name == "BUDGET_KIND_UNSPECIFIED" {
		return 0, errUsage
	}
	return typev1.BudgetKind(v), nil
}

// overlayAccountSpec applies --spec-json first, then every scalar flag the
// owner set, and reports the mask paths those flags imply.
func overlayAccountSpec(c *cobra.Command, f *accountFlags) (*hubv1.AccountSpec, []string, error) {
	spec := &hubv1.AccountSpec{}
	if err := specFromJSON(f.specJSON, spec); err != nil {
		return nil, nil, err
	}
	var derived []string
	changed := c.Flags().Changed
	if changed("display-name") {
		if !validText(f.displayName, 1024) || f.displayName == "" {
			return nil, nil, errUsage
		}
		spec.DisplayName = f.displayName
		derived = append(derived, "spec.display_name")
	}
	if changed("mode") {
		mode, err := parseAccountMode(f.mode)
		if err != nil {
			return nil, nil, err
		}
		spec.Mode = mode
		derived = append(derived, "spec.mode")
	}
	if changed("budget-kind") {
		kind, err := parseBudgetKind(f.budgetKind)
		if err != nil {
			return nil, nil, err
		}
		spec.BudgetKind = kind
		derived = append(derived, "spec.budget_kind")
	}
	if changed("reserve-percent") {
		spec.ReservePercent = f.reservePercent
		derived = append(derived, "spec.reserve_percent")
	}
	if changed("consent") {
		acts, err := parseConsentActs(f.consent)
		if err != nil {
			return nil, nil, err
		}
		spec.Consent = acts
		derived = append(derived, "spec.consent")
	}
	if changed("spend-cap-micros") {
		spec.SpendCapMicros = f.spendCapMicros
		derived = append(derived, "spec.spend_cap_micros")
	}
	for _, s := range []string{f.harness, f.provider, f.displayName, f.mode, f.budgetKind, f.consent, f.specJSON} {
		if !validText(s, 1<<20) {
			return nil, nil, errUsage
		}
	}
	return spec, derived, nil
}

func (i *invocation) accountCreate(c *cobra.Command, f *accountFlags) error {
	if f.harness == "" || f.provider == "" || !validText(f.harness, 256) || !validText(f.provider, 64) {
		return errUsage
	}
	spec, _, err := overlayAccountSpec(c, f)
	if err != nil {
		return err
	}
	if spec.GetDisplayName() == "" {
		return errUsage
	}
	// An owner-created account bills by the dollar; that is the only kind
	// the server admits, so it is the default unless --spec-json said otherwise.
	if !c.Flags().Changed("budget-kind") && spec.GetBudgetKind() == typev1.BudgetKind_BUDGET_KIND_UNSPECIFIED {
		spec.BudgetKind = typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP
	}
	// A new account joins the fleet unless the owner says otherwise.
	if !c.Flags().Changed("mode") && spec.GetMode() == hubv1.AccountMode_ACCOUNT_MODE_UNSPECIFIED {
		spec.Mode = hubv1.AccountMode_ACCOUNT_MODE_FLEET
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.CreateAccount(ctx, withSecret(&hubv1.CreateAccountRequest{
			RequestId: requestID,
			Harness:   &typev1.Harness{Name: f.harness},
			Provider:  f.provider,
			Spec:      spec,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetAccount() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("account %s version %d\n", res.Msg.GetAccount().GetId().GetValue(), res.Msg.GetAccount().GetVersion())), nil
	})
}

func (i *invocation) accountUpdate(c *cobra.Command, f *accountFlags, id string) error {
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	spec, derived, err := overlayAccountSpec(c, f)
	if err != nil {
		return err
	}
	mask, err := unionMask(f.updateMask, derived)
	if err != nil {
		return err
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateAccount(ctx, withSecret(&hubv1.UpdateAccountRequest{
			RequestId:       requestID,
			Account:         &hubv1.Account{Id: &typev1.AccountId{Value: id}, Spec: spec},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: mask},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetAccount() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("account %s version %d\n", res.Msg.GetAccount().GetId().GetValue(), res.Msg.GetAccount().GetVersion())), nil
	})
}

func (i *invocation) accountDelete(c *cobra.Command, f *accountFlags, id string) error {
	if !f.yes {
		return errUsage
	}
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		_, err := client.DeleteAccount(ctx, withSecret(&hubv1.DeleteAccountRequest{
			RequestId:       requestID,
			Id:              &typev1.AccountId{Value: id},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if f.json {
			return []byte("{}\n"), nil
		}
		return []byte(fmt.Sprintf("deleted account %s\n", id)), nil
	})
}
