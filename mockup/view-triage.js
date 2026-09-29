// Triage: one queue of everything that wants a decision, worked with the keyboard.
function sendCmd(q, s) {
  const m = mById(s.m), val = ui.choice[q.id] ?? q.def;
  const keys = q.kind === "perm" ? q.keys[q.options.indexOf(val)] : `"${val.replace(/"/g, '\\"')}" Enter`;
  return `${m.hub ? "" : `ssh ${m.user}@${m.ip} `}tmux send-keys -t ${s.target} ${keys}`;
}

function answer(q, val, how) {
  const s = sById(q.s);
  ui.answered[q.id] = val; ui.confirm = null;
  log(`${how} “${val}” for: ${q.text}${q.sendable ? " (mock, not sent)" : " (notice only)"}`, s);
  rerender();
}

function triageTitle(it) {
  if (it.type === "question") return it.q.text;
  if (it.type === "stuck") return `No output for ${rel(it.s.last).replace(" ago", "")}: ${it.s.task}`;
  return `CI red on ${shortName(it.pr.repo)}#${it.pr.n}, nobody on it`;
}

function renderTriage() {
  const items = triageItems();
  ui.triageIdx = Math.min(ui.triageIdx, Math.max(0, items.length - 1));
  const cur = items[ui.triageIdx];
  $("view").innerHTML = `<div class="triage">
    <section class="tq" aria-label="Triage queue">
      <div class="section-head"><h2>Needs you</h2><span class="hint">j/k move · a act · d default · s snooze · o open</span></div>
      <ol class="tq-list">${items.length ? items.map((it, i) => {
        const tone = it.type === "question" ? (it.q.kind === "perm" ? "bad" : "warn") : it.type === "stuck" ? "bad" : "accent";
        const label = it.type === "question" ? (it.q.kind === "perm" ? "permission" : "question") : it.type === "stuck" ? "stuck" : "suggestion";
        const src = it.s ? `${it.s.agent} · ${where(it.s)}` : `${shortName(it.pr.repo)} · ${it.pr.ci.fail} failing`;
        return `<li><button class="tq-item ${tone}" data-ti="${i}" aria-current="${i === ui.triageIdx}">
          <span class="tq-top"><span class="tq-kind">${label}</span><span class="tq-src">${esc(src)}</span>
          ${it.q ? `<span class="timer num" data-timer="${it.q.id}">${timerText(it.q)}</span>` : ""}</span>
          <span class="tq-title">${esc(triageTitle(it))}</span></button></li>`;
      }).join("") : `<li class="tq-empty">Nothing needs you. Press 2 to plan work or n to step through sessions.</li>`}</ol>
    </section>
    <section class="tf" aria-label="Selected item">${cur ? triageDetail(cur) : `<div class="empty">Queue is clear.</div>`}</section>
  </div>
  <section class="glance" aria-label="Fleet at a glance"><h2>Machines</h2>${machines.map(m => {
    const ss = sessions.filter(s => s.m === m.id && s.agent !== "shell");
    return `<button class="gl-row" data-fleet="${m.id}"><span class="dot ${m.drained ? "warn" : "ok"}"></span><span class="gl-name">${esc(m.name)}</span>
      <span class="gl-slots num">${busy(m)}/${m.slots}${m.drained ? " · draining" : ""}</span>
      <span class="gl-sess">${ss.map(s => `<span class="sdot ${liveState(s)}" title="${esc(s.agent + " " + s.target + " " + s.repo)}"></span>`).join("")}</span></button>`;
  }).join("")}</section>`;

  $("view").querySelectorAll("[data-ti]").forEach(b => b.onclick = () => { ui.triageIdx = +b.dataset.ti; ui.confirm = null; rerender(); });
  $("view").querySelectorAll("[data-fleet]").forEach(b => b.onclick = () => { ui.fleetFocus = b.dataset.fleet; go("fleet"); });
  if (cur) wireTriage(cur);
}

function triageDetail(it) {
  if (it.type === "question") {
    const q = it.q, s = it.s, pick = ui.choice[q.id] ?? q.def;
    return `<div class="tf-head">${agentChip(s.agent)}<span>${esc(mById(s.m).name)} · ${esc(s.target)} · ${esc(s.repo)}</span></div>
      <h3>${esc(q.text)}</h3>
      <p class="dflt">Default: <b>${esc(q.def)}</b>. ${esc(q.why)}</p>
      <div class="opts">${q.options.map(o => `<button class="opt" data-o="${esc(o)}" aria-pressed="${pick === o}">${esc(o)}</button>`).join("")}</div>
      ${q.kind === "text" ? `<input id="free-${q.id}" class="field" placeholder="Or type your own answer" aria-label="Custom answer" value="${ui.choice[q.id] && !q.options.includes(ui.choice[q.id]) ? esc(ui.choice[q.id]) : ""}">` : ""}
      ${ui.confirm?.id === q.id ? `<div class="confirm"><div>The pane shows the prompt. This runs:</div><code>${esc(sendCmd(q, s))}</code>
        <div class="actions"><button class="btn primary" data-act="send">Send to pane</button><button class="btn" data-act="cancel">Cancel</button></div></div>` :
      `<div class="actions"><button class="btn primary" data-act="review" ${q.sendable ? "" : "disabled"}>Send answer… <kbd>a</kbd></button>
        <button class="btn" data-act="default">Accept default <kbd>d</kbd></button><button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button>
        <button class="btn" data-act="open">Open session <kbd>o</kbd></button></div>`}
      ${q.blocked ? `<p class="why">${esc(q.blocked)}</p>` : ""}
      <pre class="term small">${s.pane.slice(-4).map(([c, t]) => c ? `<span class="${c}">${esc(t)}</span>` : esc(t)).join("\n")}</pre>`;
  }
  if (it.type === "stuck") {
    const s = it.s;
    return `<div class="tf-head">${agentChip(s.agent)}<span>${esc(mById(s.m).name)} · ${esc(s.target)} · ${esc(s.repo)}</span></div>
      <h3>${esc(s.task)}</h3>
      <p class="dflt">No pane output for <b data-t="${+s.last}">${rel(s.last)}</b>. ${s.tasks.filter(t => t[1]).length}/${s.tasks.length} tasks done.</p>
      <pre class="term small">${s.pane.map(([c, t]) => c ? `<span class="${c}">${esc(t)}</span>` : esc(t)).join("\n")}</pre>
      ${ui.confirm?.id === s.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div><code>${esc(ui.confirm.cmd)}</code>
        <div class="actions"><button class="btn primary" data-act="do">Run it</button><button class="btn" data-act="cancel">Cancel</button></div></div>` :
      `<div class="actions"><button class="btn primary" data-act="open">Open session <kbd>a</kbd></button>
        <button class="btn" data-act="nudge">Nudge (Enter)</button><button class="btn" data-act="move">Move to another machine…</button>
        <button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button></div>`}`;
  }
  const p = it.pr;
  return `<div class="tf-head"><span class="mono ref">${esc(p.repo)}#${p.n}</span><span class="ci bad">✗ ${p.ci.fail} failing · ✓ ${p.ci.pass}</span></div>
    <h3>${esc(p.t)}</h3>
    <p class="dflt">Suggested job: <b>${defaultJob(p)}</b> on <span class="mono">${esc(p.branch)}</span>. Nothing starts until you approve.</p>
    ${dispatchState[keyOf(p)] ? dispatchPanel(p) : `<div class="actions"><button class="btn primary" data-act="plan">Pick machine and agent… <kbd>a</kbd></button>
      <button class="btn" data-act="dismiss">Dismiss <kbd>x</kbd></button></div>`}`;
}

function wireTriage(it) {
  const v = $("view");
  v.querySelectorAll(".opt").forEach(b => b.onclick = () => { ui.choice[it.id] = b.dataset.o; rerender(); });
  const inp = v.querySelector(".field");
  if (inp) inp.oninput = () => { ui.choice[it.id] = inp.value.trim() || it.q.def; v.querySelectorAll(".opt").forEach(b => b.setAttribute("aria-pressed", b.dataset.o === ui.choice[it.id])); };
  v.querySelectorAll("[data-act]").forEach(b => b.onclick = () => triageAct(it, b.dataset.act));
  if (it.type === "suggest") wireDispatch(v, [it.pr]);
}

function triageAct(it, act) {
  const s = it.s;
  if (act === "cancel") { ui.confirm = null; return rerender(); }
  if (act === "open") return go("session", s.id);
  if (act === "snooze") { ui.snoozed[it.id] = Date.now() + 15 * 60e3; log(`snoozed ${it.type} for 15m`, s); return rerender(); }
  if (it.type === "question") {
    if (act === "review" && it.q.sendable) ui.confirm = { id: it.q.id };
    if (act === "send") return answer(it.q, ui.choice[it.q.id] ?? it.q.def, "sent");
    if (act === "default") return answer(it.q, it.q.def, "accepted default");
  }
  if (it.type === "stuck") {
    const m = mById(s.m), via = m.hub ? "" : `ssh ${m.user}@${m.ip} `;
    if (act === "nudge") ui.confirm = { id: s.id, label: "Send one Enter key to the pane:", cmd: `${via}tmux send-keys -t ${s.target} Enter`, what: "nudged with Enter" };
    if (act === "move") { ui.moveOpen = true; return go("session", s.id); }
    if (act === "do") { log(`${ui.confirm.what} (mock, not sent)`, s); s.last = new Date(); s.state = "working"; ui.confirm = null; }
  }
  if (it.type === "suggest") {
    if (act === "plan") openDispatch(it.pr);
    if (act === "dismiss") { ui.dismissed[it.id] = true; log(`dismissed fix-checks suggestion for ${shortName(it.pr.repo)}#${it.pr.n}`); }
  }
  rerender();
}

function triageKey(e) {
  const items = triageItems(), cur = items[ui.triageIdx];
  const k = e.key;
  if (k === "j" || k === "ArrowDown") { ui.triageIdx = Math.min(ui.triageIdx + 1, items.length - 1); ui.confirm = null; rerender(); }
  else if (k === "k" || k === "ArrowUp") { ui.triageIdx = Math.max(ui.triageIdx - 1, 0); ui.confirm = null; rerender(); }
  else if (!cur) return;
  else if (k === "o" && cur.s) triageAct(cur, "open");
  else if (k === "s" && cur.type !== "suggest") triageAct(cur, "snooze");
  else if (k === "d" && cur.type === "question") triageAct(cur, "default");
  else if (k === "x" && cur.type === "suggest") triageAct(cur, "dismiss");
  else if (k === "a") triageAct(cur, cur.type === "question" ? "review" : cur.type === "stuck" ? "open" : "plan");
}
