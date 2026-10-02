// Session view terminal: the selected tmux session drawn like the developer's own client (J1 Terminal stream).
// Status line ↔ TmuxWindow{index, name, active}; pane boxes ↔ TmuxWindow.panes[] (TmuxPane{index, current_command});
// screen text ↔ CapturePaneResult.text / TermUp.output. Terminal state lives in T; keys are in view-session-keys.js.
const T = { focused: false, prefix: false, zoom: {}, overlay: null, ovIdx: 0, detached: null, ro: false, msg: null,
  drawer: false, side: true, lastDown: null, active: {}, input: {}, cur: {}, rendering: false };
// Pane neighbours per layout for prefix + arrow (tmux select-pane -LRUD). Pane 0 is the window's main pane.
const PANE_DIRS = { single: {}, cols: { 0: { ArrowRight: 1 }, 1: { ArrowLeft: 0 } }, rows: { 0: { ArrowDown: 1 }, 1: { ArrowUp: 0 } },
  "main-vertical": { 0: { ArrowRight: 1 }, 1: { ArrowLeft: 0, ArrowDown: 2 }, 2: { ArrowLeft: 0, ArrowUp: 1 } } };
const attachId = s => `att-${s.m}-${s.sess}`;
const sessKey = s => `${s.m}/${s.sess}`;
const attached = s => T.detached !== sessKey(s);   // prefix d detaches one session; any other session is still attached
const viewSess = s => `${s.sess}-view-1`;
const winsOf = s => sessions.filter(x => x.m === s.m && x.sess === s.sess).sort((a, b) => a.idx - b.idx);
const sessNames = s => [...new Set(sessions.filter(x => x.m === s.m).map(x => x.sess))].sort((a, b) => (a === "fleet") - (b === "fleet") || a.localeCompare(b));
const panesOf = s => [{ cmd: s.harness === "shell" ? s.win : s.harness, paneId: s.paneId, pane: s.pane }, ...(s.panes ?? [])];
const activeIdx = s => Math.min(T.active[s.id] ?? 0, panesOf(s).length - 1);
const layoutOf = s => s.panes?.length ? s.layout ?? "cols" : "single";
const zoomed = s => !!T.zoom[s.id] && panesOf(s).length > 1;
const runningGoal = s => isGoal(s) && !["merged", "none"].includes(goalState(s)) && !s.stopped;
const termDown = (s, body) => { T.lastDown = `TermDown{attach_id: ${attachId(s)}, ${body}}`; };
const tmClock = () => { const d = new Date(); return `${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} ${d.toLocaleDateString("en-GB", { day: "2-digit", month: "short", year: "2-digit" }).replace(/ /g, "-")}`; };
setInterval(() => { const el = $("tmuxClock"); if (el) el.textContent = tmClock(); }, 1000);

function renderTmux(s, m) {
  const wins = winsOf(s), panes = panesOf(s), ai = activeIdx(s), z = zoomed(s);
  T.cur[sessKey(s)] = s.id;
  if (!attached(s)) return detachedPanel(s, wins);
  const status = `<div class="tm-status" role="tablist" aria-label="Windows in ${esc(s.sess)}"><span class="tm-left">[${esc(s.sess)}]</span>
    ${wins.map(w => `<button class="tm-win ${w.id === s.id ? "cur" : ""}" role="tab" aria-selected="${w.id === s.id}" data-tab="${w.id}" title="TermDown.select{window_id: @${w.idx}}"><span class="num">${w.idx}</span> ${esc(w.win)}${w.id === s.id && z ? "<b>Z</b>" : ""}</button>`).join("")}
    <span class="tm-right">${T.msg ? `<span class="tm-msg">${esc(T.msg)}</span>` : ""}${T.prefix ? `<span class="tm-prefix">PREFIX</span>` : ""}${T.ro ? `<span class="tm-flag">read-only</span>` : ""}
      <span class="tm-view" title="grouped view session (OpenAttachResult.view_session_name)">${esc(viewSess(s))}</span><span class="tm-host">"${esc(m.short)}"</span><span id="tmuxClock" class="num">${tmClock()}</span></span></div>`;
  const shown = z ? [[panes[ai], ai]] : panes.map((p, i) => [p, i]);
  return `<div class="tm-wrap">${runningGoal(s) ? goalBanner(s) : ""}
    <div id="tmuxTerm" class="tmux ${T.focused ? "focused" : ""}" tabindex="0" role="application" aria-label="tmux session ${esc(s.sess)} on ${esc(m.short)}; click to focus, ${esc(settings.tmuxPrefix)} is the prefix">
      ${status}<div class="tm-panes ${z ? "single" : layoutOf(s)}">${shown.map(([p, i]) => paneHtml(p, i, i === ai)).join("")}</div>${T.overlay ? overlayHtml(s) : ""}</div>
    <div class="tm-caption"><span>Click the terminal to focus it; then every key is typed into the active pane and <span class="mono">${esc(settings.tmuxPrefix)}</span> arms the tmux prefix (<span class="mono">${esc(settings.tmuxPrefix)} ?</span> lists the keys). In the real build the keys go straight to tmux over the Terminal stream as <span class="mono">TermDown.input</span>, so your own prefix and bindings apply; a click on a window tab sends <span class="mono">TermDown.select{window_id}</span> (contract addition). Esc leaves the terminal.</span>
      <span class="mono small">${esc(attachCmd(s, T.ro))} → ${esc(viewSess(s))}${T.lastDown ? ` · last sent: ${esc(T.lastDown)}` : ""}</span></div></div>`;
}

const promptIdx = lines => { for (let i = lines.length - 1; i >= 0; i--) if (/(^>\s?$)|([%$#]\s?$)/.test(lines[i][1])) return i; return lines.length - 1; };
function paneHtml(p, i, active) {
  const inp = T.input[p.paneId] ?? "", pi = promptIdx(p.pane);
  const lines = p.pane.map(([c, t], j) => { const body = esc(t) + (j === pi ? esc(inp) + (active ? `<span class="cursor">█</span>` : "") : ""); return c ? `<span class="${c}">${body}</span>` : body; });
  return `<div class="tm-pane ${active ? "active" : ""} p${i}" data-pane="${i}"><div class="tm-title">${i}: ${esc(p.cmd)}${active ? " *" : ""} <span class="dim">${esc(p.paneId)}</span></div><pre class="tm-screen">${lines.join("\n")}</pre></div>`;
}

// Safety: typing into a running goal is allowed for the developer's own attach, but said out loud; read-only drops input.
function goalBanner(s) {
  const perm = s.perm && !ui.answered[s.perm] ? pById(s.perm) : null;
  return `<div class="tm-goal ${T.ro ? "ro" : ""}"><b>${T.ro ? "Read-only" : "You're typing into a running goal"} (${esc(s.display ?? s.win)})</b>
    <span class="why">${T.ro ? "input is dropped on the hub and the agent (OpenAttach.read_only); switching windows and panes still works" : "keystrokes go to the harness, not the board; the goal file keeps the goal (ADR 0004)"}</span>
    <button class="btn sm ${T.ro ? "on" : ""}" data-act="ro">${T.ro ? "Read-write" : "Read-only"}</button>
    ${perm ? `<button class="btn sm" data-tri="${perm.id}">Destructive prompt ${esc(perm.promptId)} → Triage</button>` : `<span class="why">destructive prompts in this pane are answered in Triage, never by typing</span>`}</div>`;
}

function overlayItems(s) {
  if (T.overlay === "windows") return winsOf(s).map(w => ({ id: w.id, label: `(${w.idx}) ${s.sess}:${w.idx} ${w.win}${w.id === s.id ? "*" : ""}`, sub: `${panesOf(w).length} pane${panesOf(w).length === 1 ? "" : "s"} · ${w.harness}` }));
  if (T.overlay === "sessions") return sessNames(s).map(n => {
    const ws = sessions.filter(x => x.m === s.m && x.sess === n).sort((a, b) => a.idx - b.idx), cur = sById(T.cur[`${s.m}/${n}`]) ?? ws[0];
    return { id: cur.id, label: `${n}: ${ws.length} window${ws.length === 1 ? "" : "s"}${n === s.sess ? " (attached)" : ""}`, sub: ws.some(x => !x.yours) ? "started by fleet" : "yours" }; });
  return [];
}
function overlayHtml(s) {
  if (T.overlay === "help") return `<div class="tm-ov" role="dialog" aria-label="tmux keys"><div class="tm-ov-h">${esc(settings.tmuxPrefix)} then…</div>
    <table class="tm-keys">${HELP.map(([k, d]) => `<tr><td class="mono">${esc(k)}</td><td>${esc(d)}</td></tr>`).join("")}</table>
    <div class="dim">Esc or q closes · emulated tmux defaults; the real build sends every key to tmux, so your own bindings apply</div></div>`;
  const items = overlayItems(s);
  return `<div class="tm-ov" role="listbox" aria-label="${T.overlay === "windows" ? "Choose a window" : "Choose a session"}">
    ${items.map((it, i) => `<button class="tm-ov-i" role="option" aria-selected="${i === T.ovIdx}" data-ov="${it.id}">${esc(it.label)} <span class="dim">${esc(it.sub)}</span></button>`).join("")}
    <div class="dim">↑↓ or j k move · Enter choose · Esc close</div></div>`;
}

function detachedPanel(s, wins) {
  return `<div class="tm-wrap"><div id="tmuxTerm" class="tmux detached" tabindex="0" role="application" aria-label="detached"><div class="tm-det"><div>[detached (from session ${esc(s.sess)})]</div>
    <div class="dim">The grouped view session ${esc(viewSess(s))} is gone (destroy-unattached). Enter, Attach, or a window below opens a new OpenAttach.</div>
    <div class="tm-det-list">${wins.map(w => `<button class="btn sm" data-tab="${w.id}"><span class="num">${w.idx}</span> ${esc(w.win)}</button>`).join("")}</div>
    <div><button class="btn primary" data-act="reattach">Attach</button></div></div></div>
    <div class="tm-caption"><span class="mono small">CloseAttach{attach_id: ${esc(attachId(s))}} sent · the sidebar still switches windows and machines</span></div></div>`;
}

function selectWindow(s, id, click) {
  const w = sById(id);
  if (!w) return;
  if (click) termDown(s, `select: {window_id: @${w.idx}}`);
  T.detached = null; T.focused = true; T.overlay = null;
  go("session", id);
}

function wireTmux(s) {
  const t = $("tmuxTerm");
  if (!t) return;
  t.addEventListener("keydown", e => tmuxKey(e, s));
  t.addEventListener("focus", () => { T.focused = true; t.classList.add("focused"); });
  t.addEventListener("blur", () => { if (T.rendering) return; T.focused = false; T.prefix = false; t.classList.remove("focused"); });
  t.querySelectorAll("[data-tab]").forEach(b => b.onclick = e => { e.stopPropagation(); selectWindow(s, b.dataset.tab, true); });
  t.querySelectorAll("[data-pane]").forEach(p => p.onclick = () => { T.active[s.id] = +p.dataset.pane; T.focused = true; rerender(); });
  t.querySelectorAll("[data-ov]").forEach(b => b.onclick = e => { e.stopPropagation(); selectWindow(s, b.dataset.ov); });
  if (T.focused) t.focus({ preventScroll: true });
}
