// Dispatcher: assigns an agent on a machine to an issue or a PR.
// Rules: machine needs tmux + the agent and a free slot; agent needs at least
// settings.headroomMin% left in its tightest usage window; unknown usage is never
// auto-picked; one agent per PR branch. Prefer non-hub machines, then most free slots.
const headroom = a => a.windows.length ? 100 - Math.max(...a.windows.map(w => w.used)) : null;
const tightest = a => a.windows.reduce((p, w) => (w.used > p.used ? w : p));
const agentIds = () => agents.map(a => a.id);
const busy = m => sessions.filter(s => s.m === m.id && agentIds().includes(s.agent) && s.state !== "done").length;
const free = m => m.drained ? 0 : Math.max(0, m.slots - busy(m));
const watched = repo => settings.repos.some(r => r.on && r.repo === repo);
const shortName = repo => repo.split("/")[1];
const keyOf = it => `${it.repo}#${it.n}`;

const PR_JOBS = ["Fix failing checks", "Address review comments", "Rebase on main and resolve conflicts", "Continue the work on this branch"];
const defaultJob = pr => pr.ci.fail ? PR_JOBS[0] : PR_JOBS[1];
const onBranch = pr => sessions.find(s => s.branch === pr.branch && s.state !== "done");

function suggest() {
  const ms = machines.filter(m => m.tools.tmux && !m.drained && free(m) > 0)
    .sort((a, b) => (!!a.hub - !!b.hub) || (free(b) - free(a)));
  const as = agents.filter(a => headroom(a) !== null && headroom(a) >= settings.headroomMin)
    .sort((a, b) => headroom(b) - headroom(a));
  for (const m of ms) for (const a of as) if (m.tools[a.id]) return { m: m.id, a: a.id };
  return null;
}

function reasons(it, st) {
  const m = machines.find(x => x.id === st.m), a = agents.find(x => x.id === st.a), out = [];
  if (it.kind === "pr") {
    const s = onBranch(it);
    if (s) out.push(["bad", `${s.agent} on ${mById(s.m).name} ${s.target} is already on ${it.branch}. One agent per branch.`]);
  }
  if (!m.tools.tmux) out.push(["bad", `${m.name} has no tmux yet.`]);
  else if (!m.tools[st.a]) out.push(["bad", `${a.label} is not installed on ${m.name}.`]);
  else if (m.drained) out.push(["bad", `${m.name} is draining: it finishes current work and takes nothing new.`]);
  else if (free(m) === 0) out.push(["bad", `${m.name} is full (${busy(m)} of ${m.slots} slots busy).`]);
  else out.push(["ok", `${m.name}: ${free(m)} of ${m.slots} slots free · ${m.ram} GB${m.hub ? " · hub, others preferred" : ""}.`]);
  const h = headroom(a);
  if (h === null) out.push(["warn", `${a.label} usage is unknown, so it is never picked automatically.`]);
  else if (h < settings.headroomMin) out.push(["warn", `${a.label} has ${h}% left in its ${tightest(a).label} window (under ${settings.headroomMin}%).`]);
  else out.push(["ok", `${a.label} has ${h}% left in its ${tightest(a).label} window.`]);
  agents.filter(x => x.id !== st.a && headroom(x) !== null && headroom(x) < settings.headroomMin)
    .forEach(x => out.push(["dim", `${x.label} skipped: ${headroom(x)}% left.`]));
  return out;
}

function launchCmd(it, st) {
  const m = machines.find(x => x.id === st.m), short = shortName(it.repo);
  const prompt = it.kind === "pr"
    ? `${st.job} on ${it.repo}#${it.n}. Check out ${it.branch} in a new worktree. Post questions with fleet ask.`
    : `Work on ${it.repo}#${it.n} in a new git worktree. Post questions with fleet ask.`;
  const via = m.hub ? "" : `ssh ${m.user}@${m.ip} `;
  return `${via}tmux new-window -d -t ${m.tmux} -n ${short}-${it.n} -c ${m.repoRoot}/${short} "${st.a} '${prompt}'"`;
}

const dispatchState = {}; // "repo#n" -> { m, a, job }

function openDispatch(it) {
  const pick = suggest() ?? { m: machines[0].id, a: agents[0].id };
  dispatchState[keyOf(it)] = { ...pick, job: it.kind === "pr" ? defaultJob(it) : null };
}

// Panel shown under an issue or PR row while it is being assigned.
function dispatchPanel(it) {
  const k = keyOf(it), st = dispatchState[k], id = k.replace(/[^a-z0-9]/gi, "_");
  const rs = reasons(it, st), blocked = rs.some(r => r[0] === "bad");
  return `<div class="disp-panel" data-k="${esc(k)}">
    <div class="disp">
      ${it.kind === "pr" ? `<label>Job <select data-f="job" id="dj-${id}">${PR_JOBS.map(j => `<option ${j === st.job ? "selected" : ""}>${j}</option>`).join("")}</select></label>` : ""}
      <label>Machine <select data-f="m" id="dm-${id}">${machines.map(m => `<option value="${m.id}" ${m.id === st.m ? "selected" : ""}>${esc(m.name)} (${free(m)}/${m.slots} free)</option>`).join("")}</select></label>
      <label>Agent <select data-f="a" id="da-${id}">${agents.map(a => `<option value="${a.id}" ${a.id === st.a ? "selected" : ""}>${a.label} (${headroom(a) ?? "?"}% left)</option>`).join("")}</select></label>
    </div>
    <ul class="why-list">${rs.map(([c, t]) => `<li class="${c}">${esc(t)}</li>`).join("")}</ul>
    <code class="cmd">${esc(launchCmd(it, st))}</code>
    <div class="actions"><button class="btn primary" data-start="${esc(k)}" ${blocked ? "disabled" : ""}>Start session</button>
      <button class="btn" data-cancel="${esc(k)}">Cancel</button><span class="why">Mockup: adds a sample row, runs nothing.</span></div>
  </div>`;
}

function wireDispatch(root, items) {
  const find = k => items.find(it => keyOf(it) === k);
  root.querySelectorAll("[data-assign]").forEach(b => b.onclick = () => { Object.keys(dispatchState).forEach(k => delete dispatchState[k]); openDispatch(find(b.dataset.assign)); rerender(); });
  root.querySelectorAll("[data-cancel]").forEach(b => b.onclick = () => { delete dispatchState[b.dataset.cancel]; rerender(); });
  root.querySelectorAll(".disp-panel select").forEach(s => s.onchange = () => {
    dispatchState[s.closest(".disp-panel").dataset.k][s.dataset.f] = s.value; rerender();
  });
  root.querySelectorAll("[data-start]").forEach(b => b.onclick = () => startSession(find(b.dataset.start)));
}

function startSession(it) {
  const k = keyOf(it), st = dispatchState[k], m = mById(st.m), short = shortName(it.repo), id = "d" + it.n;
  const s = { id, m: st.m, agent: st.a, target: `${m.tmux}:${short}-${it.n}`, repo: short, ref: k,
    branch: it.kind === "pr" ? it.branch : "(new worktree)", task: it.kind === "pr" ? `${st.job} · #${it.n}` : `#${it.n} ${it.t}`,
    state: "working", last: new Date(), started: new Date(), tasks: [],
    pane: [["dim", `$ ${launchCmd(it, st)}`], ["", ""], ["hl", `Starting ${st.a} on ${it.repo}#${it.n}…`]] };
  sessions.push(s);
  log(`assigned ${st.a} to ${short}#${it.n}${it.kind === "pr" ? " · " + st.job : ""} (mock, nothing ran)`, s);
  delete dispatchState[k];
  go("session", id);
}
