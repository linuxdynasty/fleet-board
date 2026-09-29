// Work: issues and PRs by goal stage, plus what auto-dispatch will do next.
const STAGES = [
  ["queued", "Queued", "Open, unclaimed or claimed elsewhere"],
  ["working", "Working", "Goal is running"],
  ["waiting", "Waiting", "Goal blocked on CI or review, watcher armed"],
  ["stuck", "Stuck", "Blocked, no watcher"],
  ["verifying", "Verifying", "Goal says DONE, GitHub not yet merged"],
  ["merged", "Merged", "Confirmed on GitHub"],
  ["unowned", "PRs with no goal", "Open PRs nobody is driving"],
];

function nextUp() {
  const issue = readyIssues()[0], pick = suggest();
  if (!issue) return { text: "Nothing ready: every watched issue is claimed or running." };
  if (!pick) return { text: `Next is ${shortName(issue.repo)}#${issue.n}, but no account is under its pace line with a free machine.`, issue };
  const a = accounts.find(x => x.id === pick.a), m = mById(pick.m);
  return { issue, pick, text: `${shortName(issue.repo)}#${issue.n} → ${a.label} on ${m.name}`,
    why: `${a.label} is ${PACE_LABEL[pace(a).state]} (${pace(a).used}% used, line ${Math.round(pace(a).target)}%, resets in ${until(a.resets)}); ${m.short} has ${freeActive(m)} active slot${freeActive(m) === 1 ? "" : "s"} free.` };
}

function renderWorkView() {
  const items = workItems(), nu = nextUp();
  const open = items.find(it => dispatchState[keyOf(it)]);
  const cols = STAGES.filter(([id]) => id !== "unowned" || items.some(it => stageOf(it) === "unowned"));
  $("view").innerHTML = `
    <div class="section-head"><h2>Work</h2><span class="hint">${settings.repos.filter(r => r.on).map(r => esc(r.repo)).join(", ") || "no repos watched"} · goals get consent: ${esc(settings.consent.join(", "))}</span></div>
    <section class="nextup" aria-label="Next up">
      <div><span class="nu-k">${settings.autoDispatch ? "Auto-dispatch will start next" : "Auto-dispatch is off. Suggested next"}</span>
        <b class="nu-t">${esc(nu.text)}</b>${nu.why ? `<span class="why">${esc(nu.why)}</span>` : ""}</div>
      <div class="actions">${nu.pick ? `<button class="btn primary" id="nuGo">Dispatch now</button>` : ""}
        <button class="btn" id="nuToggle">${settings.autoDispatch ? "Pause auto-dispatch" : "Turn auto-dispatch on"}</button></div>
    </section>
    ${open ? `<section class="assign-drawer" aria-label="Dispatch"><div class="tf-head"><span class="mono ref">${esc(open.repo)}#${open.n}</span></div>
      <h3>${esc(open.t)}</h3>${dispatchPanel(open)}</section>` : ""}
    <div class="board" style="grid-template-columns: repeat(${cols.length}, minmax(190px, 1fr))">${cols.map(([id, label, hint]) => {
      const col = items.filter(it => stageOf(it) === id);
      return `<section class="col" aria-label="${label}"><header><span class="col-name">${label}</span><span class="num">${col.length}</span></header>
        <p class="col-hint">${hint}</p>${col.map(workCard).join("") || `<p class="col-empty">None</p>`}</section>`;
    }).join("")}</div>`;
  wireDispatch($("view"), items);
  $("view").querySelectorAll("[data-open]").forEach(b => b.onclick = () => go("session", b.dataset.open));
  $("nuToggle").onclick = () => { settings.autoDispatch = !settings.autoDispatch; log(`turned auto-dispatch ${settings.autoDispatch ? "on" : "off"}`); rerender(); };
  if ($("nuGo")) $("nuGo").onclick = () => { dispatchState[keyOf(nu.issue)] = nu.pick; startGoal(nu.issue, "you"); };
  if (ui.workFocus) { document.querySelector(`[data-card="${CSS.escape(ui.workFocus)}"]`)?.scrollIntoView({ block: "center" }); ui.workFocus = null; }
}

function workCard(it) {
  const k = keyOf(it), s = goalFor(it) ?? (it.kind === "issue" ? sessions.find(x => x.repo === it.repo && x.issue === it.n) : null), st = stageOf(it);
  const ci = it.kind === "pr" ? (it.state === "merged" ? `<span class="ci ok">merged ${hhmm(it.mergedAt)}</span>` : it.ci.fail ? `<span class="ci bad">✗ ${it.ci.fail}</span>` : it.ci.pend ? `<span class="ci warn">● ${it.ci.pend}</span>` : `<span class="ci ok">✓ ${it.ci.pass}</span>`)
    : `<span class="kind">issue${it.size ? " · " + it.size : ""}</span>`;
  let act = "";
  if (s) act = `<button class="link" data-open="${s.id}">${esc(s.harness)} · ${esc(where(s))}</button>`;
  else if (it.claim) act = `<span class="claim">claimed by ${esc(it.claim)}</span>`;
  else if (st === "queued" && !dispatchState[k]) act = `<button class="btn sm" data-assign="${esc(k)}">Dispatch…</button>`;
  else if (st === "unowned" && !dispatchState[k]) act = `<button class="btn sm" data-assign="${esc(k)}">Drive to merge…</button>`;
  const closes = it.kind === "pr" && it.issues.length ? `<div class="br">closes ${it.issues.map(n => "#" + n).join(", ")}</div>` : "";
  return `<article class="card${ui.workFocus === k ? " focus" : ""}" data-card="${esc(k)}">
    <div class="card-top"><span class="mono ref">${esc(shortName(it.repo))}#${it.n}</span>${ci}</div>
    <div class="card-t">${esc(it.t)}</div>${closes}
    ${s?.line && st !== "working" ? `<div class="goal-line">${esc(s.line)}</div>` : ""}
    <div class="card-act">${act}</div></article>`;
}
