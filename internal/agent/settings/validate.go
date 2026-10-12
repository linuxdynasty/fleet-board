package settings

import (
	"path/filepath"
	"regexp"
	"slices"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

var (
	harnessName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	labelName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

// rejection is one field the agent did not apply. msg is a fixed phrase:
// never the hub's value (it may be a path).
type rejection struct {
	field  string
	reason typev1.ReasonCode
	msg    string
}

// valueChecks are the checks of each SettingValues field the hub may set;
// each returns "" for a value the agent can apply, else a fixed phrase.
// merged_grace is the hub's own and is echoed unchecked (03e).
var valueChecks = map[string]func(v *typev1.SettingValues) string{
	fMaxActive: func(v *typev1.SettingValues) string {
		return inRange(v.GetMaxActive(), 1, maxActiveHi, "must be 1 to 16")
	},
	fMaxTotal: func(v *typev1.SettingValues) string {
		return inRange(v.GetMaxTotal(), 1, maxTotalHi, "must be 1 to 32")
	},
	fMaxWindows: func(v *typev1.SettingValues) string {
		return inRange(v.GetMaxWindows(), 1, ^uint32(0), "must be at least 1")
	},
	fCertLifetime: func(v *typev1.SettingValues) string {
		if msg := positive(v.GetCertLifetime()); msg != "" {
			return msg
		}
		if v.GetCertLifetime().AsDuration() > certLifetimeHi {
			return "must be up to 365 days"
		}
		return ""
	},
	fAttachIdleTimeout: func(v *typev1.SettingValues) string { return positive(v.GetAttachIdleTimeout()) },
	fIdleThreshold:     func(v *typev1.SettingValues) string { return positive(v.GetIdleThreshold()) },
	fProbeInterval:     func(v *typev1.SettingValues) string { return positive(v.GetProbeInterval()) },
	fDefaultServer: func(v *typev1.SettingValues) string {
		switch v.GetDefaultServer() {
		case typev1.DefaultServerMode_DEFAULT_SERVER_MODE_NONE, typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE,
			typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL:
			return ""
		case typev1.DefaultServerMode_DEFAULT_SERVER_MODE_UNSPECIFIED:
		}
		return "must be none, observe or control"
	},
	fAllowedHarnesses: func(v *typev1.SettingValues) string {
		return eachOK(v.GetAllowedHarnesses(), harnessName.MatchString, "names are lowercase letters, digits and -")
	},
	fAllowedRepoRoots: func(v *typev1.SettingValues) string {
		return eachOK(v.GetAllowedRepoRoots(), func(s string) bool {
			return filepath.IsAbs(s) && printable(s, maxRoot)
		}, "every entry must be an absolute path")
	},
	fLocalApproval: func(v *typev1.SettingValues) string {
		kinds := make([]string, 0, len(v.GetLocalApproval()))
		for _, r := range v.GetLocalApproval() {
			kinds = append(kinds, r.GetCommandKind())
		}
		return kindsOK(kinds)
	},
	fRateLimits: func(v *typev1.SettingValues) string {
		kinds := make([]string, 0, len(v.GetRateLimits()))
		for _, r := range v.GetRateLimits() {
			if r.GetPerMinute() < 1 {
				return "per_minute must be at least 1"
			}
			kinds = append(kinds, r.GetCommandKind())
		}
		return kindsOK(kinds)
	},
	fWindowNameTemplate: func(v *typev1.SettingValues) string {
		if !printable(v.GetWindowNameTemplate(), maxTemplate) {
			return "no control characters, at most 256 bytes"
		}
		return ""
	},
}

func inRange(u, lo, hi uint32, want string) string {
	if u < lo || u > hi {
		return want
	}
	return ""
}

func positive(d *durationpb.Duration) string {
	if d.CheckValid() != nil {
		return "must be a valid duration"
	}
	if d.AsDuration() <= 0 {
		return "must be above zero"
	}
	return ""
}

func eachOK(list []string, ok func(string) bool, bad string) string {
	for _, s := range list {
		if !ok(s) {
			return bad
		}
	}
	return ""
}

// kindsOK checks command kinds: each an arm of Command.kind, each once.
func kindsOK(kinds []string) string {
	seen := map[string]bool{}
	for _, k := range kinds {
		if !policy.IsCommandKind(k) {
			return "command_kind is not a command kind"
		}
		if seen[k] {
			return "a command kind is given twice"
		}
		seen[k] = true
	}
	return ""
}

func printable(s string, maxBytes int) bool {
	if len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// keep checks the hub's message and returns the Settings the agent keeps:
// the message with each invalid field replaced by the one kept before it
// (prev; nil when none), and the INVALID_ARGUMENT rejections (03e failure
// table). Unknown fields stay in the kept message.
func keep(in, prev *agentv1.Settings) (*agentv1.Settings, []rejection) {
	kept, _ := proto.Clone(in).(*agentv1.Settings)
	if kept.GetValues() == nil {
		kept.Values = &typev1.SettingValues{}
	}
	var out []rejection
	vr := kept.GetValues().ProtoReflect()
	pr := prev.GetValues().ProtoReflect()
	fields := vr.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		check := valueChecks[string(fd.Name())]
		if check == nil || !vr.Has(fd) {
			continue
		}
		if msg := check(kept.GetValues()); msg != "" {
			out = append(out, rejection{field: string(fd.Name()), reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: msg})
			if pr.IsValid() && pr.Has(fd) {
				vr.Set(fd, pr.Get(fd))
			} else {
				vr.Clear(fd)
			}
		}
	}
	for _, id := range sortedKeys(kept.GetProbeIntervalByAccount()) {
		msg := positive(kept.GetProbeIntervalByAccount()[id])
		if !printable(id, maxAccountID) || id == "" {
			msg = "the account id is not printable"
		}
		if msg == "" {
			continue
		}
		field := fProbeByAccount
		if printable(id, maxAccountID) && id != "" {
			field += "." + id
		}
		out = append(out, rejection{field: field, reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: msg})
		if old, ok := prev.GetProbeIntervalByAccount()[id]; ok {
			kept.ProbeIntervalByAccount[id] = old
		} else {
			delete(kept.GetProbeIntervalByAccount(), id)
		}
	}
	if msg := eachOK(kept.GetLabels(), labelName.MatchString, "each label is [a-z0-9][a-z0-9-]{0,31}"); msg != "" {
		out = append(out, rejection{field: fLabels, reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: msg})
		kept.Labels = slices.Clone(prev.GetLabels())
	}
	return kept, out
}

// sent lists every field the message states, for the safe-mode rejections.
func sent(s *agentv1.Settings) []string {
	var out []string
	vr := s.GetValues().ProtoReflect()
	if vr.IsValid() {
		fields := vr.Descriptor().Fields()
		for i := range fields.Len() {
			if vr.Has(fields.Get(i)) {
				out = append(out, string(fields.Get(i).Name()))
			}
		}
	}
	if len(s.GetLabels()) > 0 {
		out = append(out, fLabels)
	}
	for _, id := range sortedKeys(s.GetProbeIntervalByAccount()) {
		if printable(id, maxAccountID) && id != "" {
			out = append(out, fProbeByAccount+"."+id)
		} else if !slices.Contains(out, fProbeByAccount) {
			out = append(out, fProbeByAccount)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
