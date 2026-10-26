package goalregistry

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// ByGoal returns the entry bound to goal, if any. The spool tailer (E8,
// row 21) resolves hook lines that carry a goal id but no usable pane or
// session; hook text never names a path, so a goal id is the only key.
func (r *Registry) ByGoal(goal string) (*agentv1.GoalRegistration, bool) {
	if goal == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.byPane {
		if entry.GetGoal().GetValue() == goal {
			return entry, true
		}
	}
	return nil, false
}

// ReleaseByPath drops every entry whose worktree sits at or under path
// (PruneWorktree for entries pointing into the worktree, 02h) and reports
// what it dropped, ordered by goal id. Each removal emits
// GoalRegistryChanged CLOSED; an empty match emits nothing. A sibling
// prefix never matches: only path itself or path plus a separator.
func (r *Registry) ReleaseByPath(path string) []*agentv1.GoalRegistration {
	clean := filepath.Clean(path)
	r.mu.Lock()
	var dropped []*agentv1.GoalRegistration
	for key, entry := range r.byPane {
		if !underPath(entry.GetWorktreePath(), clean) {
			continue
		}
		delete(r.byPane, key)
		dropped = append(dropped, entry)
	}
	r.mu.Unlock()
	sort.Slice(dropped, func(i, j int) bool {
		return dropped[i].GetGoal().GetValue() < dropped[j].GetGoal().GetValue()
	})
	for _, entry := range dropped {
		_ = r.changed(context.Background(), agentv1.ChangeKind_CHANGE_KIND_CLOSED, entry)
	}
	return dropped
}

// underPath reports whether wt is path itself or sits under it. Either
// side may be empty or uncleaned; the comparison cleans both.
func underPath(wt, path string) bool {
	w := filepath.Clean(wt)
	if w == path {
		return true
	}
	return strings.HasPrefix(w, path+string(filepath.Separator))
}
