// Triage: only what needs a person. Goals never ask questions (an arbiter model
// answers those), so this is permission prompts, stuck goals, DONE claims GitHub
// disagrees with, and accounts that reached the owner's reserve.
const via = m => m.hub ? "" : `ssh ${m.user}@${m.ip} `;
const KIND = { perm: ["permission", "bad"], stuck: ["stuck goal", "bad"], verify: ["done? GitHub disagrees", "warn"], pace: ["reserve reached", "accent"] };

function triageTitle(it) {
  if (it.type === "perm") return it.p.text;
  if (it.type === "stuck") return it.s.line.replace(/^BLOCKED:\s*/, "");
  if (it.type === "verify") return `${ref(it.s)} says DONE, PR #${it.s.pr} is still open`;
  return `${it.a.label} is at ${used(it.a)}%; the fleet stopped giving it new work`;
}

function renderTriage() {
  const items = triageItems();
  ui.triageIdx = Math.min(ui.triageIdx, Math.max(0, items.length - 1));
  const cur = items[ui.triageIdx];
  $("view").innerHTML = `<div class="triage">
    <section class="tq" aria-label="Triage queue">
      <div class="section-head"><h2>Needs you</h2><span class="hint">j/k move · a act · s snooze · o open</span></div>
      <ol class="tq-list">${items.length ? items.map((it, i) => {
        const [label, tone] = KIND[it.type];
        const src = it.s ? `${it.s.harness} · ${where(it.s)}` : `${it.a.harness} account`;
        return `<li><button class="tq-item ${tone}" data-ti="${i}" aria-current="${i === ui.triageIdx}">
          <span class="tq-top"><span class="tq-kind">${label}</span><span class="tq-src">${esc(src)}</span></span>
          <span class="tq-title">${esc(triageTitle(it))}</span></button></li>`;
      }).join("") : `<li class="tq-empty">Nothing needs you. Goals are running.</li>`}</ol>
    </section>
    <section class="tf" aria-label="Selected item">${cur ? triageDetail(cur) : `<div class="empty">Queue is clear.</div>`}</section>
  </div>
  <section class="glance" aria-label="Fleet at a glance"><h2>Machines</h2>${machines.map(m => {
    const ss = sessions.filter(s => s.m === m.id && isGoal(s));
    return `<button class="gl-row" data-fleet="${m.id}"><span class="dot ${m.drained ? "warn" : "ok"}"></span><span class="gl-name">${esc(m.name)}</span>
      <span class="gl-slots num">${activeOn(m)}/${m.maxActive} active · ${totalOn(m)}/${m.maxTotal} total${m.drained ? " · draining" : ""}</span>
      <span class="gl-sess">${ss.map(s => `<span class="sdot ${goalState(s)}" title="${esc(s.harness + " " + ref(s) + " " + goalState(s))}"></span>`).join("")}</span></button>`;
  }).join("")}</section>`;
  $("view").querySelectorAll("[data-ti]").forEach(b => b.onclick = () => { ui.triageIdx = +b.dataset.ti; ui.confirm = null; rerender(); });
  $("view").querySelectorAll("[data-fleet]").forEach(b => b.onclick = () => { ui.fleetFocus = b.dataset.fleet; go("fleet"); });
  if (cur) $("view").querySelectorAll("[data-act]").forEach(b => b.onclick = () => triageAct(cur, b.dataset.act));
  if (cur?.type === "perm") $("view").querySelectorAll(".opt").forEach(b => b.onclick = () => { ui.choice[cur.id] = b.dataset.o; rerender(); });
}

const head = s => `<div class="tf-head">${agentChip(s.harness)}<span>${esc(mById(s.m).name)} · ${esc(s.target)} · ${esc(ref(s))}</span></div>`;
const term = (lines, cls = "small") => `<pre class="term ${cls}">${lines.map(([c, t]) => c ? `<span class="${c}">${esc(t)}</span>` : esc(t)).join("\n")}</pre>`;
const confirmBox = () => `<div class="confirm"><div>${esc(ui.confirm.label)}</div><code>${esc(ui.confirm.cmd)}</code>
  <div class="actions"><button class="btn primary" data-act="do">${esc(ui.confirm.go ?? "Run it")}</button><button class="btn" data-act="cancel">Cancel</button></div></div>`;

function triageDetail(it) {
  if (it.type === "perm") {
    const p = it.p, pick = ui.choice[p.id] ?? p.def;
    return `${head(it.s)}<h3>${esc(p.text)}</h3><p class="dflt">Default: <b>${esc(p.def)}</b>. ${esc(p.why)}</p>
      <div class="opts">${p.options.map(o => `<button class="opt" data-o="${esc(o)}" aria-pressed="${pick === o}">${esc(o)}</button>`).join("")}</div>
      ${ui.confirm?.id === p.id ? confirmBox() : `<div class="actions"><button class="btn primary" data-act="review">Send choice… <kbd>a</kbd></button>
        <button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button><button class="btn" data-act="open">Open session <kbd>o</kbd></button></div>`}
      ${term(it.s.pane)}`;
  }
  if (it.type === "stuck") {
    const s = it.s;
    return `${head(s)}<h3>${esc(triageTitle(it))}</h3>
      <p class="dflt">Goal file: <code class="inline">${esc(s.line)}</code>. No watcher is armed, so nothing will wake it. Last output <b data-t="${+s.last}">${rel(s.last)}</b>.</p>
      ${term(s.pane)}
      ${ui.confirm?.id === s.id ? confirmBox() : `<div class="actions"><button class="btn primary" data-act="open">Open session <kbd>a</kbd></button>
        <button class="btn" data-act="hint">Send a hint…</button><button class="btn" data-act="move">Move to another machine…</button>
        <button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button></div>`}`;
  }
  if (it.type === "verify") {
    const s = it.s, pr = prs.find(p => p.n === s.pr);
    return `${head(s)}<h3>${esc(triageTitle(it))}</h3>
      <p class="dflt">The agent wrote <code class="inline">DONE</code>, but GitHub shows PR #${pr.n} <b>${pr.state}</b> with ${pr.ci.fail} failing checks. The worker stays busy until GitHub confirms a merge.</p>
      ${ui.confirm?.id === s.id ? confirmBox() : `<div class="actions"><button class="btn primary" data-act="reopen">Reopen goal… <kbd>a</kbd></button>
        <button class="btn" data-act="open">Open session <kbd>o</kbd></button><button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button></div>`}`;
  }
  const a = it.a, p = pace(a), running = sessions.filter(s => s.harness === a.harness && ["working", "waiting", "stuck"].includes(goalState(s))).length;
  return `<div class="tf-head">${agentChip(a.harness)}<span>${esc(a.label)} account</span></div><h3>${esc(triageTitle(it))}</h3>
    <p class="dflt">${p.used}% of the ${esc(bindingBucket(a).label)} weekly limit is used; your reserve starts at ${p.cap}%. Resets in <b data-until="${a.resets}">${until(a.resets)}</b>. ${running} running ${a.harness} goal${running === 1 ? "" : "s"} keep going; new work goes to other accounts.</p>
    <div class="actions"><button class="btn primary" data-act="fleet">Change reserve…</button><button class="btn" data-act="ack">Got it <kbd>a</kbd></button></div>`;
}

function triageAct(it, act) {
  const s = it.s, m = s && mById(s.m);
  if (act === "cancel") { ui.confirm = null; return rerender(); }
  if (act === "open") return go("session", s.id);
  if (act === "fleet") return go("fleet");
  if (act === "snooze") { ui.snoozed[it.id] = true; log(`snoozed ${it.type} for 15m`, s); return rerender(); }
  if (act === "ack") { ui.dismissed[it.id] = true; return rerender(); }
  if (act === "move") { ui.moveOpen = true; return go("session", s.id); }
  if (it.type === "perm" && act === "review") {
    const val = ui.choice[it.id] ?? it.p.def;
    ui.confirm = { id: it.id, label: "Send this key to the pane:", cmd: `${via(m)}tmux send-keys -t ${s.target} ${it.p.keys[it.p.options.indexOf(val)]}`, go: "Send", what: `answered permission: ${val}` };
  }
  if (act === "hint") ui.confirm = { id: s.id, label: "Type a hint into the goal session (it keeps its goal):", cmd: `${via(m)}tmux send-keys -t ${s.target} "Start the test DB with make db-up, then retry." Enter`, go: "Send hint", what: "sent a hint" };
  if (act === "reopen") ui.confirm = { id: s.id, label: "Put the goal back to work with the reason from GitHub:", cmd: `${via(m)}tmux send-keys -t ${s.target} "/goal consent ${settings.consent.join(", ")} -- PR #${s.pr} is still open with failing checks; drive it to merged" Enter`, go: "Reopen goal", what: "reopened goal", reopen: true };
  if (act === "do") {
    const c = ui.confirm; log(`${c.what} (mock, not sent)`, s);
    if (it.type === "perm") ui.answered[it.id] = true;
    if (c.reopen) { s.line = `Drive ${s.repo}#${s.issue} to merged`; s.last = new Date(); }
    if (it.type === "stuck") { s.last = new Date(); }
    ui.confirm = null;
  }
  rerender();
}

function triageKey(e) {
  const items = triageItems(), cur = items[ui.triageIdx], k = e.key;
  if (k === "j" || k === "ArrowDown") { ui.triageIdx = Math.min(ui.triageIdx + 1, items.length - 1); ui.confirm = null; rerender(); }
  else if (k === "k" || k === "ArrowUp") { ui.triageIdx = Math.max(ui.triageIdx - 1, 0); ui.confirm = null; rerender(); }
  else if (!cur) return;
  else if (k === "o" && cur.s) triageAct(cur, "open");
  else if (k === "s" && cur.s) triageAct(cur, "snooze");
  else if (k === "a") triageAct(cur, { perm: "review", stuck: "open", verify: "reopen", pace: "ack" }[cur.type]);
}
