package fleetcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
)

// errReadFile reports a fleet.yaml the CLI could not read or parse. It
// carries no file contents, only the failure.
var errReadFile = errors.New("could not read fleet.yaml")

// errPlanHash reports a bare import: the server applies only the hash of a
// plan the owner already saw, so a run without --dry-run or
// --expected-plan-hash applies nothing and names that flow instead of
// silently applying a plan nobody reviewed.
var errPlanHash = errors.New("import needs the reviewed plan hash")

// errNotPortable reports an export the YAML exchange cannot render yet:
// the hub carries hub IDs (account bindings, tracker projects) whose
// portable references land with #48, so export refuses by name instead of
// failing in the codec. The first unportable path rides along for tests;
// the diagnostic stays fixed.
var errNotPortable = errors.New("this hub cannot be rendered as portable YAML yet")

type importFlags struct {
	json             bool
	requestID        string
	dryRun           bool
	allowSkipped     bool
	expectedPlanHash string
}

func (i *invocation) addImportCommand(root *cobra.Command) {
	var f importFlags
	importCmd := &cobra.Command{
		Use: "import FILE", Short: "Show a fleet.yaml plan and apply a reviewed one",
		Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
			return i.configImport(c, &f, argv[0])
		},
	}
	bindWriteFlags(importCmd.Flags(), &f.json, &f.requestID, nil, nil, nil)
	importCmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "show the plan without applying it")
	importCmd.Flags().BoolVar(&f.allowSkipped, "allow-skipped", false, "apply the lines that can apply despite skipped ones")
	importCmd.Flags().StringVar(&f.expectedPlanHash, "expected-plan-hash", "", "apply only when the plan still hashes to this")
	root.AddCommand(importCmd)
}

// portableConfig drops the server-stamped ids the exchange document must
// not carry. The hub stamps repo.code_host (an integration id) on export,
// but a document carries ids nowhere and import re-derives the host by
// matching, so clearing it is lossless; --json keeps the raw response.
// Anything else unportable (tracker or binding machine ids) still fails,
// loudly and by name: dropping those would silently lose them on
// re-import, and portable references with hub-side translation are #48's
// work, so export names the gap instead of an opaque codec error.
func portableConfig(cfg *hubv1.FleetConfig) (*hubv1.FleetConfig, error) {
	for _, r := range cfg.GetRepos() {
		if repo := r.GetRepo(); repo != nil {
			repo.CodeHost = nil
		}
	}
	if unportable := fleetconfig.UnportableIDs(cfg); len(unportable) > 0 {
		return nil, fmt.Errorf("%w: %s", errNotPortable, unportable[0])
	}
	return cfg, nil
}

// readImportFile loads one fleet.yaml exchange document. The file is the
// command's named input, so reading it is the feature, not a secret read.
func readImportFile(path string) (*hubv1.FleetConfig, error) {
	if path == "" || !validText(path, 4096) {
		return nil, fmt.Errorf("%w: bad file name", errReadFile)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errReadFile, err)
	}
	if info.IsDir() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("%w: %w", errReadFile, errors.New("refusing "+path))
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the owner's named import file, size-capped above
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errReadFile, err)
	}
	cfg, err := fleetconfig.Decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errReadFile, err)
	}
	return cfg, nil
}

func (i *invocation) configImport(c *cobra.Command, f *importFlags, path string) error {
	if f.dryRun && f.expectedPlanHash != "" {
		return errUsage
	}
	if !validText(f.expectedPlanHash, 256) {
		return errUsage
	}
	cfg, err := readImportFile(path)
	if err != nil {
		return fmt.Errorf("%w: %w", errCommand, err)
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		plan := func(id string) (*hubv1.ImportConfigResponse, error) {
			res, err := client.ImportConfig(ctx, withSecret(&hubv1.ImportConfigRequest{
				RequestId: id, Config: cfg, DryRun: true,
			}, secret))
			if err != nil {
				return nil, err
			}
			return res.Msg, nil
		}
		switch {
		case f.dryRun:
			res, err := plan(requestID)
			if err != nil {
				return nil, err
			}
			return renderPlan(f.json, res)
		case f.expectedPlanHash != "":
			res, err := client.ImportConfig(ctx, withSecret(&hubv1.ImportConfigRequest{
				RequestId: requestID, Config: cfg,
				ExpectedPlanHash: f.expectedPlanHash, AllowSkipped: f.allowSkipped,
			}, secret))
			if err != nil {
				return nil, err
			}
			return renderPlan(f.json, res.Msg)
		default:
			// No reviewed hash: the plan this run would apply was never
			// shown, so apply nothing. The owner reviews it with
			// --dry-run and re-runs quoting its hash.
			return nil, fmt.Errorf("%w: %w", errCommand, errPlanHash)
		}
	})
}

// renderPlan prints the server's plan: one line per change, the plan hash
// the apply must quote, and whether anything was applied.
func renderPlan(jsonOut bool, res *hubv1.ImportConfigResponse) ([]byte, error) {
	if res == nil {
		return nil, errMalformed
	}
	if jsonOut {
		return writeJSON(res)
	}
	var b strings.Builder
	for _, change := range res.GetChanges() {
		if change == nil {
			continue
		}
		entity := strings.TrimPrefix(change.GetEntity().String(), "CONFIG_ENTITY_")
		kind := strings.TrimPrefix(change.GetKind().String(), "CONFIG_CHANGE_KIND_")
		// The name comes from the owner's document and may carry terminal
		// control characters, so it is quoted at this output boundary.
		fmt.Fprintf(&b, "%s %s %s", entity, strconvQuote(change.GetName()), kind)
		if len(change.GetChangedFields()) > 0 {
			fmt.Fprintf(&b, " changed=%s", strings.Join(change.GetChangedFields(), ","))
		}
		if problem := change.GetProblem(); problem != nil {
			fmt.Fprintf(&b, " problem=%s", problem.GetReason().String())
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "plan_hash %s\napplied %v\n", res.GetPlanHash(), res.GetApplied())
	return []byte(b.String()), nil
}
