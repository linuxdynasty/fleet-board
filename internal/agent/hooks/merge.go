package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
)

// MergeUserConfig merges the fleet entries for harness into the user
// config at path (claude, codex, cursor; Muse has no user file). A
// missing or blank file is created from RenderFresh. Otherwise the file
// is read, backed up to <file>.fleet-backup.<timestamp>, and rewritten
// atomically (temp file plus rename): only entries whose command points
// into the fleet hooks directory are added or updated, everything else
// keeps its bytes and its order. changed is false when the entries are
// already current and the file is left untouched.
func MergeUserConfig(path, harness, dataDir string) (changed bool, backup string, err error) {
	if !Supported(harness) || harness == HarnessMuse {
		return false, "", invalidArg("merge", path, fmt.Errorf("unsupported harness %q", harness))
	}
	scriptPath := ScriptPath(dataDir, harness)
	fresh, err := RenderFresh(harness, scriptPath)
	if err != nil {
		return false, "", invalidArg("merge", path, err)
	}
	raw, rerr := os.ReadFile(path) //nolint:gosec // G304: the user config path the caller named
	if rerr != nil {
		if !os.IsNotExist(rerr) {
			return false, "", installFailed("merge", path, rerr)
		}
		if err := writeAtomic(path, fresh, 0o600); err != nil {
			return false, "", installFailed("merge", path, err)
		}
		return true, "", nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if err := writeAtomic(path, fresh, 0o600); err != nil {
			return false, "", installFailed("merge", path, err)
		}
		return true, "", nil
	}
	merged, changed, merr := mergeBytes(raw, harness, scriptPath, HooksDir(dataDir, harness))
	if merr != nil {
		return false, "", installFailed("merge", path, merr)
	}
	if !changed {
		return false, "", nil
	}
	mode := os.FileMode(0o600)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	backup, berr := backupFile(path, raw)
	if berr != nil {
		return false, "", installFailed("backup", path, berr)
	}
	if werr := writeAtomic(path, merged, mode); werr != nil {
		return false, "", installFailed("merge", path, werr)
	}
	return true, backup, nil
}

// RemoveUserConfig removes only the fleet entries from the user config
// at path, keeping the backup and every other entry. A missing file or a
// file with no fleet entries is a no-op.
func RemoveUserConfig(path, harness, dataDir string) (bool, error) {
	if !Supported(harness) || harness == HarnessMuse {
		return false, invalidArg("uninstall", path, fmt.Errorf("unsupported harness %q", harness))
	}
	raw, rerr := os.ReadFile(path) //nolint:gosec // G304: the user config path the caller named
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, nil
		}
		return false, installFailed("uninstall", path, rerr)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	merged, changed, merr := removeBytes(raw, harness, HooksDir(dataDir, harness))
	if merr != nil {
		return false, installFailed("uninstall", path, merr)
	}
	if !changed {
		return false, nil
	}
	mode := os.FileMode(0o600)
	if fi, serr := os.Stat(path); serr == nil {
		mode = fi.Mode().Perm()
	}
	if _, berr := backupFile(path, raw); berr != nil {
		return false, installFailed("backup", path, berr)
	}
	if werr := writeAtomic(path, merged, mode); werr != nil {
		return false, installFailed("uninstall", path, werr)
	}
	return true, nil
}

// mergeBytes is the pure merge: parse with order and raw bytes preserved,
// replace or append the fleet entries, re-encode only what changed.
func mergeBytes(raw []byte, harness, scriptPath, hooksDir string) ([]byte, bool, error) {
	top, err := parseObject(raw)
	if err != nil {
		return nil, false, err
	}
	hi := top.index("hooks")
	hooks := &rawObject{raw: []byte("{}")}
	if hi >= 0 {
		hooks, err = parseObject(top.val[hi])
		if err != nil {
			return nil, false, fmt.Errorf("hooks section: %w", err)
		}
	}
	// Phase 1: append missing events. Appends insert before the closing
	// brace, so the recorded trail stays valid across events.
	hooksRaw := hooks.raw
	hadEvents := len(hooks.keys)
	nmembers := hadEvents
	for _, event := range Events(harness) {
		if hooks.index(event) >= 0 {
			continue
		}
		fresh := freshGroup(harness, event, scriptPath)
		hooksRaw = appendRawMember(hooksRaw, hooks.trail, nmembers == 0, event, []byte("["+string(fresh)+"]"))
		nmembers++
	}
	// Phase 2: replace stale fleet entries in the events that exist.
	hooks, err = parseObject(hooksRaw)
	if err != nil {
		return nil, false, err
	}
	hooksVals := append([][]byte(nil), hooks.val...)
	changed := len(hooks.keys) != hadEvents
	for _, event := range Events(harness) {
		ei := hooks.index(event)
		if ei < 0 {
			continue
		}
		arr, err := parseArray(hooks.val[ei])
		if err != nil {
			return nil, false, fmt.Errorf("event %s: %w", event, err)
		}
		fresh := freshGroup(harness, event, scriptPath)
		merged, same := mergeArray(arr, fresh, hooksDir)
		if !same {
			hooksVals[ei] = merged
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	hooksRaw = hooks.build(nil, hooksVals)
	topVals := append([][]byte(nil), top.val...)
	if hi < 0 {
		out := appendRawMember(top.raw, top.trail, len(top.keys) == 0, "hooks", hooksRaw)
		return withNewline(raw, out), true, nil
	}
	topVals[hi] = hooksRaw
	return withNewline(raw, top.build(nil, topVals)), true, nil
}

// appendRawMember inserts a member before the closing brace: only the new
// member uses canonical separators. empty reports an object with no
// members yet, so no separator comma is written.
func appendRawMember(raw, trail []byte, empty bool, key string, val []byte) []byte {
	kb, err := json.Marshal(key)
	if err != nil {
		panic(fmt.Sprintf("hooks: marshal key: %v", err))
	}
	cut := len(raw) - len(trail) - 1
	var b bytes.Buffer
	b.Write(raw[:cut])
	if !empty {
		b.WriteByte(',')
	}
	b.Write(kb)
	b.WriteByte(':')
	b.Write(val)
	b.Write(trail)
	b.WriteByte('}')
	return b.Bytes()
}

// withNewline keeps the file's trailing newline state.
func withNewline(raw, out []byte) []byte {
	if bytes.HasSuffix(raw, []byte("\n")) {
		return append(out, '\n')
	}
	return out
}

// mergeArray splices the fresh fleet element into the parsed array:
// the first fleet element is replaced, duplicates are dropped, and a
// missing one is appended. same reports no change.
func mergeArray(arr *rawArray, fresh []byte, hooksDir string) ([]byte, bool) {
	freshNested := freshNestedEntries(fresh)
	elems := arr.elems
	keep := make([]bool, len(elems))
	repl := make(map[int][]byte)
	replaced := false
	changed := false
	for i, el := range elems {
		if groupHasFleet(el, hooksDir) {
			if !hasCommand(el, hooksDir) {
				if out, ch, ok := spliceFleetNested(el, freshNested, hooksDir); ok {
					keep[i] = true
					if ch {
						repl[i] = out
						changed = true
					}
					replaced = true
					continue
				}
			}
			if !replaced {
				keep[i] = true
				if !sameJSON(el, fresh) {
					repl[i] = fresh
					changed = true
				}
				replaced = true
			} else {
				changed = true // drop duplicate fleet groups
			}
			continue
		}
		keep[i] = true
	}
	if !replaced {
		return arr.appendElem(fresh), false
	}
	if !changed {
		return arr.raw, true
	}
	out := make([][]byte, len(elems))
	for i, el := range elems {
		out[i] = el
		if r, ok := repl[i]; ok {
			out[i] = r
		}
	}
	return arr.build(keep, out), false
}

// freshNestedEntries parses the canonical fleet group's nested hook
// entries (claude/codex shape: {"matcher":..., "hooks":[...]}). Empty
// when fresh has no nested entries (cursor's flat shape).
func freshNestedEntries(fresh []byte) [][]byte {
	obj, err := parseObject(fresh)
	if err != nil {
		return nil
	}
	hi := obj.index("hooks")
	if hi < 0 {
		return nil
	}
	arr, err := parseArray(obj.val[hi])
	if err != nil {
		return nil
	}
	return append([][]byte(nil), arr.elems...)
}

// isFleetNestedEntry reports whether one nested hook entry points into
// the fleet hooks directory.
func isFleetNestedEntry(el []byte, hooksDir string) bool {
	var h struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(el, &h); err != nil {
		return false
	}
	return IsFleetCommand(h.Command, hooksDir)
}

// nestedHooks parses one group element down to its nested hooks
// array. False for flat entries and unparseable shapes.
func nestedHooks(el []byte) (obj *rawObject, hi int, arr *rawArray, ok bool) {
	obj, err := parseObject(el)
	if err != nil {
		return nil, -1, nil, false
	}
	if hi = obj.index("hooks"); hi < 0 {
		return nil, -1, nil, false
	}
	if arr, err = parseArray(obj.val[hi]); err != nil {
		return nil, -1, nil, false
	}
	return obj, hi, arr, true
}

// spliceFleetNested rewrites one mixed group element: fleet nested
// commands are replaced by fresh's (the first takes fresh's entry,
// extras are dropped) while every other nested entry keeps its bytes.
// ok is false when the element is not a nested group (flat cursor
// entries, unparseable shapes) or holds no user-owned entries: the
// caller falls back to whole-element handling.
func spliceFleetNested(el []byte, freshNested [][]byte, hooksDir string) (out []byte, changed, ok bool) {
	if len(freshNested) == 0 {
		return nil, false, false
	}
	obj, hi, arr, ok := nestedHooks(el)
	if !ok {
		return nil, false, false
	}
	keep := make([]bool, len(arr.elems))
	elems := append([][]byte(nil), arr.elems...)
	replaced := false
	seenUser := false
	for i, n := range arr.elems {
		if !isFleetNestedEntry(n, hooksDir) {
			keep[i] = true
			seenUser = true
			continue
		}
		if !replaced {
			keep[i] = true
			if !sameJSON(n, freshNested[0]) {
				elems[i] = freshNested[0]
				changed = true
			}
			replaced = true
			continue
		}
		changed = true // drop duplicate fleet entries
	}
	if !seenUser {
		return nil, false, false
	}
	if !changed {
		return el, false, true
	}
	vals := append([][]byte(nil), obj.val...)
	vals[hi] = arr.build(keep, elems)
	return obj.build(nil, vals), true, true
}

// dropFleetNested removes the fleet nested commands from a mixed
// group, keeping the rest byte-identical. False when nothing
// user-owned would remain or the element is not nested.
func dropFleetNested(el []byte, hooksDir string) (out []byte, changed, ok bool) {
	obj, hi, arr, ok := nestedHooks(el)
	if !ok {
		return nil, false, false
	}
	keep := make([]bool, len(arr.elems))
	kept := 0
	for i, n := range arr.elems {
		keep[i] = !isFleetNestedEntry(n, hooksDir)
		if keep[i] {
			kept++
		}
	}
	if kept == len(arr.elems) {
		return el, false, true
	}
	if kept == 0 {
		return nil, false, false
	}
	vals := append([][]byte(nil), obj.val...)
	vals[hi] = arr.build(keep, nil)
	return obj.build(nil, vals), true, true
}

// removeBytes drops every fleet entry; emptied events are dropped too.
// Other events keep their exact bytes.
func removeBytes(raw []byte, harness, hooksDir string) ([]byte, bool, error) {
	_ = harness // the match rule is the command, not the event
	top, err := parseObject(raw)
	if err != nil {
		return nil, false, err
	}
	hi := top.index("hooks")
	if hi < 0 {
		return raw, false, nil
	}
	hooks, err := parseObject(top.val[hi])
	if err != nil {
		return nil, false, fmt.Errorf("hooks section: %w", err)
	}
	keepHooks := make([]bool, len(hooks.keys))
	hooksVals := append([][]byte(nil), hooks.val...)
	changed := false
	for i := range hooks.keys {
		arr, err := parseArray(hooks.val[i])
		if err != nil {
			return nil, false, fmt.Errorf("event %s: %w", hooks.keys[i], err)
		}
		keep := make([]bool, len(arr.elems))
		outs := append([][]byte(nil), arr.elems...)
		kept := 0
		elemChanged := false
		for j, el := range arr.elems {
			if groupHasFleet(el, hooksDir) && !hasCommand(el, hooksDir) {
				if out, ch, ok := dropFleetNested(el, hooksDir); ok {
					keep[j] = true
					kept++
					if ch {
						outs[j] = out
						elemChanged = true
					}
					continue
				}
			}
			keep[j] = !groupHasFleet(el, hooksDir)
			if keep[j] {
				kept++
			}
		}
		switch {
		case kept == len(arr.elems) && !elemChanged:
			keepHooks[i] = true
		case kept == 0:
			changed = true // drop the emptied event
		default:
			keepHooks[i] = true
			hooksVals[i] = arr.build(keep, outs)
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	topVals := append([][]byte(nil), top.val...)
	topVals[hi] = hooks.build(keepHooks, hooksVals)
	return withNewline(raw, top.build(nil, topVals)), true, nil
}

// sameJSON reports whether two JSON values carry the same content. A
// fleet group written by a fresh install (indented) and by a merge
// (compact) must compare equal, so a second Ensure is a no-op.
func sameJSON(a, b []byte) bool {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// groupHasFleet reports whether an array element holds a fleet command.
func groupHasFleet(el []byte, hooksDir string) bool {
	if hasCommand(el, hooksDir) {
		return true
	}
	var g struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(el, &g); err != nil {
		return false
	}
	for _, h := range g.Hooks {
		if IsFleetCommand(h.Command, hooksDir) {
			return true
		}
	}
	return false
}

// hasCommand reports whether a cursor-style entry holds a fleet command.
func hasCommand(el []byte, hooksDir string) bool {
	var e struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(el, &e); err != nil || e.Command == "" {
		return false
	}
	return IsFleetCommand(e.Command, hooksDir)
}

// freshGroup is the canonical fleet element for one event.
func freshGroup(harness, event, scriptPath string) []byte {
	var v any
	switch harness {
	case HarnessCursor:
		e := cursorEntry{Command: Command(harness, scriptPath), Timeout: 10}
		if event == "stop" {
			e.LoopLimit = 5
		}
		v = e
	case HarnessCodex:
		v = codexGroup{Hooks: []hookCmd{fleetCmd(harness, scriptPath)}}
	default:
		v = claudeGroup{Matcher: "", Hooks: []hookCmd{fleetCmd(harness, scriptPath)}}
	}
	out, err := marshalJSON(v, "")
	if err != nil {
		panic(fmt.Sprintf("hooks: marshal fleet group: %v", err))
	}
	return bytes.TrimSuffix(out, []byte("\n"))
}
