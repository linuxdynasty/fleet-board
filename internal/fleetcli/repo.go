package fleetcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type repoFlags struct {
	json            bool
	requestID       string
	updateMask      string
	expectedVersion uint64
	yes             bool
	specJSON        string
	enabled         bool
	autoDispatch    bool
	maxParallel     uint32
	branchPattern   string
	baseBranch      string
	worktreeMode    string
	hooksMode       string
	consent         string
}

func (i *invocation) addRepoCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "repo", Short: "Watch and tune repositories", Args: cobra.NoArgs}
	var cf, uf, df repoFlags
	create := &cobra.Command{Use: "create HOST/PATH", Short: "Start watching a repo", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.repoCreate(c, &cf, argv[0])
	}}
	update := &cobra.Command{Use: "update HOST/PATH", Short: "Change a watched repo", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.repoUpdate(c, &uf, argv[0])
	}}
	deleteCmd := &cobra.Command{Use: "delete HOST/PATH", Short: "Stop watching a repo", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.repoDelete(c, &df, argv[0])
	}}
	bindWriteFlags(create.Flags(), &cf.json, &cf.requestID, nil, nil, nil)
	create.Flags().BoolVar(&cf.enabled, "enabled", true, "pause the repo when false")
	create.Flags().StringVar(&cf.specJSON, "spec-json", "", "proto JSON of RepoSpec for the remaining fields")
	bindWriteFlags(update.Flags(), &uf.json, &uf.requestID, &uf.updateMask, &uf.expectedVersion, nil)
	update.Flags().BoolVar(&uf.enabled, "enabled", false, "pause or resume the repo")
	update.Flags().BoolVar(&uf.autoDispatch, "auto-dispatch", false, "auto-dispatch default override")
	update.Flags().Uint32Var(&uf.maxParallel, "max-parallel-goals", 0, "live-goal cap for the repo")
	update.Flags().StringVar(&uf.branchPattern, "branch-pattern", "", "goal branch template")
	update.Flags().StringVar(&uf.baseBranch, "base-branch", "", "base branch for new branches")
	update.Flags().StringVar(&uf.worktreeMode, "worktree-mode", "", "AUTO, AGENT_ENSURES or REPO_DRIVER")
	update.Flags().StringVar(&uf.hooksMode, "hooks-mode", "", "AUTO, ESHU or GENERIC")
	update.Flags().StringVar(&uf.consent, "consent", "", "comma list like PUSH,PR_OPEN,MERGE")
	update.Flags().StringVar(&uf.specJSON, "spec-json", "", "proto JSON of RepoSpec; needs --update-mask for its paths")
	bindWriteFlags(deleteCmd.Flags(), &df.json, &df.requestID, nil, &df.expectedVersion, &df.yes)
	parent.AddCommand(create, update, deleteCmd)
	root.AddCommand(parent)
}

func parseWorktreeMode(raw string) (hubv1.RepoWorktreeMode, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if short, ok := map[string]string{
		"AUTO": "REPO_WORKTREE_MODE_AUTO", "AGENT_ENSURES": "REPO_WORKTREE_MODE_AGENT_ENSURES",
		"REPO_DRIVER": "REPO_WORKTREE_MODE_REPO_DRIVER",
	}[name]; ok {
		name = short
	}
	v, ok := hubv1.RepoWorktreeMode_value[name]
	if !ok || name == "REPO_WORKTREE_MODE_UNSPECIFIED" {
		return 0, errUsage
	}
	return hubv1.RepoWorktreeMode(v), nil
}

func parseHooksMode(raw string) (hubv1.RepoHooksMode, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if short, ok := map[string]string{
		"AUTO": "REPO_HOOKS_MODE_AUTO", "ESHU": "REPO_HOOKS_MODE_ESHU", "GENERIC": "REPO_HOOKS_MODE_GENERIC",
	}[name]; ok {
		name = short
	}
	v, ok := hubv1.RepoHooksMode_value[name]
	if !ok || name == "REPO_HOOKS_MODE_UNSPECIFIED" {
		return 0, errUsage
	}
	return hubv1.RepoHooksMode(v), nil
}

// overlayRepoSpec applies --spec-json first, then every scalar flag the owner
// set, and reports the mask paths those flags imply.
func overlayRepoSpec(c *cobra.Command, f *repoFlags) (*hubv1.RepoSpec, []string, error) {
	spec := &hubv1.RepoSpec{}
	if err := specFromJSON(f.specJSON, spec); err != nil {
		return nil, nil, err
	}
	var derived []string
	changed := c.Flags().Changed
	set := func(flag, path string, apply func()) {
		if changed(flag) {
			apply()
			derived = append(derived, path)
		}
	}
	set("enabled", "spec.enabled", func() { spec.Enabled = f.enabled })
	set("auto-dispatch", "spec.auto_dispatch", func() {
		v := f.autoDispatch
		spec.AutoDispatch = &v
	})
	set("max-parallel-goals", "spec.max_parallel_goals", func() {
		v := f.maxParallel
		spec.MaxParallelGoals = &v
	})
	set("branch-pattern", "spec.branch_pattern", func() { spec.BranchPattern = f.branchPattern })
	set("base-branch", "spec.base_branch", func() { spec.BaseBranch = f.baseBranch })
	if changed("worktree-mode") {
		mode, err := parseWorktreeMode(f.worktreeMode)
		if err != nil {
			return nil, nil, err
		}
		spec.WorktreeMode = mode
		derived = append(derived, "spec.worktree_mode")
	}
	if changed("hooks-mode") {
		mode, err := parseHooksMode(f.hooksMode)
		if err != nil {
			return nil, nil, err
		}
		spec.HooksMode = mode
		derived = append(derived, "spec.hooks_mode")
	}
	if changed("consent") {
		acts, err := parseConsentActs(f.consent)
		if err != nil {
			return nil, nil, err
		}
		spec.Consent = &hubv1.RepoConsent{Acts: acts}
		derived = append(derived, "spec.consent")
	}
	for _, s := range []string{f.branchPattern, f.baseBranch, f.worktreeMode, f.hooksMode, f.consent, f.specJSON} {
		if !validText(s, 1<<20) {
			return nil, nil, errUsage
		}
	}
	return spec, derived, nil
}

// specJSONSetsEnabled reports whether --spec-json explicitly names the
// enabled field. Proto JSON carries no presence for the scalar, so only
// the raw document tells an explicit false from an omitted field; an
// explicit false must survive the true default above.
func specJSONSetsEnabled(raw string) bool {
	if raw == "" {
		return false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return false
	}
	_, ok := doc["enabled"]
	return ok
}

func (i *invocation) repoCreate(c *cobra.Command, f *repoFlags, id string) error {
	host, path, err := splitRepoID(id)
	if err != nil {
		return err
	}
	spec, _, err := overlayRepoSpec(c, f)
	if err != nil {
		return err
	}
	if !c.Flags().Changed("enabled") && !specJSONSetsEnabled(f.specJSON) {
		spec.Enabled = true
	}
	spec.Repo = &typev1.RepoRef{Host: host, Path: path}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.CreateRepo(ctx, withSecret(&hubv1.CreateRepoRequest{
			RequestId: requestID,
			Repo:      &hubv1.Repo{Spec: spec},
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetRepo() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("repo %s version %d\n", res.Msg.GetRepo().GetId().GetValue(), res.Msg.GetRepo().GetVersion())), nil
	})
}

func (i *invocation) repoUpdate(c *cobra.Command, f *repoFlags, id string) error {
	host, path, err := splitRepoID(id)
	if err != nil {
		return err
	}
	spec, derived, err := overlayRepoSpec(c, f)
	if err != nil {
		return err
	}
	mask, err := unionMask(f.updateMask, derived)
	if err != nil {
		return err
	}
	spec.Repo = &typev1.RepoRef{Host: host, Path: path}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateRepo(ctx, withSecret(&hubv1.UpdateRepoRequest{
			RequestId:       requestID,
			Repo:            &hubv1.Repo{Spec: spec},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: mask},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetRepo() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("repo %s version %d\n", res.Msg.GetRepo().GetId().GetValue(), res.Msg.GetRepo().GetVersion())), nil
	})
}

func (i *invocation) repoDelete(c *cobra.Command, f *repoFlags, id string) error {
	if !f.yes {
		return errUsage
	}
	if _, _, err := splitRepoID(id); err != nil {
		return err
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		_, err := client.DeleteRepo(ctx, withSecret(&hubv1.DeleteRepoRequest{
			RequestId:       requestID,
			Id:              &hubv1.RepoId{Value: id},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if f.json {
			return []byte("{}\n"), nil
		}
		return []byte(fmt.Sprintf("deleted repo %s\n", id)), nil
	})
}
