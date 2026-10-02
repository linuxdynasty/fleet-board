// Session: the sidebar lists every tmux window on every machine (names exactly as tmux shows them); the main area IS
// the selected tmux session (view-session-tmux.js, keys in view-session-keys.js); the Details drawer on the right holds
// goal controls, tasks, change request, harness palette, history, session controls (view-session-panels.js).
const sideTag = s => s.yours ? (s.available ? `<span class="tag ok">available</span>` : s.idle ? `<span class="tag">idle</span>` : "") : "";

function renderSessionView() {
  const order = sessionOrder();
  if (!sById(ui.selected)) ui.selected = order[0].id;
  const s = sById(ui.selected), m = mById(s.m), st = liveState(s), pos = order.indexOf(s) + 1;
  const perm = s.perm && !ui.answered[s.perm] ? pById(s.perm) : null, cond = activeCond(s);
  const drawer = T.drawer || ui.confirm?.sid === s.id || !!ui.newOpen || ui.moveOpen;
  T.rendering = true;
  $("view").innerHTML = `<div class="sess ${T.side ? "" : "side-off"} ${drawer ? "drawer-on" : ""}">
    ${T.side ? sidebar(order, s) : `<nav class="sess-side mini" aria-label="All tmux windows (collapsed)"><button class="btn sm" data-act="side" aria-label="Show the session list" title="Show sessions">»</button></nav>`}
    <div class="sess-main">
      <div class="sess-head">${agentChip(s.harness)}<h3>${esc(s.win)}</h3>${provChip(s.provider)}<span class="pill ${STATE_PILL[st]}">${st === "none" ? (s.yours ? "yours" : "not a goal") : st}</span>${s.sample ? `<span class="samp">sample</span>` : ""}
        <span class="why">${esc(m.short)} · <span class="mono">${esc(s.target)}</span></span>
        <span class="spacer"></span><button class="btn sm" data-step="-1" aria-label="Previous window">[</button><span class="num why">${pos} of ${order.length}</span><button class="btn sm" data-step="1" aria-label="Next window">]</button>
        <button class="btn sm ${drawer ? "on" : ""}" data-act="drawer" aria-expanded="${drawer}">Details <kbd>i</kbd></button></div>
      ${cond ? `<div class="sess-q cond"><b>${esc(cond.kind)}${cond.retrying ? ", retrying" : ""}:</b> ${esc(cond.text)} <button class="btn sm" data-tri="${cond.id}">See in Triage</button></div>` : ""}
      ${perm ? `<div class="sess-q"><b>Permission prompt ${esc(perm.promptId)}:</b> ${esc(perm.text)} <span class="why">default ${esc(perm.def)}${perm.destructive ? " · destructive: " + esc(perm.reason) : ""}</span> <button class="btn sm" data-tri="${perm.id}">Answer in Triage</button></div>` : ""}
      ${renderTmux(s, m)}
    </div>
    ${drawer ? drawerHtml(s, m) : ""}</div>`;
  wireSession(s, m);
  T.rendering = false;
}

function sidebar(order, s) {
  return `<nav class="sess-side" aria-label="All tmux windows"><div class="side-top"><span class="why">tmux windows</span><button class="btn sm" data-act="side" aria-label="Hide the session list" title="Hide sessions">«</button></div>${machines.map(mm => {
    const ss = sessions.filter(x => x.m === mm.id), names = [...new Set(order.filter(x => x.m === mm.id).map(x => x.sess))];
    return `<div class="side-group"><div class="side-m"><span>${esc(mm.short)}</span><span class="num">${activeOn(mm)}/${mm.maxActive} · ${totalOn(mm)}/${mm.maxTotal}${mm.drained ? " · draining" : ""}</span></div>
      ${names.map(n => { const fl = ss.some(x => x.sess === n && !x.yours); return `<div class="side-sess"><span class="mono">${esc(n)}</span><span class="tag ${fl ? "fleet" : ""}">${fl ? "started by fleet" : "yours"}</span></div>
        ${ss.filter(x => x.sess === n).sort((a, b) => a.idx - b.idx).map(x => `<button class="side-s" data-sid="${x.id}" aria-current="${x.id === s.id}"><span class="sdot ${liveState(x)}"></span>
          <span class="side-a num">${x.idx}</span><span class="side-r" style="color:${harnessColor[x.harness]}">${esc(x.win)}</span><span class="side-t">${sideTag(x)}</span></button>`).join("")}`; }).join("")}
      <button class="btn sm ghost" data-new="${mm.id}">+ New session…</button></div>`;
  }).join("")}<p class="why">Window names as the tmux status bar shows them. With the terminal unfocused: [ and ] step through · n jumps to the next one that needs you · i opens Details</p></nav>`;
}

// Everything that used to sit in the right rail, plus what the attach panel explained.
function drawerHtml(s, m) {
  const pr = prs.find(p => p.n === s.pr && p.repo === s.repo), done = s.tasks.filter(t => t[1]).length, acct = s.acct ? apiAccounts.find(a => a.id === s.acct) : acctFor(s.harness);
  return `<aside class="sess-rail" aria-label="Details">
    <div class="rail-top"><h2>Details</h2><button class="btn sm" data-act="drawer" aria-label="Close details">×</button></div>
    ${ui.newOpen ? newSessionPanel(mById(ui.newOpen)) : ""}
    ${ui.confirm?.sid === s.id ? confirmBoxS() : ""}
    ${ui.moveOpen ? movePanel(s) : ""}
    <section><h2>Window</h2><div class="sess-meta"><span>${esc(m.name)} → <span class="mono">${esc(s.target)}</span> · pane <span class="mono">${esc(s.paneId)}</span>${s.panes?.length ? ` + ${s.panes.length} more` : ""}</span>${s.branch ? `<span class="mono">${esc(s.branch)}</span>` : ""}
      <span>started ${hhmm(s.started)} · last output <span data-t="${+s.last}">${rel(s.last)}</span></span>${s.yours ? `<span>${s.idle ? `idle · ` : ""}${s.available ? "available for work" : s.harness === "shell" ? "" : "not available for work"}</span>` : ""}
      ${s.model ? `<span>model line: <span class="mono">${esc(s.model)}</span> → spending from <b>${esc(acct?.label ?? "?")}</b>${acct?.capUsd ? ` ($${acct.spentUsd.toFixed(2)} of $${acct.capUsd} cap · manual only)` : ""}</span>` : ""}</div>
      ${s.line ? `<div class="goalfile"><span class="why">goal file, first line</span><code>${esc(s.line)}</code><span class="why">CONSENT: ${esc(consentFor(acct).join(", "))}</span><span class="why">${esc(GOAL_SYNTAX[s.harness])}</span></div>` : ""}</section>
    ${isGoal(s) ? `<section><h2>Goal progress <span class="num why">${done}/${s.tasks.length}</span></h2>
      ${s.tasks.length ? `<ol class="tasks">${s.tasks.map((t, i) => `<li class="${t[1] ? "done" : i === done ? "cur" : ""}">${esc(t[0])}</li>`).join("")}</ol>` : `<p class="why">No task list reported.</p>`}</section>` : ""}
    ${pr ? `<section><h2>Change request</h2><div class="mono ref">${esc(pr.display)} ${provChip(pr.provider)} · ${esc(pr.state)}</div><div class="card-t">${esc(pr.t)}</div>
      <div>${pr.ci.fail ? `<span class="ci bad">✗ ${pr.ci.fail} failing</span>` : ""} <span class="ci ok">✓ ${pr.ci.pass}</span> ${pr.ci.pend ? `<span class="ci warn">● ${pr.ci.pend}</span>` : ""}</div></section>` : ""}
    ${s.harness !== "shell" ? goalControls(s) : ""}
    <section><h2>Send keys</h2><div class="keys"><input id="keysIn" class="field" placeholder="Keys for the main pane" aria-label="Keys to send"><button class="btn" data-act="keys">Send…</button></div>
      <div class="why">Typing in the terminal goes over TermDown.input unconfirmed; this path is SendKeys, confirmed for goal panes (ADR 0004).</div></section>
    ${s.harness !== "shell" ? `<section><h2>Harness command</h2><button class="btn ${ui.paletteOpen ? "on" : ""}" data-act="palette">${ui.paletteOpen ? "Hide the palette" : "Harness command…"}</button>${ui.paletteOpen ? palettePanel(s) : ""}</section>` : ""}
    <section><h2>Session</h2><div class="ctl">
      ${s.yours && s.harness !== "shell" ? `<button class="btn" data-act="avail">${s.available ? "Withdraw “available for work”" : "Mark available for work"}</button>` : ""}
      ${!s.yours && isGoal(s) ? `<span class="why">Becomes available for work after a verified merge (E11)${goalState(s) === "merged" ? ": available now" : ""}.</span>` : ""}
      ${isGoal(s) ? `<button class="btn" data-act="move">Move to another machine…</button>` : ""}
      ${s.harness !== "shell" ? `<button class="btn" data-act="restart">Restart harness in place…</button>` : ""}
      ${!s.yours ? `<button class="btn danger" data-act="kill">Kill window…</button>` : `<span class="why">Your own windows are never killed from the board (ROLE_OWNER only).</span>`}
      <button class="btn" data-act="attachline">Attach from your terminal…</button><button class="btn" data-new="${m.id}">New session on ${esc(m.short)}…</button></div></section>
    ${attachSection(s, m)}
    <section><h2>History</h2><button class="btn ${ui.histOpen ? "on" : ""}" data-act="history">${ui.histOpen ? "Hide history" : "History…"}</button>${ui.histOpen ? historyPanel(s) : ""}</section>
    <section><h2>Timeline</h2><ol class="timeline">${audit.filter(a => a.s === s.id).map(a => `<li><span class="num">${hhmm(a.at)}</span> <b>${esc(a.who)}</b> ${esc(a.what)}</li>`).join("") || `<li class="why">No actions yet.</li>`}
      <li><span class="num">${hhmm(s.started)}</span> window created</li></ol></section></aside>`;
}

const confirmBoxS = () => `<div class="confirm"><div>${esc(ui.confirm.label)}</div><code>${esc(ui.confirm.cmd)}</code>${ui.confirm.note ? `<div class="why">${esc(ui.confirm.note)}</div>` : ""}
  ${ui.confirm.input !== undefined ? `<input id="cfIn" class="field mono" value="${esc(ui.confirm.input)}" aria-label="Text">` : ""}
  <div class="actions">${ui.confirm.copy ? `<button class="btn primary" data-act="copy">Copy line</button>` : `<button class="btn primary ${ui.confirm.danger ? "danger" : ""}" data-act="do">${esc(ui.confirm.go)}</button>`}
  <button class="btn" data-act="cancel">Cancel</button></div></div>`;

function goalControls(s) {
  const subs = GOAL_SUBS[s.harness] ?? [], has = k => subs.includes(k);
  const btn = (k, label, ok, title) => `<button class="btn sm" data-goal="${k}" ${ok ? "" : "disabled"} title="${esc(title)}">${label}</button>`;
  return `<section><h2>Goal controls</h2><div class="why">${esc(s.harness)}: <span class="mono">${esc(GOAL_SYNTAX[s.harness])}</span></div>
    <div class="actions">${btn("pause", "Pause", has("pause") || s.harness === "claude", has("pause") ? "RunHarnessCommand /goal pause" : s.harness === "claude" ? "No /goal pause: sends Interrupt" : "GOAL_SUBCOMMAND_UNSUPPORTED")}
      ${btn("resume", "Resume", has("resume"), has("resume") ? "RunHarnessCommand /goal resume" : "GOAL_SUBCOMMAND_UNSUPPORTED")}
      ${btn("edit", "Edit…", has("edit") || s.harness === "claude", has("edit") ? "RunHarnessCommand /goal edit" : s.harness === "claude" ? "No /goal edit: sets /goal again" : "GOAL_SUBCOMMAND_UNSUPPORTED")}
      ${btn("clear", "Clear…", has("clear"), has("clear") ? "RunHarnessCommand /goal clear" : "GOAL_SUBCOMMAND_UNSUPPORTED")}</div>
    <div class="why">Each button sends the harness's own /goal subcommand through RunHarnessCommand (class GOAL) at an idle prompt. ${s.harness === "claude" ? "Claude Code has no pause/resume: Pause sends Interrupt (F4)." : s.harness === "opencode" ? "OpenCode has no /goal; goals need the generic goal protocol first." : "No composite stop exists; a goal ends with /goal clear."}</div></section>`;
}

function movePanel(s) {
  const targets = machines.filter(x => x.id !== s.m && x.harnesses.includes(s.harness));
  const pick = ui.moveTo && ui.moveTo !== s.m ? ui.moveTo : (targets.filter(x => freeActive(x) > 0).sort((a, b) => freeActive(b) - freeActive(a))[0] ?? targets[0]).id;
  ui.moveTo = pick;
  const to = mById(pick), full = freeActive(to) === 0 || freeTotal(to) === 0;
  return `<div class="confirm move"><div><b>Move this goal.</b> v2 has no move command: the goal is cleared here and dispatched there from the pushed branch. Unpushed work stays behind.</div>
    <label class="slot"><span>Move to</span><select id="moveTo">${targets.map(x => `<option value="${x.id}" ${x.id === pick ? "selected" : ""}>${esc(x.name)} (${freeActive(x)}/${x.maxActive} active free${x.drained ? ", draining" : ""})</option>`).join("")}</select></label>
    ${full ? `<p class="bad-t">${esc(to.name)} has no free slot${to.drained ? " (draining)" : ""}.</p>` : ""}
    <code>${esc(moveCmds(s, to))}</code>
    <div class="actions"><button class="btn primary" data-act="domove" ${full ? "disabled" : ""}>Move goal</button><button class="btn" data-act="closemove">Cancel</button></div></div>`;
}

function wireSession(s, m) {
  const v = $("view");
  v.querySelectorAll("[data-sid]").forEach(b => b.onclick = () => { ui.moveOpen = false; ui.newOpen = null; T.overlay = null; go("session", b.dataset.sid); });
  v.querySelectorAll("[data-step]").forEach(b => b.onclick = () => stepSession(+b.dataset.step));
  v.querySelectorAll("[data-tri]").forEach(b => b.onclick = () => { ui.triageIdx = Math.max(0, triageItems().findIndex(t => t.id === b.dataset.tri)); go("triage"); });
  v.querySelectorAll("[data-new]").forEach(b => b.onclick = () => { ui.newOpen = b.dataset.new; ui.confirm = null; rerender(); });
  v.querySelectorAll("[data-goal]").forEach(b => b.onclick = () => goalAct(s, b.dataset.goal));
  v.querySelectorAll("[data-cmd]").forEach(b => b.onclick = () => paletteAct(s, b.dataset.cmd, b.dataset.cls));
  $("moveTo")?.addEventListener("change", e => { ui.moveTo = e.target.value; rerender(); });
  $("nsName")?.addEventListener("change", e => { ui.newState.name = e.target.value.trim(); rerender(); });
  $("nsRepo")?.addEventListener("change", e => { ui.newState.repo = e.target.value; rerender(); });
  $("nsHarness")?.addEventListener("change", e => { ui.newState.harness = e.target.value; rerender(); });
  $("nsReuse")?.addEventListener("change", e => { ui.newState.reuse = e.target.checked; rerender(); });
  $("roToggle")?.addEventListener("change", e => setReadOnly(s, e.target.checked));
  v.querySelectorAll("[data-act]").forEach(b => b.onclick = () => sessionAct(s, m, b.dataset.act));
  wireTmux(s);
}

function setReadOnly(s, ro) { T.ro = ro; log(`browser terminal ${ro ? "read-only" : "read-write"} (OpenAttach read_only=${ro})`, s); rerender(); }

function goalAct(s, sub) {
  const text = sub === "edit" ? (s.line ?? "").replace(/^Drive /, "drive ") : "";
  const g = goalCtl(s, sub, text);
  if (!g.ok) return;
  ui.confirm = { sid: s.id, label: g.label, cmd: g.cmd, go: sub[0].toUpperCase() + sub.slice(1), what: g.what, danger: sub === "clear", sub, input: sub === "edit" ? text : undefined,
    note: "Typed only at an idle prompt (ready pattern showing, input line empty); otherwise PANE_BUSY. A composite is never used." };
  rerender();
}

function sessionAct(s, m, act) {
  if (act === "cancel") ui.confirm = null;
  if (act === "drawer") { T.drawer = !T.drawer; if (!T.drawer) { ui.confirm = null; ui.moveOpen = false; ui.newOpen = null; } }
  if (act === "side") T.side = !T.side;
  if (act === "ro") return setReadOnly(s, !T.ro);
  if (act === "reattach") { T.detached = null; T.focused = true; log(`re-attached the browser terminal to ${s.sess} on ${m.short} (${attachCmd(s, T.ro)}; mock)`, s); }
  if (act === "keys") { const k = $("keysIn").value; if (!k) return $("keysIn").focus(); ui.confirm = { sid: s.id, label: "Send these keys to the pane (literal text plus Enter; never a tmux command string):", cmd: sendKeysCmd(s, k, isGoal(s) ? { require_empty_input_line: true } : {}), go: "Send", what: `sent keys “${k}”`, note: isGoal(s) ? "Typing into a running goal asks for confirmation (ADR 0004); your own windows go straight through." : "" }; }
  if (act === "attachline") ui.confirm = { sid: s.id, label: "Attach from your own terminal. This line runs on your machine; the hub never runs ssh.", cmd: attachLine(m, s), copy: true };
  if (act === "palette") { ui.paletteOpen = !ui.paletteOpen; ui.confirm = null; }
  if (act === "history") { ui.histOpen = !ui.histOpen; }
  if (act === "avail") ui.confirm = { sid: s.id, label: s.available ? "Withdraw the mark; the dispatcher stops treating this session as free capacity:" : "Let the dispatcher start the next goal in this idle session instead of opening a new window:", cmd: availCmd(s, !s.available), go: s.available ? "Withdraw" : "Mark available", what: s.available ? "withdrew availability" : "marked available for work", avail: !s.available, note: "Any keystroke of yours clears idle at once; a goal is never typed into a non-empty input line." };
  if (act === "restart") ui.confirm = { sid: s.id, label: `Restart the harness in the same window through the adapter's ResumeArgs (${RESUME[s.harness]}); the goal file keeps the goal:`, cmd: respawnCmd(s), go: "Restart", danger: true, what: "restarted harness in place", note: "Dangerous kind: may wait for `fleet-agent approve <command_id>` on the machine (AWAITING_LOCAL_CONFIRM)." };
  if (act === "kill") ui.confirm = { sid: s.id, label: "Close this fleet-started window. The branch, worktree and claim stay. Clear the goal first if it is still set.", cmd: killCmd(s), go: "Kill window", danger: true, what: "killed window", kill: true };
  if (act === "move") { ui.moveOpen = true; ui.confirm = null; }
  if (act === "closemove") ui.moveOpen = false;
  if (act === "copy") { navigator.clipboard?.writeText(ui.confirm.cmd).then(() => {}, () => {}); log("copied attach line for your terminal", s); ui.confirm = null; }
  if (act === "newcancel") ui.newOpen = null;
  if (act === "newgo") return newSessionGo();
  if (act === "do") {
    const c = ui.confirm, typed = c.input !== undefined ? $("cfIn").value : null;
    log(`${c.what}${typed ? `: ${typed}` : ""} (mock, not sent)`, s);
    if (c.sub === "clear" || c.kill) { s.line = null; s.stopped = true; }
    else if (c.sub === "edit" && typed) { s.line = typed[0].toUpperCase() + typed.slice(1); }
    else if (c.avail !== undefined) { s.available = c.avail; }
    else s.last = new Date();
    ui.confirm = null;
  }
  if (act === "domove") {
    const from = mById(s.m), to = mById(ui.moveTo), idx = Math.max(-1, ...sessions.filter(x => x.m === to.id && x.sess === "fleet").map(x => x.idx)) + 1;
    log(`moved goal from ${from.short} to ${to.short}: /goal clear here, StartGoal there (mock, nothing ran)`, s);
    s.m = to.id; s.sess = "fleet"; s.idx = idx; s.target = `fleet:${idx}`; s.line = `Drive ${s.display} to merged`; s.watchAlive = false; s.last = new Date(); ui.moveOpen = false;
  }
  rerender();
}

function stepSession(d) {
  const order = sessionOrder(), i = order.findIndex(x => x.id === ui.selected);
  ui.moveOpen = false; ui.newOpen = null; T.overlay = null; go("session", order[(i + d + order.length) % order.length].id);
}
// Board-level keys for this view; they run only while the terminal does not have focus (its own handler stops propagation).
function sessionKey(e) {
  if (e.key === "]") stepSession(1); else if (e.key === "[") stepSession(-1);
  else if (e.key === "i") { T.drawer = !T.drawer; rerender(); }
}
