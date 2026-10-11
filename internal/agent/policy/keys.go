package policy

import (
	"math"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

const day = 24 * time.Hour

// topKeys is the key table of 03e, "The local policy file (L1)". A key not in
// it is unknown (safe mode).
var topKeys = map[string]handler{
	"schema_version": func(*Policy, *yaml.Node) *Problem { return nil }, // checked first, in Parse
	"default_server": func(p *Policy, v *yaml.Node) *Problem {
		s, pr := text(v, "default_server")
		if pr != nil {
			return pr
		}
		switch m := ServerMode(s); m {
		case ServerNone, ServerObserve, ServerControl:
			p.DefaultServer = &m
			return nil
		default:
			return problem(CodeOutOfRange, resolve(v).Line, "default_server", "must be none, observe or control")
		}
	},
	"max_active":  uintKey("max_active", 1, 16, "must be 1 to 16", func(p *Policy, u uint32) { p.MaxActive = &u }),
	"max_total":   uintKey("max_total", 1, 32, "must be 1 to 32", func(p *Policy, u uint32) { p.MaxTotal = &u }),
	"max_windows": uintKey("max_windows", 1, math.MaxUint32, "must be at least 1", func(p *Policy, u uint32) { p.MaxWindows = &u }),
	"allowed_harnesses": func(p *Policy, v *yaml.Node) *Problem {
		l, pr := stringList(v, "allowed_harnesses", harnessName.MatchString, "names are lowercase letters, digits and -")
		p.AllowedHarnesses = l
		return pr
	},
	"allowed_repo_roots": func(p *Policy, v *yaml.Node) *Problem {
		var pr *Problem
		p.AllowedRepoRoots, pr = repoRoots(v)
		return pr
	},
	"local_approval": func(p *Policy, v *yaml.Node) *Problem {
		var pr *Problem
		p.LocalApproval, pr = approvalRules(v)
		return pr
	},
	"rate_limits": func(p *Policy, v *yaml.Node) *Problem {
		var pr *Problem
		p.RateLimits, pr = rateLimits(v)
		return pr
	},
	"cert_lifetime": func(p *Policy, v *yaml.Node) *Problem {
		d, pr := duration(v, "cert_lifetime", 1, 365*day, "must be above zero", "must be up to 365 days (8760h)")
		p.CertLifetime = &d
		return pr
	},
	"attach_idle_timeout": durKey("attach_idle_timeout", 1, "must be above zero", func(p *Policy, d time.Duration) { p.AttachIdleTimeout = &d }),
	"idle_threshold":      durKey("idle_threshold", 1, "must be above zero", func(p *Policy, d time.Duration) { p.IdleThreshold = &d }),
	"min_probe_interval":  durKey("min_probe_interval", time.Minute, "must be at least 1m", func(p *Policy, d time.Duration) { p.MinProbeInterval = &d }),
	"spool_cap_bytes": func(p *Policy, v *yaml.Node) *Problem {
		u, pr := wholeNumber(v, "spool_cap_bytes", 0, math.MaxUint64, "")
		p.SpoolCapBytes = &u
		return pr
	},
	"block_on_battery":     boolKey("block_on_battery", func(p *Policy, b bool) { p.BlockOnBattery = &b }),
	"share_identity_email": boolKey("share_identity_email", func(p *Policy, b bool) { p.ShareIdentityEmail = &b }),
	"window_name_template": func(p *Policy, v *yaml.Node) *Problem {
		s, pr := text(v, "window_name_template")
		if pr != nil {
			return pr
		}
		if !printable(s, MaxFileBytes) {
			return problem(CodeBadValue, resolve(v).Line, "window_name_template", "no control characters")
		}
		p.WindowNameTemplate = &s
		return nil
	},
	"labels": func(p *Policy, v *yaml.Node) *Problem {
		var pr *Problem
		p.Labels, pr = stringList(v, "labels", labelName.MatchString, "each label is [a-z0-9][a-z0-9-]{0,31}")
		return pr
	},
	"metrics_listen": listenKey("metrics_listen", func(p *Policy, s string) { p.MetricsListen = &s }),
	"pprof_listen":   listenKey("pprof_listen", func(p *Policy, s string) { p.PprofListen = &s }),
	"telemetry": func(p *Policy, v *yaml.Node) *Problem {
		var pr *Problem
		p.Telemetry, pr = telemetryKey(v)
		return pr
	},
	"exec": func(p *Policy, v *yaml.Node) *Problem {
		return visit(v, "exec", execKeys, func(string, *yaml.Node) *Problem {
			p.ExecRawIgnored = true // known, never honoured (03e)
			return nil
		})
	},
}

var execKeys = map[string]struct{}{"raw": {}}

func uintKey(name string, lo, hi uint64, want string, set func(*Policy, uint32)) handler {
	return func(p *Policy, v *yaml.Node) *Problem {
		u, pr := wholeNumber(v, name, lo, hi, want)
		if pr == nil {
			set(p, uint32(u)) //nolint:gosec // G115: wholeNumber held u to at most MaxUint32
		}
		return pr
	}
}

func durKey(name string, lo time.Duration, want string, set func(*Policy, time.Duration)) handler {
	return func(p *Policy, v *yaml.Node) *Problem {
		d, pr := duration(v, name, lo, 0, want, "")
		if pr == nil {
			set(p, d)
		}
		return pr
	}
}

func boolKey(name string, set func(*Policy, bool)) handler {
	return func(p *Policy, v *yaml.Node) *Problem {
		b, pr := boolean(v, name)
		if pr == nil {
			set(p, b)
		}
		return pr
	}
}

func listenKey(name string, set func(*Policy, string)) handler {
	return func(p *Policy, v *yaml.Node) *Problem {
		s, pr := text(v, name)
		if pr != nil {
			return pr
		}
		if !loopbackAddr(s) {
			return problem(CodeBadValue, resolve(v).Line, name, "must be a loopback host:port")
		}
		set(p, s)
		return nil
	}
}

// repoRoots reads allowed_repo_roots: absolute paths of existing folders.
func repoRoots(v *yaml.Node) ([]string, *Problem) {
	const scope = "allowed_repo_roots"
	n := resolve(v)
	if n.Kind != yaml.SequenceNode {
		return nil, problem(CodeBadType, n.Line, scope, "must be a list")
	}
	out := make([]string, 0, len(n.Content))
	for _, item := range n.Content {
		s, pr := text(item, scope)
		if pr != nil {
			return nil, pr
		}
		line := resolve(item).Line
		if !filepath.IsAbs(s) || !printable(s, 4096) {
			return nil, problem(CodeBadValue, line, scope, "every entry must be an absolute path")
		}
		clean := filepath.Clean(s)
		if fi, err := os.Stat(clean); err != nil || !fi.IsDir() {
			return nil, problem(CodeBadValue, line, scope, "every entry must be an existing folder")
		}
		out = append(out, clean)
	}
	return out, nil
}
