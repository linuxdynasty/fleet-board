package fleetcli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type settingsFlags struct {
	json            bool
	requestID       string
	updateMask      string
	expectedVersion uint64
	specJSON        string
	maxActive       uint32
	maxTotal        uint32
	maxWindows      uint32
	spoolCapBytes   uint64
	shareEmail      bool
	blockOnBattery  bool
	windowTemplate  string
	defaultServer   string
	certLifetime    string
	idleThreshold   string
	attachTimeout   string
	probeInterval   string
	mergedGrace     string
	autoDispatch    bool
	preferNonHub    bool
	pauseAtReserve  bool
	maxPerRepo      uint32
	defaultConsent  string
	triageExpiry    string
	approvalTimeout string
	logSizeCap      uint64
	logRetention    string
	suggestTriage   bool
	passkeySignIn   bool
	githubSignIn    bool
	sessionIdle     string
	sessionMaxAge   string
}

func (i *invocation) addSettingsCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "settings", Short: "Tune fleet settings", Args: cobra.NoArgs}
	var f settingsFlags
	update := &cobra.Command{Use: "update", Short: "Change the fleet settings layer", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return i.settingsUpdate(c, &f)
	}}
	bindWriteFlags(update.Flags(), &f.json, &f.requestID, &f.updateMask, &f.expectedVersion, nil)
	update.Flags().Uint32Var(&f.maxActive, "max-active", 0, "goals at once per machine (1 to 16)")
	update.Flags().Uint32Var(&f.maxTotal, "max-total", 0, "total goals per machine (1 to 32)")
	update.Flags().Uint32Var(&f.maxWindows, "max-windows", 0, "terminal windows per machine")
	update.Flags().Uint64Var(&f.spoolCapBytes, "spool-cap-bytes", 0, "agent log spool cap")
	update.Flags().BoolVar(&f.shareEmail, "share-identity-email", false, "show the harness email as-is")
	update.Flags().BoolVar(&f.blockOnBattery, "block-on-battery", false, "hold goal starts on battery")
	update.Flags().StringVar(&f.windowTemplate, "window-name-template", "", "terminal window name template")
	update.Flags().StringVar(&f.defaultServer, "default-server", "", "NONE, OBSERVE or CONTROL")
	update.Flags().StringVar(&f.certLifetime, "cert-lifetime", "", "agent certificate life, like 24h")
	update.Flags().StringVar(&f.idleThreshold, "idle-threshold", "", "idle pane age, like 30m")
	update.Flags().StringVar(&f.attachTimeout, "attach-idle-timeout", "", "attach idle cut, like 30m")
	update.Flags().StringVar(&f.probeInterval, "probe-interval", "", "account probe gap, like 10m")
	update.Flags().StringVar(&f.mergedGrace, "merged-grace", "", "worktree prune grace, like 1h")
	update.Flags().BoolVar(&f.autoDispatch, "auto-dispatch", false, "master switch for auto-dispatch")
	update.Flags().BoolVar(&f.preferNonHub, "prefer-non-hub", false, "prefer other machines over the hub machine")
	update.Flags().BoolVar(&f.pauseAtReserve, "auto-pause-at-reserve", false, "pause working goals at the reserve line")
	update.Flags().Uint32Var(&f.maxPerRepo, "max-parallel-goals-per-repo", 0, "fleet default live-goal cap per repo")
	update.Flags().StringVar(&f.defaultConsent, "default-consent", "", "comma list like PUSH,PR_OPEN,MERGE")
	update.Flags().StringVar(&f.triageExpiry, "triage-expiry", "", "triage item life, like 168h")
	update.Flags().StringVar(&f.approvalTimeout, "approval-timeout", "", "approval wait, like 30m")
	update.Flags().Uint64Var(&f.logSizeCap, "log-size-cap-bytes", 0, "stored session log cap")
	update.Flags().StringVar(&f.logRetention, "log-retention", "", "stored log age, like 720h")
	update.Flags().BoolVar(&f.suggestTriage, "suggestions-in-triage", false, "raise suggestions as triage items")
	update.Flags().BoolVar(&f.passkeySignIn, "passkey-sign-in", false, "allow passkey sign-in")
	update.Flags().BoolVar(&f.githubSignIn, "github-sign-in", false, "allow GitHub sign-in")
	update.Flags().StringVar(&f.sessionIdle, "session-idle-timeout", "", "browser idle sign-out, like 168h")
	update.Flags().StringVar(&f.sessionMaxAge, "session-max-age", "", "browser absolute sign-out, like 720h")
	update.Flags().StringVar(&f.specJSON, "spec-json", "", "proto JSON of SettingsSpec; needs --update-mask for its paths")
	parent.AddCommand(update)
	i.addIntegrationCommands(parent)
	root.AddCommand(parent)
}

func parseDurationFlag(raw string) (*durationpb.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < 0 {
		return nil, errUsage
	}
	return durationpb.New(d), nil
}

func parseDefaultServer(raw string) (typev1.DefaultServerMode, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if short, ok := map[string]string{
		"NONE": "DEFAULT_SERVER_MODE_NONE", "OBSERVE": "DEFAULT_SERVER_MODE_OBSERVE",
		"CONTROL": "DEFAULT_SERVER_MODE_CONTROL",
	}[name]; ok {
		name = short
	}
	v, ok := typev1.DefaultServerMode_value[name]
	if !ok || name == "DEFAULT_SERVER_MODE_UNSPECIFIED" {
		return 0, errUsage
	}
	return typev1.DefaultServerMode(v), nil
}

// overlaySettingsSpec applies --spec-json first, then every scalar flag the
// owner set, and reports the mask paths those flags imply.
func overlaySettingsSpec(c *cobra.Command, f *settingsFlags) (*hubv1.SettingsSpec, []string, error) {
	spec := &hubv1.SettingsSpec{}
	if err := specFromJSON(f.specJSON, spec); err != nil {
		return nil, nil, err
	}
	if spec.GetValues() == nil {
		spec.Values = &typev1.SettingValues{}
	}
	if spec.GetHub() == nil {
		spec.Hub = &hubv1.HubSettings{}
	}
	values, hub := spec.GetValues(), spec.GetHub()
	var derived []string
	changed := c.Flags().Changed
	uint32Value := func(flag, path string, v uint32, set func(uint32)) {
		if changed(flag) {
			p := v
			set(p)
			derived = append(derived, path)
		}
	}
	uint32Value("max-active", "spec.values.max_active", f.maxActive, func(v uint32) { values.MaxActive = &v })
	uint32Value("max-total", "spec.values.max_total", f.maxTotal, func(v uint32) { values.MaxTotal = &v })
	uint32Value("max-windows", "spec.values.max_windows", f.maxWindows, func(v uint32) { values.MaxWindows = &v })
	if changed("spool-cap-bytes") {
		v := f.spoolCapBytes
		values.SpoolCapBytes = &v
		derived = append(derived, "spec.values.spool_cap_bytes")
	}
	if changed("share-identity-email") {
		v := f.shareEmail
		values.ShareIdentityEmail = &v
		derived = append(derived, "spec.values.share_identity_email")
	}
	if changed("block-on-battery") {
		v := f.blockOnBattery
		values.BlockOnBattery = &v
		derived = append(derived, "spec.values.block_on_battery")
	}
	if changed("window-name-template") {
		if !validText(f.windowTemplate, 1024) {
			return nil, nil, errUsage
		}
		values.WindowNameTemplate = &f.windowTemplate
		derived = append(derived, "spec.values.window_name_template")
	}
	if changed("default-server") {
		mode, err := parseDefaultServer(f.defaultServer)
		if err != nil {
			return nil, nil, err
		}
		values.DefaultServer = &mode
		derived = append(derived, "spec.values.default_server")
	}
	durationValue := func(flag, path string, raw string, set func(*durationpb.Duration)) error {
		if changed(flag) {
			d, err := parseDurationFlag(raw)
			if err != nil {
				return err
			}
			set(d)
			derived = append(derived, path)
		}
		return nil
	}
	for _, d := range []struct {
		flag, path, raw string
		set             func(*durationpb.Duration)
	}{
		{"cert-lifetime", "spec.values.cert_lifetime", f.certLifetime, func(d *durationpb.Duration) { values.CertLifetime = d }},
		{"idle-threshold", "spec.values.idle_threshold", f.idleThreshold, func(d *durationpb.Duration) { values.IdleThreshold = d }},
		{"attach-idle-timeout", "spec.values.attach_idle_timeout", f.attachTimeout, func(d *durationpb.Duration) { values.AttachIdleTimeout = d }},
		{"probe-interval", "spec.values.probe_interval", f.probeInterval, func(d *durationpb.Duration) { values.ProbeInterval = d }},
		{"merged-grace", "spec.values.merged_grace", f.mergedGrace, func(d *durationpb.Duration) { values.MergedGrace = d }},
		{"triage-expiry", "spec.hub.triage_expiry", f.triageExpiry, func(d *durationpb.Duration) { hub.TriageExpiry = d }},
		{"approval-timeout", "spec.hub.approval_timeout", f.approvalTimeout, func(d *durationpb.Duration) { hub.ApprovalTimeout = d }},
		{"session-idle-timeout", "spec.hub.session_idle_timeout", f.sessionIdle, func(d *durationpb.Duration) { hub.SessionIdleTimeout = d }},
		{"session-max-age", "spec.hub.session_max_age", f.sessionMaxAge, func(d *durationpb.Duration) { hub.SessionMaxAge = d }},
		{"log-retention", "spec.hub.log_retention", f.logRetention, func(d *durationpb.Duration) { hub.LogRetention = d }},
	} {
		if err := durationValue(d.flag, d.path, d.raw, d.set); err != nil {
			return nil, nil, err
		}
	}
	if changed("auto-dispatch") {
		v := f.autoDispatch
		hub.AutoDispatch = &v
		derived = append(derived, "spec.hub.auto_dispatch")
	}
	if changed("prefer-non-hub") {
		v := f.preferNonHub
		hub.PreferNonHub = &v
		derived = append(derived, "spec.hub.prefer_non_hub")
	}
	if changed("auto-pause-at-reserve") {
		v := f.pauseAtReserve
		hub.AutoPauseAtReserve = &v
		derived = append(derived, "spec.hub.auto_pause_at_reserve")
	}
	if changed("max-parallel-goals-per-repo") {
		v := f.maxPerRepo
		hub.MaxParallelGoalsPerRepo = &v
		derived = append(derived, "spec.hub.max_parallel_goals_per_repo")
	}
	if changed("default-consent") {
		acts, err := parseConsentActs(f.defaultConsent)
		if err != nil {
			return nil, nil, err
		}
		hub.DefaultConsent = acts
		derived = append(derived, "spec.hub.default_consent")
	}
	if changed("log-size-cap-bytes") {
		v := f.logSizeCap
		hub.LogSizeCapBytes = &v
		derived = append(derived, "spec.hub.log_size_cap_bytes")
	}
	if changed("suggestions-in-triage") {
		v := f.suggestTriage
		hub.SuggestionsInTriage = &v
		derived = append(derived, "spec.hub.suggestions_in_triage")
	}
	if changed("passkey-sign-in") {
		v := f.passkeySignIn
		hub.PasskeySignIn = &v
		derived = append(derived, "spec.hub.passkey_sign_in")
	}
	if changed("github-sign-in") {
		v := f.githubSignIn
		hub.GithubSignIn = &v
		derived = append(derived, "spec.hub.github_sign_in")
	}
	for _, s := range []string{
		f.windowTemplate, f.defaultServer, f.defaultConsent, f.specJSON,
		f.certLifetime, f.idleThreshold, f.attachTimeout, f.probeInterval, f.mergedGrace,
		f.triageExpiry, f.approvalTimeout, f.sessionIdle, f.sessionMaxAge, f.logRetention,
	} {
		if !validText(s, 1<<20) {
			return nil, nil, errUsage
		}
	}
	return spec, derived, nil
}

func (i *invocation) settingsUpdate(c *cobra.Command, f *settingsFlags) error {
	spec, derived, err := overlaySettingsSpec(c, f)
	if err != nil {
		return err
	}
	mask, err := unionMask(f.updateMask, derived)
	if err != nil {
		return err
	}
	// Only the fleet layer is writable in this build; group and machine
	// layers arrive through fleet import.
	spec.Scope = &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Fleet_{Fleet: &hubv1.SettingsScope_Fleet{}}}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateSettings(ctx, withSecret(&hubv1.UpdateSettingsRequest{
			RequestId:       requestID,
			Settings:        &hubv1.Settings{Spec: spec},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: mask},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetSettings() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("settings %s version %d\n",
			res.Msg.GetSettings().GetId().GetValue(), res.Msg.GetSettings().GetVersion())), nil
	})
}

type integrationFlags struct {
	json            bool
	requestID       string
	updateMask      string
	expectedVersion uint64
	id              string
	label           string
	specJSON        string
}

func (i *invocation) addIntegrationCommands(parent *cobra.Command) {
	mid := &cobra.Command{Use: "integration", Short: "Tune provider integrations", Args: cobra.NoArgs}
	var f integrationFlags
	update := &cobra.Command{Use: "update", Short: "Relabel or retarget an integration", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return i.integrationUpdate(c, &f)
	}}
	bindWriteFlags(update.Flags(), &f.json, &f.requestID, &f.updateMask, &f.expectedVersion, nil)
	update.Flags().StringVar(&f.id, "id", "", "integration id, e.g. github (required)")
	update.Flags().StringVar(&f.label, "label", "", "owner display name")
	update.Flags().StringVar(&f.specJSON, "spec-json", "", "proto JSON of IntegrationSpec; needs --update-mask for its paths")
	mid.AddCommand(update)
	parent.AddCommand(mid)
}

func (i *invocation) integrationUpdate(c *cobra.Command, f *integrationFlags) error {
	if f.id == "" || !validText(f.id, 256) {
		return errUsage
	}
	spec := &hubv1.IntegrationSpec{}
	if err := specFromJSON(f.specJSON, spec); err != nil {
		return err
	}
	var derived []string
	if c.Flags().Changed("label") {
		if !validText(f.label, 1024) {
			return errUsage
		}
		spec.Label = f.label
		derived = append(derived, "spec.label")
	}
	mask, err := unionMask(f.updateMask, derived)
	if err != nil {
		return err
	}
	for _, s := range []string{f.id, f.label, f.specJSON, f.updateMask} {
		if !validText(s, 1<<20) {
			return errUsage
		}
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateIntegration(ctx, withSecret(&hubv1.UpdateIntegrationRequest{
			RequestId:       requestID,
			Integration:     &hubv1.Integration{Id: &typev1.IntegrationId{Value: f.id}, Spec: spec},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: mask},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetIntegration() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return []byte(fmt.Sprintf("integration %s version %d\n",
			res.Msg.GetIntegration().GetId().GetValue(), res.Msg.GetIntegration().GetVersion())), nil
	})
}
