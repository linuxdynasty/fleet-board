// Session: every session in a sidebar for fast switching, one session in full detail.
const RESUME = { claude: "claude --continue", codex: "codex resume --last", muse: "muse resume --last" };
const via = m => m.hub ? "" : `ssh ${m.user}@${m.ip} `;

function renderSessionView() {
  const order = sessionOrder();
  if (!sById(ui.selected)) ui.selected = order[0].id;
  const s = sById(ui.selected), m = mById(s.m), st = liveState(s), pos = order.indexOf(s) + 1;
  const pr = prs.find(p => p.branch === s.branch), q = s.q && qById(s.q);
  const done = s.tasks.filter(t => t[1]).length;
  $("view").innerHTML = `<div class="sess">
    <nav class="sess-side" aria-label="All sessions">${machines.map(mm => {
      const ss = sessions.filter(x => x.m === mm.id);
      return `<div class="side-group"><div class="side-m"><span>${esc(mm.short)}</span><span class="num">${busy(mm)}/${mm.slots}${mm.drained ? " · draining" : ""}</span></div>
        ${ss.map(x => `<button class="side-s" data-sid="${x.id}" aria-current="${x.id === s.id}"><span class="sdot ${liveState(x)}"></span>
          <span class="side-a" style="color:${agentColor[x.agent]}">${esc(x.agent)}</span><span class="side-r">${esc(x.repo)}</span><span class="side-t mono">${esc(x.target)}</span></button>`).join("") || `<p class="col-empty">No sessions</p>`}</div>`;
    }).join("")}<p class="why">[ and ] step through · n jumps to the next one that needs you</p></nav>

    <div class="sess-main">
      <div class="sess-head">${agentChip(s.agent)}<h3>${esc(s.task)}</h3><span class="pill ${st}">${st}</span>
        <span class="spacer"></span><button class="btn sm" data-step="-1" aria-label="Previous session">[</button><span class="num why">${pos} of ${order.length}</span><button class="btn sm" data-step="1" aria-label="Next session">]</button></div>
      <div class="sess-meta"><span>${esc(m.name)} → <span class="mono">${esc(s.target)}</span></span><span>${esc(s.repo)}${s.branch ? ` · <span class="mono">${esc(s.branch)}</span>` : ""}</span>
        <span>started ${hhmm(s.started)} · last output <span data-t="${+s.last}">${rel(s.last)}</span></span></div>
      ${q && !ui.answered[q.id] ? `<div class="sess-q"><b>Waiting on you:</b> ${esc(q.text)} <span class="why">default ${esc(q.def)}</span> <button class="btn sm" data-tri="${q.id}">Answer in Triage</button></div>` : ""}
      <pre class="term">${s.pane.map(([c, t]) => c ? `<span class="${c}">${esc(t)}</span>` : esc(t)).join("\n")}</pre>
      <div class="keys"><input id="keysIn" class="field" placeholder="Type keys to send to this pane" aria-label="Keys to send"><button class="btn" data-act="keys">Send…</button>
        <button class="btn" data-act="attach">Attach…</button></div>
      ${ui.confirm?.sid === s.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div><code>${esc(ui.confirm.cmd)}</code>
        <div class="actions">${ui.confirm.copy ? `<button class="btn primary" data-act="copy">Copy command</button>` : `<button class="btn primary ${ui.confirm.danger ? "danger" : ""}" data-act="do">${esc(ui.confirm.go)}</button>`}
        <button class="btn" data-act="cancel">Cancel</button></div></div>` : ""}
      ${ui.moveOpen ? movePanel(s) : ""}
    </div>

    <aside class="sess-rail">
      <section><h2>Tasks <span class="num why">${done}/${s.tasks.length}</span></h2>
        ${s.tasks.length ? `<ol class="tasks">${s.tasks.map((t, i) => `<li class="${t[1] ? "done" : i === done ? "cur" : ""}">${esc(t[0])}</li>`).join("")}</ol>` : `<p class="why">No task list reported yet.</p>`}</section>
      ${pr ? `<section><h2>Pull request</h2><div class="mono ref">${esc(pr.repo)}#${pr.n}</div><div class="card-t">${esc(pr.t)}</div>
        <div>${pr.ci.fail ? `<span class="ci bad">✗ ${pr.ci.fail} failing</span>` : ""} <span class="ci ok">✓ ${pr.ci.pass}</span> ${pr.ci.pend ? `<span class="ci warn">● ${pr.ci.pend}</span>` : ""}</div></section>` : ""}
      <section><h2>Controls</h2><div class="ctl">
        <button class="btn" data-act="move">Move to another machine…</button>
        <button class="btn" data-act="restart">Restart in place…</button>
        <button class="btn danger" data-act="stop">Stop session…</button></div></section>
      <section><h2>Timeline</h2><ol class="timeline">${audit.filter(a => a.s === s.id).map(a => `<li><span class="num">${hhmm(a.at)}</span> <b>${esc(a.who)}</b> ${esc(a.what)}</li>`).join("") || `<li class="why">No actions yet.</li>`}
        <li><span class="num">${hhmm(s.started)}</span> session started</li></ol></section>
    </aside></div>`;
  wireSession(s, m);
}

function movePanel(s) {
  const targets = machines.filter(x => x.id !== s.m);
  const pick = ui.moveTo && ui.moveTo !== s.m ? ui.moveTo : (targets.filter(x => free(x) > 0).sort((a, b) => free(b) - free(a))[0] ?? targets[0]).id;
  ui.moveTo = pick;
  const to = mById(pick), from = mById(s.m), full = free(to) === 0;
  const cmd = `${via(from)}tmux send-keys -t ${s.target} C-c && ${via(from)}tmux kill-window -t ${s.target}\n${via(to)}tmux new-window -d -t ${to.tmux} -n ${s.repo} -c ${to.repoRoot}/${s.repo} "${s.agent} 'Resume: ${s.task}. Branch ${s.branch}. Pull first.'"`;
  return `<div class="confirm move"><div><b>Move this session.</b> The agent stops here and restarts on the other machine from the pushed branch. Unpushed work stays behind.</div>
    <label class="slot"><span>Move to</span><select id="moveTo">${targets.map(x => `<option value="${x.id}" ${x.id === pick ? "selected" : ""}>${esc(x.name)} (${free(x)}/${x.slots} free${x.drained ? ", draining" : ""})</option>`).join("")}</select></label>
    ${full ? `<p class="bad-t">${esc(to.name)} has no free slot${to.drained ? " (draining)" : ""}.</p>` : ""}
    <code>${esc(cmd)}</code>
    <div class="actions"><button class="btn primary" data-act="domove" ${full ? "disabled" : ""}>Move session</button><button class="btn" data-act="closemove">Cancel</button></div></div>`;
}

function wireSession(s, m) {
  const v = $("view");
  v.querySelectorAll("[data-sid]").forEach(b => b.onclick = () => { ui.moveOpen = false; go("session", b.dataset.sid); });
  v.querySelectorAll("[data-step]").forEach(b => b.onclick = () => stepSession(+b.dataset.step));
  v.querySelectorAll("[data-tri]").forEach(b => b.onclick = () => { ui.triageIdx = Math.max(0, triageItems().findIndex(t => t.id === b.dataset.tri)); go("triage"); });
  $("moveTo")?.addEventListener("change", e => { ui.moveTo = e.target.value; rerender(); });
  v.querySelectorAll("[data-act]").forEach(b => b.onclick = () => sessionAct(s, m, b.dataset.act));
}

function sessionAct(s, m, act) {
  const attach = `ssh -t ${m.user}@${m.ip} tmux attach -t ${s.target.split(":")[0]} \\; select-window -t ${s.target}`;
  if (act === "cancel") ui.confirm = null;
  if (act === "keys") { const k = $("keysIn").value; if (!k) return $("keysIn").focus(); ui.confirm = { sid: s.id, label: "Send these keys to the pane:", cmd: `${via(m)}tmux send-keys -t ${s.target} "${k.replace(/"/g, '\\"')}" Enter`, go: "Send", what: `sent keys “${k}”` }; }
  if (act === "attach") ui.confirm = { sid: s.id, label: "Attach from a terminal on the hub. You get the live pane with full keyboard control.", cmd: attach, copy: true };
  if (act === "restart") ui.confirm = { sid: s.id, label: "Stop the agent and start it again in the same window, same branch:", cmd: `${via(m)}tmux respawn-pane -k -t ${s.target} "${RESUME[s.agent] ?? s.agent}"`, go: "Restart", danger: true, what: "restarted in place" };
  if (act === "stop") ui.confirm = { sid: s.id, label: "Interrupt the agent, then close its window. The branch and worktree stay.", cmd: `${via(m)}tmux send-keys -t ${s.target} C-c && ${via(m)}tmux kill-window -t ${s.target}`, go: "Stop session", danger: true, what: "stopped session", stop: true };
  if (act === "move") { ui.moveOpen = true; ui.confirm = null; }
  if (act === "closemove") ui.moveOpen = false;
  if (act === "copy") { const c = ui.confirm.cmd; navigator.clipboard?.writeText(c).then(() => {}, () => {}); log("copied attach command", s); ui.confirm = null; }
  if (act === "do") {
    const c = ui.confirm; log(`${c.what} (mock, not sent)`, s);
    if (c.stop) s.state = "done"; else { s.state = "working"; s.last = new Date(); }
    ui.confirm = null;
  }
  if (act === "domove") {
    const from = mById(s.m), to = mById(ui.moveTo);
    log(`moved from ${from.short} to ${to.short} (mock, nothing ran)`, s);
    s.m = to.id; s.target = `${to.tmux}:${s.repo}`; s.state = "working"; s.last = new Date(); ui.moveOpen = false;
  }
  rerender();
}

function stepSession(d) {
  const order = sessionOrder(), i = order.findIndex(x => x.id === ui.selected);
  ui.moveOpen = false; go("session", order[(i + d + order.length) % order.length].id);
}
function sessionKey(e) {
  if (e.key === "]") stepSession(1);
  else if (e.key === "[") stepSession(-1);
}
