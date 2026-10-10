package policy

import (
	"math"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	approvalKeys = map[string]struct{}{"command_kind": {}, "required": {}}
	rateKeys     = map[string]struct{}{"command_kind": {}, "per_minute": {}}
	otlpKeys     = map[string]struct{}{"endpoint": {}, "protocol": {}, "headers_env": {}, "insecure": {}}
	telemetryTop = map[string]struct{}{"otlp": {}, "signals": {}}
)

// items checks v is a list and calls fn for each entry (a mapping).
func items(v *yaml.Node, scope string, fn func(item *yaml.Node) *Problem) *Problem {
	n := resolve(v)
	if n.Kind != yaml.SequenceNode {
		return problem(CodeBadType, n.Line, scope, "must be a list")
	}
	for _, item := range n.Content {
		if p := fn(item); p != nil {
			return p
		}
	}
	return nil
}

// kindOf reads a command_kind and checks it names a command.
func kindOf(v *yaml.Node, scope string) (string, *Problem) {
	s, p := text(v, scope)
	if p != nil {
		return "", p
	}
	if !IsCommandKind(s) {
		return "", problem(CodeBadValue, resolve(v).Line, scope, "command_kind is not a command kind")
	}
	return s, nil
}

func approvalRules(v *yaml.Node) ([]ApprovalRule, *Problem) {
	const scope = "local_approval"
	out := []ApprovalRule{}
	seen := map[string]bool{}
	p := items(v, scope, func(item *yaml.Node) *Problem {
		var r ApprovalRule
		var have bool
		if p := visit(item, scope, approvalKeys, func(key string, val *yaml.Node) *Problem {
			var p *Problem
			if key == "command_kind" {
				r.CommandKind, p = kindOf(val, scope)
				have = p == nil
				return p
			}
			r.Required, p = boolean(val, scope)
			return p
		}); p != nil {
			return p
		}
		if !have {
			return problem(CodeBadValue, resolve(item).Line, scope, "command_kind is missing")
		}
		if seen[r.CommandKind] {
			return problem(CodeDuplicateKey, resolve(item).Line, scope, r.CommandKind+" is given twice")
		}
		seen[r.CommandKind] = true
		out = append(out, r)
		return nil
	})
	return out, p
}

func rateLimits(v *yaml.Node) ([]RateLimit, *Problem) {
	const scope = "rate_limits"
	out := []RateLimit{}
	seen := map[string]bool{}
	p := items(v, scope, func(item *yaml.Node) *Problem {
		var r RateLimit
		var haveKind, havePer bool
		var kindLine int
		if p := visit(item, scope, rateKeys, func(key string, val *yaml.Node) *Problem {
			if key == "command_kind" {
				var p *Problem
				r.CommandKind, p = kindOf(val, scope)
				haveKind, kindLine = p == nil, resolve(val).Line
				return p
			}
			u, p := wholeNumber(val, scope, 1, math.MaxUint32, "per_minute must be at least 1")
			r.PerMinute, havePer = uint32(u), p == nil //nolint:gosec // G115: wholeNumber held u to at most MaxUint32
			if p != nil && p.Code == CodeBadType {
				return problem(CodeBadType, p.Line, scope, "per_minute must be a whole number")
			}
			return p
		}); p != nil {
			return p
		}
		if !haveKind || !havePer {
			return problem(CodeBadValue, resolve(item).Line, scope, "each rule needs command_kind and per_minute")
		}
		if seen[r.CommandKind] {
			return problem(CodeDuplicateKey, kindLine, scope, r.CommandKind+" is given twice")
		}
		seen[r.CommandKind] = true
		out = append(out, r)
		return nil
	})
	return out, p
}

// telemetryKey reads the keys of 05b section 5.
func telemetryKey(v *yaml.Node) (*Telemetry, *Problem) {
	const scope = "telemetry"
	t := &Telemetry{}
	p := visit(v, scope, telemetryTop, func(key string, val *yaml.Node) *Problem {
		if key == "otlp" {
			return otlpKey(val, &t.OTLP)
		}
		l, p := stringList(val, "telemetry.signals", func(s string) bool {
			return s == "traces" || s == "metrics" || s == "logs"
		}, "each is traces, metrics or logs")
		if p != nil && p.Code == CodeBadValue {
			return problem(CodeOutOfRange, p.Line, "telemetry.signals", "each is traces, metrics or logs")
		}
		t.Signals = l
		return p
	})
	return t, p
}

func otlpKey(v *yaml.Node, o *OTLP) *Problem {
	const scope = "telemetry.otlp"
	insecureLine := 0
	if p := visit(v, scope, otlpKeys, func(key string, val *yaml.Node) *Problem {
		switch key {
		case "endpoint":
			s, p := text(val, scope+".endpoint")
			if p != nil {
				return p
			}
			if _, ok := endpoint(s); !ok {
				return problem(CodeBadValue, resolve(val).Line, scope+".endpoint", "must be an http or https URL")
			}
			o.Endpoint = s
		case "protocol":
			s, p := text(val, scope+".protocol")
			if p != nil {
				return p
			}
			if s != "grpc" && s != "http/protobuf" {
				return problem(CodeOutOfRange, resolve(val).Line, scope+".protocol", "must be grpc or http/protobuf")
			}
			o.Protocol = s
		case "headers_env":
			s, p := text(val, scope+".headers_env")
			if p != nil {
				return p
			}
			if !envName.MatchString(s) {
				return problem(CodeBadValue, resolve(val).Line, scope+".headers_env", "must be an environment variable name")
			}
			o.HeadersEnv = s
		default: // insecure
			b, p := boolean(val, scope+".insecure")
			o.Insecure, insecureLine = b, resolve(val).Line
			return p
		}
		return nil
	}); p != nil {
		return p
	}
	if o.Insecure && o.Endpoint != "" {
		if u, ok := endpoint(o.Endpoint); !ok || !isLoopbackHost(strings.ToLower(u.Hostname())) {
			return problem(CodeBadValue, insecureLine, scope+".insecure", "only for a loopback endpoint")
		}
	}
	return nil
}
