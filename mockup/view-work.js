// Work: issues and PRs in watched repos, laid out by lifecycle stage.
const STAGES = [
  ["queued", "Queued", "Open issues nobody is on"],
  ["running", "Running", "An agent is working"],
  ["needs", "Needs you", "Agent is waiting on an answer"],
  ["stuck", "Stuck", "No progress"],
  ["cired", "CI red", "Failing checks, nobody on it"],
  ["review", "In review", "Green or pending, waiting on review"],
];

function renderWorkView() {
  const items = workItems();
  const open = Object.keys(dispatchState).map(k => items.find(it => keyOf(it) === k)).filter(Boolean)[0];
  $("view").innerHTML = `
    <div class="section-head"><h2>Work</h2>
      <span class="hint">${settings.repos.filter(r => r.on).map(r => esc(r.repo)).join(", ") || "no repos watched"} · change in Fleet → Settings</span></div>
    ${open ? `<section class="assign-drawer" aria-label="Assign">
      <div class="tf-head"><span class="mono ref">${esc(open.repo)}#${open.n}</span><span>${open.kind === "pr" ? "Pull request" : "Issue"}</span></div>
      <h3>${esc(open.t)}</h3>${dispatchPanel(open)}</section>` : ""}
    <div class="rules"><span>Rules:</span><span class="rule on">CI red → suggest “Fix failing checks”</span>
      <span class="rule on">New review → suggest “Address review comments”</span><span class="rule off">Auto-start suggested jobs</span>
      <span class="rule on">One agent per branch</span></div>
    <div class="board">${STAGES.map(([id, label, hint]) => {
      const col = items.filter(it => stageOf(it) === id);
      return `<section class="col" aria-label="${label}"><header><span class="col-name">${label}</span><span class="num">${col.length}</span></header>
        <p class="col-hint">${hint}</p>${col.map(workCard).join("") || `<p class="col-empty">None</p>`}</section>`;
    }).join("")}</div>`;
  wireDispatch($("view"), items);
  $("view").querySelectorAll("[data-open]").forEach(b => b.onclick = () => go("session", b.dataset.open));
  $("view").querySelectorAll("[data-job]").forEach(b => b.onclick = () => {
    const it = items.find(x => keyOf(x) === b.dataset.key);
    Object.keys(dispatchState).forEach(k => delete dispatchState[k]);
    openDispatch(it); dispatchState[keyOf(it)].job = b.dataset.job; rerender(); window.scrollTo(0, 0);
  });
  if (ui.workFocus) { document.querySelector(`[data-card="${CSS.escape(ui.workFocus)}"]`)?.scrollIntoView({ block: "center" }); ui.workFocus = null; }
}

function workCard(it) {
  const k = keyOf(it), s = sessionFor(it), st = stageOf(it);
  const ci = it.kind === "pr" ? (it.ci.fail ? `<span class="ci bad">✗ ${it.ci.fail}</span>` : it.ci.pend ? `<span class="ci warn">● ${it.ci.pend} pending</span>` : `<span class="ci ok">✓ ${it.ci.pass}</span>`) : `<span class="kind">issue</span>`;
  let act = "";
  if (s) act = `<button class="link" data-open="${s.id}">${esc(s.agent)} · ${esc(where(s))}</button>`;
  else if (dispatchState[k]) act = `<span class="why">assigning above</span>`;
  else if (it.kind === "issue") act = `<button class="btn sm" data-assign="${esc(k)}">Assign…</button>`;
  else act = `<button class="btn sm" data-key="${esc(k)}" data-job="${st === "cired" ? PR_JOBS[0] : PR_JOBS[1]}">${st === "cired" ? "Fix checks…" : "Address reviews…"}</button>`;
  return `<article class="card${ui.workFocus === k ? " focus" : ""}" data-card="${esc(k)}">
    <div class="card-top"><span class="mono ref">${esc(shortName(it.repo))}#${it.n}</span>${ci}</div>
    <div class="card-t">${esc(it.t)}</div>
    ${it.kind === "pr" ? `<div class="br">${esc(it.branch)}</div>` : ""}
    <div class="card-act">${act}${it.at ? `<span class="ago">${rel(new Date(it.at))}</span>` : ""}</div></article>`;
}
