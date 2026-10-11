package exec

import "github.com/linuxdynasty/fleet-board/internal/agent/policy"

// localPolicy reads the policy file in force.
type localPolicy struct{ m *policy.Manager }

// FromPolicy is the [Policy] of policy.yaml alone. With the hub's settings
// merged in, tighten only, the Policy is settings.Manager (plan row 6i
// wires it).
func FromPolicy(m *policy.Manager) Policy { return localPolicy{m: m} }

func (p localPolicy) SafeMode() bool { return p.m.SafeMode() }

func (p localPolicy) RateLimit(kind string) (int, bool) {
	for _, r := range p.m.Policy().RateLimits {
		if r.CommandKind == kind {
			return int(r.PerMinute), true
		}
	}
	return 0, false
}

func (p localPolicy) ApprovalRequired(kind string) bool {
	for _, r := range p.m.Policy().LocalApproval {
		if r.CommandKind == kind {
			return r.Required
		}
	}
	return false
}
