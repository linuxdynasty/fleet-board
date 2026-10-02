// Session view keyboard while the terminal has focus: tmux default bindings emulated behind the configured prefix
// (settings.tmuxPrefix). The real build forwards every key as TermDown.input and tmux applies the developer's own
// bindings; this table exists only so the mock can react. Rendering is in view-session-tmux.js.
const HELP = [["n / p", "next / previous window"], ["0–9", "window by index"], ["o", "next pane"], ["← ↑ → ↓", "pane in that direction"], ["z", "zoom the active pane (Z on the tab)"],
  ["w", "choose a window"], ["s", "choose a session on this machine"], ["d", "detach"], ["?", "this list"], ["Esc", "cancel the prefix"]];
const ARROW_BYTES = { ArrowUp: "\x1b[A", ArrowDown: "\x1b[B", ArrowRight: "\x1b[C", ArrowLeft: "\x1b[D" };
const MODIFIERS = ["Shift", "Control", "Alt", "Meta", "CapsLock"];
const isPrefixKey = e => e.ctrlKey && !e.metaKey && !e.altKey && e.key.toLowerCase() === (settings.tmuxPrefix === "C-a" ? "a" : "b");
const prefixBytes = () => settings.tmuxPrefix === "C-a" ? "\x01" : "\x02";
const repr = b => `"${b.replace(/[\x00-\x1f\x7f"\\]/g, c => c === '"' || c === "\\" ? "\\" + c : "\\x" + c.charCodeAt(0).toString(16).padStart(2, "0"))}"`;

function tmuxKey(e, s) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") return;   // ⌘K stays the board's switcher
  if (e.key === "Tab" || MODIFIERS.includes(e.key)) return;              // keyboard navigation and bare modifiers pass through
  e.stopPropagation();                                                    // nothing below reaches the board's global keys
  if (T.overlay) return overlayKey(e, s);
  if (!attached(s)) { if (e.key === "Enter") { e.preventDefault(); T.detached = null; rerender(); } return; }
  if (isPrefixKey(e)) { e.preventDefault(); T.prefix = true; T.msg = null; termDown(s, `input: ${repr(prefixBytes())}`); rerender(); return; }
  if (T.prefix) { e.preventDefault(); T.prefix = false; prefixKey(e.key, s); rerender(); return; }
  if (e.key === "Escape") { T.focused = false; $("tmuxTerm").blur(); rerender(); return; }
  if (e.metaKey || e.altKey) return;
  let bytes = null;
  if (e.ctrlKey && /^[a-z]$/i.test(e.key)) bytes = String.fromCharCode(e.key.toLowerCase().charCodeAt(0) - 96);
  else if (e.key === "Enter") bytes = "\r";
  else if (e.key === "Backspace") bytes = "\x7f";
  else if (ARROW_BYTES[e.key]) bytes = ARROW_BYTES[e.key];
  else if (e.key.length === 1 && !e.ctrlKey) bytes = e.key;
  if (bytes === null) return;
  e.preventDefault(); typeInto(s, bytes); rerender();
}

// prefix + key: the tmux defaults the mock understands. Anything else is reported on the status line like tmux does.
function prefixKey(k, s) {
  const wins = winsOf(s), i = wins.findIndex(w => w.id === s.id), panes = panesOf(s), ai = activeIdx(s);
  if (k === "Escape") return;
  termDown(s, `input: ${repr(prefixBytes() + (ARROW_BYTES[k] ?? (k.length === 1 ? k : "")))}`);
  if (k === "n" || k === "p") return selectWindow(s, wins[(i + (k === "n" ? 1 : wins.length - 1)) % wins.length].id);
  if (/^[0-9]$/.test(k)) { const w = wins.find(x => x.idx === +k); if (w) return selectWindow(s, w.id); T.msg = `can't find window: ${k}`; return; }
  if (k === "o") { T.active[s.id] = (ai + 1) % panes.length; return; }
  if (ARROW_BYTES[k]) { const to = (PANE_DIRS[layoutOf(s)][ai] ?? {})[k]; if (to !== undefined) T.active[s.id] = to; return; }
  if (k === "z") { if (panes.length > 1) T.zoom[s.id] = !T.zoom[s.id]; else T.msg = "only one pane"; return; }
  if (k === "w") { T.overlay = "windows"; T.ovIdx = Math.max(0, i); return; }
  if (k === "s") { T.overlay = "sessions"; T.ovIdx = Math.max(0, sessNames(s).indexOf(s.sess)); return; }
  if (k === "d") { T.detached = sessKey(s); T.msg = null; log(`detached the browser terminal from ${s.sess} on ${mById(s.m).short} (mock)`, s); return; }
  if (k === "?") { T.overlay = "help"; return; }
  T.msg = `unknown key: ${k}`;
}

function overlayKey(e, s) {
  const items = overlayItems(s), k = e.key;
  e.preventDefault();
  if (k === "Escape" || k === "q") T.overlay = null;
  else if (T.overlay === "help") return;
  else if (k === "ArrowDown" || k === "j") T.ovIdx = Math.min(T.ovIdx + 1, items.length - 1);
  else if (k === "ArrowUp" || k === "k") T.ovIdx = Math.max(T.ovIdx - 1, 0);
  else if (k === "Enter" && items[T.ovIdx]) { T.overlay = null; return selectWindow(s, items[T.ovIdx].id); }
  else return;
  rerender();
}

// Plain typing goes to the active pane. The mock echoes it on the pane's input line; the real build only forwards bytes.
function typeInto(s, bytes) {
  const p = panesOf(s)[activeIdx(s)], k = p.paneId;
  termDown(s, `input: ${repr(bytes)}`);
  T.msg = null;
  if (T.ro) { T.msg = "read-only: input dropped"; return; }
  const inp = T.input[k] ?? "";
  if (bytes === "\x7f") { T.input[k] = inp.slice(0, -1); return; }
  if (bytes === "\r") {
    const pi = promptIdx(p.pane), [cls, text] = p.pane[pi];
    p.pane.splice(pi, 1, [cls, text + inp], ["dim", inp ? `(mock: ${JSON.stringify(inp)} sent to the pane; the reply would appear here)` : "(mock: Enter sent)"], [cls, text]);
    while (p.pane.length > 16) p.pane.shift();
    T.input[k] = ""; s.last = new Date();
    return;
  }
  if (bytes.length === 1 && bytes >= " ") T.input[k] = inp + bytes;
  else T.msg = `sent ${repr(bytes)}`;
}
