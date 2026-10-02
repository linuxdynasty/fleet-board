// Triage: only what needs a person. Permission prompts (E6, bound to a prompt_id), stuck goals, harness
// conditions (E12: provider error, rate limit, sign-in required; distinct from stuck), DONE claims the code host
// disagrees with, and accounts that reached the owner's reserve.
const KIND = { perm: ["permission", "bad"], stuck: ["stuck goal", "bad"], cond: ["harness condition", "warn"], verify: ["done? code host disagrees", "warn"], pace: ["reserve reached", "accent"] };

function triageTitle(it) {
  if (it.type === "perm") return it.p.text;
  if (it.type === "stuck") return plainLine(it.s);
  if (it.type === "cond") return `${it.c.kind}${it.c.retrying ? ", retrying" : ""} — ${it.c.text}`;
  if (it.type === "verify") return `${ref(it.s)} says DONE, change request ${crName(it.s)} is still open`;
  return `${it.a.label} is at ${used(it.a)}%; the fleet stopped giving it new work`;
}
const crName = s => { const p = prs.find(x => x.n === s.pr && x.repo === s.repo); return p ? p.display : `#${s.pr}`; };

function renderTriage() {
  const items = triageItems();
  ui.triageIdx = Math.max(0, Math.min(ui.triageIdx, items.length - 1));
  const cur = items[ui.triageIdx];
  $("view").innerHTML = `<div class="triage">
    <section class="tq" aria-label="Triage queue">
      <div class="section-head"><h2>Needs you</h2><span class="hint">j/k move · a act · s snooze · o open</span></div>
      <ol class="tq-list">${items.length ? items.map((it, i) => {
        const [label, tone] = KIND[it.type], t = it.type === "cond" && !it.c.retrying ? "bad" : tone;
        const src = it.s ? `${it.s.harness} · ${where(it.s)} · ${it.s.win}` : `${it.a.harness} account`;
        return `<li><button class="tq-item ${t}" data-ti="${i}" aria-current="${i === ui.triageIdx}">
          <span class="tq-top"><span class="tq-kind">${label}</span><span class="tq-src">${esc(src)}</span>${it.type === "perm" && it.p.destructive ? `<span class="timer">destructive</span>` : ""}</span>
          <span class="tq-title">${esc(triageTitle(it))}</span></button></li>`;
      }).join("") : `<li class="tq-empty">Nothing needs you. Goals are running.</li>`}</ol>
    </section>
    <section class="tf" aria-label="Selected item">${cur ? triageDetail(cur) : `<div class="empty">Queue is clear.</div>`}</section>
  </div>
  <section class="glance" aria-label="Fleet at a glance"><h2>Machines</h2>${machines.map(m => {
    const ss = sessions.filter(s => s.m === m.id && isGoal(s));
    return `<button class="gl-row" data-fleet="${m.id}"><span class="dot ${m.drained ? "warn" : "ok"}"></span><span class="gl-name">${esc(m.name)}</span>
      <span class="gl-slots num">${activeOn(m)}/${m.maxActive} active · ${totalOn(m)}/${m.maxTotal} total${m.drained ? " · draining" : ""}</span>
      <span class="gl-sess">${ss.map(s => `<span class="sdot ${goalState(s)}" title="${esc(s.win + " " + goalState(s))}"></span>`).join("")}</span></button>`;
  }).join("")}</section>`;
  $("view").querySelectorAll("[data-ti]").forEach(b => b.onclick = () => { ui.triageIdx = +b.dataset.ti; ui.confirm = null; rerender(); });
  $("view").querySelectorAll("[data-fleet]").forEach(b => b.onclick = () => { ui.fleetFocus = b.dataset.fleet; go("machines"); });
  if (cur) $("view").querySelectorAll("[data-act]").forEach(b => b.onclick = () => triageAct(cur, b.dataset.act));
  if (cur?.type === "perm") $("view").querySelectorAll(".opt").forEach(b => b.onclick = () => { ui.choice[cur.id] = b.dataset.o; rerender(); });
}

const head = s => `<div class="tf-head">${agentChip(s.harness)}<span>${esc(mById(s.m).name)} · <span class="mono">${esc(s.target)}</span> · ${esc(s.win)}</span>${provChip(s.provider)}</div>`;
const term = (lines, cls = "small") => `<pre class="term ${cls}">${lines.map(([c, t]) => c ? `<span class="${c}">${esc(t)}</span>` : esc(t)).join("\n")}</pre>`;
const confirmBox = () => `<div class="confirm"><div>${esc(ui.confirm.label)}</div>${ui.confirm.note ? `<div class="why">${esc(ui.confirm.note)}</div>` : ""}${cmdBlock(ui.confirm, "cf")}
  <div class="actions"><button class="btn primary ${ui.confirm.danger ? "danger" : ""}" data-act="do">${esc(ui.confirm.go ?? "Send")}</button><button class="btn" data-act="cancel">Cancel</button></div></div>`;

function triageDetail(it) {
  if (it.type === "perm") {
    const p = it.p, pick = ui.choice[p.id] ?? p.def;
    return `${head(it.s)}<h3>${esc(p.text)}</h3>
      <p class="dflt">Default: <b>${esc(p.def)}</b>. ${p.destructive ? `Flagged <b>destructive</b>: ${esc(p.reason)}. Destructive prompts default to Deny and are never auto-answered.` : ""}</p>
      ${dt("tp-" + p.id, `<span>prompt_id ${esc(p.promptId)} · source: ${esc(p.source)} · confidence ${p.confidence}% · answerable: prompt detection verified for ${esc(it.s.harness)}</span>`)}
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
  if (it.type === "cond") {
    const c = it.c, s = it.s;
    return `${head(s)}<h3>${esc(c.kind)}${c.retrying ? " — the harness is retrying on its own" : " — needs you on the machine"}</h3>
      <p class="dflt"><b>${esc(c.kind)}</b>${c.http ? ` · HTTP ${c.http}` : ""}${c.attempt ? ` · attempt ${c.attempt}/${c.max}` : ""}${c.nextRetry && c.retrying ? ` · next retry <b data-until="${+c.nextRetry}">${until(c.nextRetry)}</b>` : ""} · since ${hhmm(c.since)}.
        ${c.retrying ? "Not stuck: the goal is unchanged and the harness will carry on by itself. This clears when the message leaves the screen." : "The board never signs in for you (<code class=\"inline\">/login</code>, <code class=\"inline\">/connect</code> are never sent). Sign in on the machine yourself."}</p>
      <div class="meta-line"><span class="mono">${esc(c.text)}</span>${c.sample ? `<span class="samp">sample</span>` : ""}${dt("tc-" + c.id, "<span>HarnessCondition{kind, retrying, attempt, max_attempts, http_status, next_retry_time, since, cleared}; sign-in commands are COMMAND_CLASS_DENY</span>")}</div>
      ${term(s.pane)}
      <div class="actions"><button class="btn primary" data-act="open">Open session <kbd>a</kbd></button>
        ${c.retrying ? `<button class="btn" data-act="snooze">Watch quietly 15m <kbd>s</kbd></button>` : `<button class="btn" data-act="attachline">Attach from your terminal…</button><button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button>`}</div>
      ${ui.confirm?.id === c.id ? confirmBox() : ""}`;
  }
  if (it.type === "verify") {
    const s = it.s, pr = prs.find(p => p.n === s.pr && p.repo === s.repo);
    return `${head(s)}<h3>${esc(triageTitle(it))}</h3>
      <p class="dflt">The agent wrote <code class="inline">DONE</code>, but ${esc(PROVIDER[pr.provider])} shows ${esc(pr.display)} <b>${esc(pr.state)}</b> with ${pr.ci.fail} failing checks. The worker stays busy until the code host confirms a merge; only then does the session become available for work.</p>
      ${ui.confirm?.id === s.id ? confirmBox() : `<div class="actions"><button class="btn primary" data-act="reopen">Reopen goal… <kbd>a</kbd></button>
        <button class="btn" data-act="open">Open session <kbd>o</kbd></button><button class="btn" data-act="snooze">Snooze 15m <kbd>s</kbd></button></div>`}`;
  }
  const a = it.a, p = pace(a), running = sessions.filter(s => isGoal(s) && s.harness === a.harness && ["working", "waiting", "stuck"].includes(goalState(s))).length;
  return `<div class="tf-head">${agentChip(a.harness)}<span>${esc(a.label)} account</span></div><h3>${esc(triageTitle(it))}</h3>${a.stale ? `<p class="dflt warn-t">This reading is from before the last reset; a re-probe is pending.</p>` : ""}
    <p class="dflt">${p.used}% of the ${esc(bindingBucket(a).label)} weekly limit is used; your reserve starts at ${p.cap}%. Resets in <b data-until="${a.resets}">${until(a.resets)}</b>. ${running} running ${a.harness} goal${running === 1 ? "" : "s"} keep going; new work goes to other accounts.</p>
    <div class="actions"><button class="btn primary" data-act="fleet">Change reserve…</button><button class="btn" data-act="ack">Got it <kbd>a</kbd></button></div>`;
}

function triageAct(it, act) {
  const s = it.s, m = s && mById(s.m);
  if (act === "cancel") { ui.confirm = null; return rerender(); }
  if (act === "open") return go("session", s.id);
  if (act === "fleet") return go("settings");
  if (act === "snooze") { ui.snoozed[it.id] = true; log(`snoozed ${it.type} for 15m`, s); return rerender(); }
  if (act === "ack") { ui.dismissed[it.id] = true; return rerender(); }
  if (act === "move") { ui.moveOpen = true; return go("session", s.id); }
  if (act === "attachline") ui.confirm = { id: it.id, label: "Attach from your own terminal (the only ssh line fleet-board shows; it runs on your machine, never on the hub):", cmd: attachLine(m, s), go: "Copy line", what: "copied attach line", copy: true };
  if (it.type === "perm" && act === "review") {
    const val = ui.choice[it.id] ?? it.p.def, key = it.p.keys[it.p.options.indexOf(val)];
    ui.confirm = { id: it.id, label: `Answer “${val}”: the agent presses ${key} in this prompt and nowhere else.`, cmd: answerCmd(s, it.p, key), go: "Send", what: `answered prompt ${it.p.promptId}: ${val}`,
      note: "If the prompt is gone by then, nothing is typed. If the machine stays offline for 2 minutes, the answer is dropped.", raw: "PROMPT_GONE if the prompt left the screen · not_after_time 2 min → EXPIRED", danger: val !== it.p.def && it.p.destructive };
  }
  if (act === "hint") ui.confirm = { id: s.id, label: "Type a hint into the goal session (it keeps its goal):", cmd: sendKeysCmd(s, "Start the test DB with make db-up, then retry.", {}), go: "Send hint", what: "sent a hint", note: "Typed only when the input line is empty.", raw: "PANE_BUSY otherwise" };
  if (act === "reopen") {
    const g = goalLine(`${crName(s)} is still open with failing checks; drive it to merged`, acctFor(s.harness), s.repo);
    ui.confirm = { id: s.id, label: "Put the goal back to work through the harness's own /goal:", cmd: harnessCmd(s, "/goal", "goal", g.text.replace(/^\/goal /, "")), go: "Reopen goal", what: "reopened goal", reopen: true, note: `The agent will type: ${g.text}`, raw: g.note };
  }
  if (act === "do") {
    const c = ui.confirm; log(`${c.what} (mock, not sent)`, s);
    if (it.type === "perm") ui.answered[it.id] = true;
    if (c.reopen) { s.line = `Drive ${s.display} to merged`; s.last = new Date(); }
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
  else if (k === "a") triageAct(cur, { perm: "review", stuck: "open", cond: "open", verify: "reopen", pace: "ack" }[cur.type]);
}
