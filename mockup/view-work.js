// Work: issues, tickets and change requests by goal stage (provider-neutral, ADR 0012), plus what
// auto-dispatch will do next (StartGoal, G3), including whether it reuses an available session (E11).
// Columns are WorkItem.stage (work.proto); the chip inside a Working card is the goal's own phase. A cleared goal's item
// goes to Manual only (it is never redispatched by itself) and can be put back; an untrusted item waits for a label.
const STAGES = [
  ["ready", "Ready", "Trusted and unclaimed: auto-dispatch may take it"],
  ["manual", "Manual only", "Not started by itself; the reason is on the card"],
  ["claimed", "Claimed elsewhere", "Someone else claimed it"],
  ["running", "Working", "A goal is running; the chip says what it is doing"],
  ["verifying", "Verifying", "Goal says DONE, code host not yet merged"],
  ["closed", "Closed", "Merged or closed on the code host"],
  ["unowned", "Change requests with no goal", "Open PRs/MRs nobody is driving"],
];

function nextUp() {
  const issue = readyIssues()[0], pick = issue && suggest(issue);
  if (!issue) return { text: "Nothing ready: every watched issue is claimed or running." };
  if (!pick) return { text: `Next is ${issue.display}, but no account is under its pace line with a free machine.`, issue };
  const a = accounts.find(x => x.id === pick.a), m = mById(pick.m), reuse = availableFor(m, a.harness, issue.repo);
  return { issue, pick, text: `${issue.display} → ${a.label} on ${m.name}`,
    why: `${a.label} is ${PACE_LABEL[pace(a).state]} (${pace(a).used}% used, line ${Math.round(pace(a).target)}%, resets in ${until(a.resets)}); ${m.short} has ${freeActive(m)} active slot${freeActive(m) === 1 ? "" : "s"} free.`,
    how: reuse ? `will use available session ${reuse.win} on ${m.short}` : `new window ${a.harness} ${issue.display} in session fleet on ${m.short}` };
}

function renderWorkView() {
  const items = workItems(), nu = nextUp();
  const open = items.find(it => dispatchState[keyOf(it)]);
  const cols = STAGES.filter(([id]) => !["unowned", "claimed"].includes(id) || items.some(it => stageOf(it) === id));
  const avail = sessions.filter(s => liveState(s) === "available").map(s => { const a = acctFor(s.harness); return `${s.win} on ${mById(s.m).short} (${a && dispatchable(a) ? "usable" : a ? a.label + " " + PACE_LABEL[pace(a).state] : "manual only"})`; });
  $("view").innerHTML = `
    <div class="section-head"><h2>Work</h2><span class="hint">${settings.repos.filter(r => r.on).map(r => `${esc(r.repo)} <span class="prov ${esc(r.provider)}">${esc(PROVIDER[r.provider])}</span>`).join(", ") || "no repos watched"} · goals get consent: ${esc(settings.consent.join(", "))}</span></div>
    <section class="nextup" aria-label="Next up">
      <div><span class="nu-k">${settings.autoDispatch ? "Auto-dispatch will start next" : "Auto-dispatch is off. Suggested next"}</span>
        <b class="nu-t">${esc(nu.text)}</b>${nu.why ? `<span class="why">${esc(nu.why)}</span>` : ""}${nu.how ? `<span class="why">${esc(nu.how)} · available sessions: ${esc(avail.join(", ") || "none")}</span>` : ""}</div>
      <div class="actions">${nu.pick ? `<button class="btn primary" id="nuGo">Dispatch now</button>` : ""}
        <button class="btn" id="nuToggle">${settings.autoDispatch ? "Pause auto-dispatch" : "Turn auto-dispatch on"}</button></div>
    </section>
    ${open ? `<section class="assign-drawer" aria-label="Dispatch"><div class="tf-head"><span class="mono ref">${esc(open.display)}</span>${provChip(open.provider)}<span class="why">${esc(open.host)}/${esc(open.repo)}</span></div>
      <h3>${esc(open.t)}</h3>${dispatchPanel(open)}</section>` : ""}
    <div class="board" style="grid-template-columns: repeat(${cols.length}, minmax(190px, 1fr))">${cols.map(([id, label, hint]) => {
      const col = items.filter(it => stageOf(it) === id);
      return `<section class="col" aria-label="${label}"><header><span class="col-name">${label}</span><span class="num">${col.length}</span></header>
        <p class="col-hint">${hint}</p>${col.map(workCard).join("") || `<p class="col-empty">None</p>`}</section>`;
    }).join("")}</div>`;
  wireDispatch($("view"), items);
  $("view").querySelectorAll("[data-open]").forEach(b => b.onclick = () => go("session", b.dataset.open));
  $("view").querySelectorAll("[data-requeue]").forEach(b => b.onclick = () => { const s = sById(b.dataset.requeue); s.requeued = true; log(`put ${s.display} back in the queue (mock)`, s); rerender(); });
  $("nuToggle").onclick = () => { settings.autoDispatch = !settings.autoDispatch; log(`turned auto-dispatch ${settings.autoDispatch ? "on" : "off"}`); rerender(); };
  if ($("nuGo")) $("nuGo").onclick = () => { dispatchState[keyOf(nu.issue)] = nu.pick; startGoal(nu.issue, "you"); };
  if (ui.workFocus) { document.querySelector(`[data-card="${CSS.escape(ui.workFocus)}"]`)?.scrollIntoView({ block: "center" }); ui.workFocus = null; }
}

function workCard(it) {
  const k = keyOf(it), s = goalFor(it) ?? (it.kind === "issue" ? sessions.find(x => isGoal(x) && x.repo === it.repo && x.issue === it.n) : null), st = stageOf(it);
  const ci = it.kind === "pr" ? (it.state === "merged" ? `<span class="ci ok">merged ${hhmm(it.mergedAt)}</span>` : it.ci.fail ? `<span class="ci bad">✗ ${it.ci.fail}</span>` : it.ci.pend ? `<span class="ci warn">● ${it.ci.pend}</span>` : `<span class="ci ok">✓ ${it.ci.pass}</span>`)
    : `<span class="kind">${esc(it.kindLabel ?? "issue")}${it.size ? " · " + esc(it.size) : ""}</span>`;
  let act = "";
  if (s && st !== "manual") act = `<button class="link" data-open="${s.id}">${esc(s.win)} · ${esc(where(s))}</button>`;
  else if (it.claim) act = `<span class="claim">claimed by ${esc(it.claim)}</span>`;
  else if (st === "ready" && !dispatchState[k]) act = `<button class="btn sm" data-assign="${esc(k)}">Dispatch…</button>`;
  else if (st === "manual") act = `<span class="claim">${esc(manualReason(it, s))}</span>${s?.stopped && !s.requeued ? ` <button class="btn sm" data-requeue="${esc(s.id)}">Put it back in the queue</button>` : ""}`;
  else if (st === "unowned" && !dispatchState[k]) act = `<button class="btn sm" data-assign="${esc(k)}">Drive to merge…</button>`;
  const closes = it.kind === "pr" && it.issues.length ? `<div class="br">closes ${it.issues.map(n => "#" + esc(n)).join(", ")}</div>` : "";
  const target = it.targetRepo ? `<div class="br">code: ${esc(it.targetRepo)}</div>` : "";
  return `<article class="card${ui.workFocus === k ? " focus" : ""}" data-card="${esc(k)}">
    <div class="card-top"><span class="mono ref">${esc(it.display)}</span>${provChip(it.provider)}${it.sample ? `<span class="samp">sample</span>` : ""}${ci}</div>
    <div class="card-t">${esc(it.t)}</div>${closes}${target}
    ${s?.line && !s.stopped && goalState(s) !== "working" ? `<div class="goal-line">${esc(plainLine(s))} ${dt("wk-" + s.id, `<span>goal file, first line: ${esc(s.line)}</span>`)}</div>` : ""}
    <div class="card-act">${act}</div></article>`;
}
