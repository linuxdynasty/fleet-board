package policy

import (
	"context"
	"path/filepath"
)

// linkTarget follows policy.yaml when it is a symlink to a file in another
// folder: the watch on the config folder never sees a write to the target
// there (inotify reports it only to a watch on the target's folder), so the
// target's folder is watched too. Only the final target of the link is
// followed; a link in the middle of a chain that moves is seen at the next
// Snapshot or Settings re-hash. It is re-resolved after each look at the file,
// which is when the link may have been replaced. No timer is involved.
type linkTarget struct {
	m *Manager
	w Watcher
	// extra is the extra folder being watched, "" when none.
	extra string
	// name is the path an event for the target file carries, "" when the
	// file is not a link.
	name string
}

// sync resolves the link and moves the extra watch to its target's folder.
func (t *linkTarget) sync(ctx context.Context) {
	extra, name := "", ""
	if target, err := filepath.EvalSymlinks(t.m.path); err == nil {
		if dir, err := filepath.EvalSymlinks(t.m.opts.Dir); err == nil && filepath.Dir(target) != dir {
			extra, name = filepath.Dir(target), target
		} else if err == nil {
			// The target is in the config folder, which is already watched;
			// its events carry the folder as watched, not as resolved.
			name = filepath.Join(t.m.opts.Dir, filepath.Base(target))
		}
	}
	t.name = name
	if extra == t.extra {
		return
	}
	if t.extra != "" {
		_ = t.w.Remove(t.extra)
	}
	t.extra = ""
	if extra == "" {
		return
	}
	if err := t.w.Add(extra); err != nil {
		// The file is still re-hashed at each Snapshot; say so once a minute
		// like any other watch error.
		t.m.log.WarnContext(ctx, "policy.watch_error", "err_kind", watchErrKind(err), "suppressed", 0)
		return
	}
	t.extra = extra
}

// names reports whether an event name is the policy file or its link target.
func (t *linkTarget) names(event string) bool {
	n := filepath.Clean(event)
	return n == t.m.path || (t.name != "" && n == t.name)
}
