package policy

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
)

// linkTarget follows policy.yaml when it is a symlink to a file in another
// folder: the watch on the config folder never sees a write to the target
// there (inotify reports it only to a watch on the target's folder), so the
// target's folder is watched too. Only the final target of the link is
// followed; a link in the middle of a chain that moves is seen at the next
// Snapshot or Settings re-hash. It is re-resolved before each look at the
// file, which is when the link may have been replaced, so the new target's
// folder is watched before the file is read and a write after the read always
// raises an event. No timer is involved.
type linkTarget struct {
	m *Manager
	w Watcher
	// extra is the extra folder being watched, "" when none.
	extra string
	// name is the path an event for the target file carries: the resolved
	// target for a file in another folder, the config folder joined with the
	// target's name for one inside it (events carry the folder as it was
	// added, not as it resolves). "" when policy.yaml is not a link.
	name string
}

// sync resolves the link and moves the extra watch to its target's folder.
// A link that cannot be resolved because the target (or the link) is missing
// right now, as when an editor deletes and recreates the target, changes
// nothing: the watch stays where it was until the target is back.
func (t *linkTarget) sync(ctx context.Context) {
	target, err := filepath.EvalSymlinks(t.m.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	extra, name := "", ""
	if err == nil {
		if dir, derr := filepath.EvalSymlinks(t.m.opts.Dir); derr == nil && filepath.Dir(target) != dir {
			extra, name = filepath.Dir(target), target
		} else if derr == nil {
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
		// Not rate limited: sync runs after a settled burst or a moved
		// folder, not per event. The file is still re-hashed at each Snapshot.
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
