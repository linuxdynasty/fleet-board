package settings

import (
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Field names as SettingValues names them (SettingSource.field,
// capped_fields, SettingRejection.field). TestFieldNamesAreSettingValues
// checks each against the descriptor.
const (
	fMaxActive          = "max_active"
	fMaxTotal           = "max_total"
	fMaxWindows         = "max_windows"
	fLocalApproval      = "local_approval"
	fRateLimits         = "rate_limits"
	fCertLifetime       = "cert_lifetime"
	fAllowedHarnesses   = "allowed_harnesses"
	fAllowedRepoRoots   = "allowed_repo_roots"
	fDefaultServer      = "default_server"
	fWindowNameTemplate = "window_name_template"
	fIdleThreshold      = "idle_threshold"
	fAttachIdleTimeout  = "attach_idle_timeout"
	fShareIdentityEmail = "share_identity_email"
	fSpoolCapBytes      = "spool_cap_bytes"
	fBlockOnBattery     = "block_on_battery"
	fProbeInterval      = "probe_interval"
	fMergedGrace        = "merged_grace"
	// fLabels and fProbeByAccount are fields of Settings, named in a
	// rejection ("probe_interval_by_account.<account id>").
	fLabels         = "labels"
	fProbeByAccount = "probe_interval_by_account"
)

// The layers the merge names. hubLayer is a field whose hub value stood:
// Settings carries resolved values only, so the agent names it HUB and the
// hub replaces that with the fleet, group or machine layer (03e).
const (
	hubLayer     = typev1.SettingLayer_SETTING_LAYER_HUB
	localLayer   = typev1.SettingLayer_SETTING_LAYER_LOCAL_CEILING
	defaultLayer = typev1.SettingLayer_SETTING_LAYER_AGENT_DEFAULT
	noLayer      = typev1.SettingLayer(-1) // no source entry
)

// Built-in defaults when no layer states a field (defaults.md, "Agent" and
// "Settings"; 03e "The local policy file").
const (
	defMaxActive          = 2
	defMaxTotal           = 4
	defMaxWindows         = 12
	defCertLifetime       = 90 * 24 * time.Hour
	defAttachIdleTimeout  = 30 * time.Minute
	defIdleThreshold      = 2 * time.Minute
	defSpoolCapBytes      = 2 << 30
	defWindowNameTemplate = "{harness} {work}"
	defDefaultServer      = typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL
)

// Ranges the hub's values must keep (03e key table; ADR 0024 decision 18).
const (
	maxActiveHi    = 16
	maxTotalHi     = 32
	certLifetimeHi = 365 * 24 * time.Hour
	maxTemplate    = 256
	maxRoot        = 4096
	maxAccountID   = 128
)

// knownHarnesses is allowed_harnesses when neither layer has an opinion:
// the names ids.proto knows, until C1 reports the installed ones (plan row
// 8; ruling row 6 fork 6).
func knownHarnesses() []string { return []string{"claude", "codex", "muse", "cursor", "opencode"} }
