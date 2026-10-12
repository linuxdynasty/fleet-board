package settings

import (
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// On the wire an empty list cannot be told from an absent one, so a hub list
// that is empty is no opinion. In policy.yaml a nil list is no opinion and an
// empty one is an opinion (none).

// approvals is local_approval: hub OR local per kind (03e).
func (m *merger) approvals(hub []*typev1.LocalApprovalRule, local []policy.ApprovalRule) {
	if len(hub) == 0 && local == nil {
		return
	}
	req := map[string]bool{}
	for _, r := range hub {
		req[r.GetCommandKind()] = r.GetRequired()
	}
	capped := false
	for _, r := range local {
		h, ok := req[r.CommandKind]
		capped = capped || (ok && r.Required && !h)
		req[r.CommandKind] = h || r.Required
	}
	for _, k := range slices.Sorted(maps.Keys(req)) {
		m.eff.Values.LocalApproval = append(m.eff.Values.LocalApproval, &typev1.LocalApprovalRule{CommandKind: k, Required: req[k]})
	}
	m.listSource(fLocalApproval, len(hub) > 0, capped)
}

// rates is rate_limits: min per kind (03e). A kind no layer names keeps the
// executor's built-in limit.
func (m *merger) rates(hub []*typev1.RateLimit, local []policy.RateLimit) {
	if len(hub) == 0 && local == nil {
		return
	}
	per := map[string]uint32{}
	for _, r := range hub {
		per[r.GetCommandKind()] = r.GetPerMinute()
	}
	capped := false
	for _, r := range local {
		h, ok := per[r.CommandKind]
		if !ok || r.PerMinute < h {
			capped = capped || ok
			per[r.CommandKind] = r.PerMinute
		}
	}
	for _, k := range slices.Sorted(maps.Keys(per)) {
		m.eff.Values.RateLimits = append(m.eff.Values.RateLimits, &typev1.RateLimit{CommandKind: k, PerMinute: per[k]})
	}
	m.listSource(fRateLimits, len(hub) > 0, capped)
}

// harnesses is allowed_harnesses: hub ∩ local; with no opinion anywhere,
// the names the contract knows, with no source (ruling row 6 fork 6).
func (m *merger) harnesses(hub, local []string) {
	v := m.eff.GetValues()
	switch {
	case len(hub) == 0 && local == nil:
		v.AllowedHarnesses = knownHarnesses()
	case len(hub) == 0:
		v.AllowedHarnesses = slices.Clone(local)
		m.source(fAllowedHarnesses, localLayer)
	case local == nil:
		v.AllowedHarnesses = dedupe(hub)
		m.source(fAllowedHarnesses, hubLayer)
	default:
		m.intersect(fAllowedHarnesses, hub, local, &v.AllowedHarnesses)
	}
}

// roots is allowed_repo_roots: hub ∩ local, where a file with no opinion
// is empty (03e: empty until the owner sets it), so the hub alone never adds
// a folder. Paths match exactly after Clean, never as a sub-folder: a
// symlink inside a local root could otherwise lead out of it.
func (m *merger) roots(hub, local []string) {
	clean := make([]string, len(hub))
	for i, r := range hub {
		clean[i] = filepathClean(r)
	}
	switch {
	case len(hub) == 0 && len(local) == 0:
		m.eff.Values.AllowedRepoRoots = nil
	case len(hub) == 0:
		m.eff.Values.AllowedRepoRoots = slices.Clone(local)
		m.source(fAllowedRepoRoots, localLayer)
	default:
		m.intersect(fAllowedRepoRoots, clean, local, &m.eff.Values.AllowedRepoRoots)
	}
}

// intersect keeps the hub's entries the local list also names, in the hub's
// order; it is capped when that drops any.
func (m *merger) intersect(field string, hub, local []string, out *[]string) {
	hub = dedupe(hub)
	var keep []string
	for _, h := range hub {
		if slices.Contains(local, h) {
			keep = append(keep, h)
		}
	}
	*out = keep
	if len(keep) < len(hub) {
		m.source(field, localLayer)
		m.cap(field, "", "")
		return
	}
	m.source(field, hubLayer)
}

func (m *merger) listSource(field string, hub, capped bool) {
	switch {
	case capped:
		m.source(field, localLayer)
		m.cap(field, "", "")
	case hub:
		m.source(field, hubLayer)
	default:
		m.source(field, localLayer)
	}
}

// probes floors the hub's probe intervals at the local minimum: a local value
// can only make probing rarer (03e "Probe interval"). capped_fields names
// probe_interval when the floor won for the default or any account.
func (m *merger) probes(hub *durationpb.Duration, byAccount map[string]*durationpb.Duration, floor *time.Duration) {
	longer := func(a, b time.Duration) bool { return a > b }
	if hub != nil {
		tighten(m, fProbeInterval, dur(hub), floor, nil, longer,
			func(d time.Duration) { m.eff.Values.ProbeInterval = durationpb.New(d) }, time.Duration.String)
	}
	var hubShort, floorStr string
	for _, id := range sortedKeys(byAccount) {
		d := byAccount[id].AsDuration()
		if floor != nil && *floor > d {
			if hubShort == "" {
				hubShort, floorStr = d.String(), floor.String()
			}
			d = *floor
		}
		if m.eff.ProbeIntervalByAccount == nil {
			m.eff.ProbeIntervalByAccount = map[string]*durationpb.Duration{}
		}
		m.eff.ProbeIntervalByAccount[id] = durationpb.New(d)
	}
	if hubShort != "" && !slices.ContainsFunc(m.capped, func(c Capped) bool { return c.Field == fProbeInterval }) {
		m.cap(fProbeInterval, hubShort, floorStr)
	}
}

// labels is hub ∪ local, sorted; the file cannot remove a hub label.
func (m *merger) labels(hub, local []string) {
	out := dedupe(append(slices.Clone(hub), local...))
	slices.Sort(out)
	m.eff.Labels = out
}

func dedupe(list []string) []string {
	var out []string
	for _, s := range list {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
