// Dispatcher: weekly-usage pacing per account, two slot limits per machine,
// and goal dispatch. Pure functions first, UI panel after.
const HOUR = 3600e3, DAY = 24 * HOUR;
const acctFor = h => accounts.find(a => a.harness === h);
const used = a => Math.max(...a.buckets.map(b => b.used));
const bindingBucket = a => a.buckets.reduce((p, b) => (b.used > p.used ? b : p));
const hoursLeft = a => Math.max(0, (a.resets - Date.now()) / HOUR);

// Burn-down target: share of the window elapsed × usable share (100 − reserve).
function pace(a) {
  const start = a.resets - a.windowDays * DAY;
  const frac = Math.min(1, Math.max(0, (Date.now() - start) / (a.windowDays * DAY)));
  const cap = 100 - a.reserve, u = used(a), target = frac * cap;
  const lastDay = hoursLeft(a) <= 24;
  let state;
  if (u >= cap) state = "reserve";
  else if (u > target + 5 && !lastDay) state = "ahead";
  else if (u < target - 5 || lastDay) state = "behind";
  else state = "on";
  // What would be wasted: usable quota left per hour until reset.
  const waste = (cap - u) / Math.max(1, hoursLeft(a));
  return { target, cap, used: u, frac, state, lastDay, waste };
}
const PACE_LABEL = { reserve: "reserve reached", ahead: "ahead of pace", on: "on pace", behind: "behind pace" };
const dispatchable = a => ["on", "behind"].includes(pace(a).state);

// Goal states come from the first line of the goal file.
function goalState(s) {
  if (!s.line) return "none";
  if (s.line.startsWith("DONE")) return verified(s) ? "merged" : "verifying";
  if (s.line.startsWith("BLOCKED")) return s.watchAlive ? "waiting" : "stuck";
  return "working";
}
function verified(s) {
  const pr = prs.find(p => p.n === s.pr);
  return !s.pr ? false : pr?.state === "merged";
}
const isGoal = s => s.harness !== "shell";
const activeOn = m => sessions.filter(s => s.m === m.id && isGoal(s) && ["working", "stuck"].includes(goalState(s))).length;
const totalOn = m => sessions.filter(s => s.m === m.id && isGoal(s) && !["merged", "none"].includes(goalState(s))).length;
const freeActive = m => m.drained ? 0 : Math.max(0, m.maxActive - activeOn(m));
const freeTotal = m => m.drained ? 0 : Math.max(0, m.maxTotal - totalOn(m));

const watched = repo => settings.repos.some(r => r.on && r.repo === repo);
const shortName = repo => repo.split("/")[1];
const keyOf = it => `${it.repo}#${it.n}`;
const claimedByFleet = it => sessions.some(s => s.repo === it.repo && (s.issue === it.n || s.pr === it.n) && goalState(s) !== "merged");
const readyIssues = () => backlog.filter(i => watched(i.repo) && !i.claim && !claimedByFleet(i));

// Pick account first (most quota that would otherwise expire), then machine.
function suggest() {
  const accts = accounts.filter(dispatchable).sort((x, y) => pace(y).waste - pace(x).waste);
  const ms = machines.filter(m => !m.drained && freeActive(m) > 0 && freeTotal(m) > 0)
    .sort((x, y) => (!!x.hub - !!y.hub) || (freeActive(y) - freeActive(x)));
  for (const a of accts) for (const m of ms) if (m.harnesses.includes(a.harness)) return { m: m.id, a: a.id };
  return null;
}

function reasons(it, st) {
  const m = mById(st.m), a = accounts.find(x => x.id === st.a), p = pace(a), out = [];
  if (it.claim) out.push(["bad", `Claimed by ${it.claim}. Never dispatched.`]);
  if (m.drained) out.push(["bad", `${m.name} is draining.`]);
  else if (!m.harnesses.includes(a.harness)) out.push(["bad", `${a.label} is not installed on ${m.name}.`]);
  else if (freeActive(m) === 0) out.push(["bad", `${m.name} has no free active slot (${activeOn(m)}/${m.maxActive}).`]);
  else if (freeTotal(m) === 0) out.push(["bad", `${m.name} is at its total limit (${totalOn(m)}/${m.maxTotal}).`]);
  else out.push(["ok", `${m.name}: ${freeActive(m)}/${m.maxActive} active and ${freeTotal(m)}/${m.maxTotal} total free · ${m.ram} GB.`]);
  const txt = `${a.label}: ${p.used}% used (${bindingBucket(a).label}), pace line ${Math.round(p.target)}%, reserve at ${p.cap}%, resets in ${until(a.resets)}.`;
  out.push([p.state === "reserve" ? "bad" : p.state === "ahead" ? "warn" : "ok", `${txt} ${PACE_LABEL[p.state]}.`]);
  accounts.filter(x => x.id !== a.id && !dispatchable(x)).forEach(x => out.push(["dim", `${x.label} skipped: ${PACE_LABEL[pace(x).state]}.`]));
  return out;
}

function goalCmd(it, st) {
  const m = mById(st.m), a = accounts.find(x => x.id === st.a), short = shortName(it.repo);
  const checkout = `${m.repoRoot}/${short}`; // the goal's issue driver creates its own worktree
  const goal = `/goal consent ${settings.consent.join(", ")} -- drive ${it.repo}#${it.n} to merged`;
  const via = m.hub ? "" : `ssh ${m.user}@${m.ip} `;
  return `${via}tmux new-window -d -t ${m.tmux} -n ${short}-${it.n} -c ${checkout} ${a.harness}\n# then, once the harness is up:\n${via}tmux send-keys -t ${m.tmux}:${short}-${it.n} "${goal}" Enter`;
}

const dispatchState = {};
function openDispatch(it) {
  Object.keys(dispatchState).forEach(k => delete dispatchState[k]);
  const pick = suggest() ?? { m: machines[0].id, a: accounts[0].id };
  dispatchState[keyOf(it)] = pick;
}

function dispatchPanel(it) {
  const k = keyOf(it), st = dispatchState[k], id = k.replace(/[^a-z0-9]/gi, "_");
  const rs = reasons(it, st), blocked = rs.some(r => r[0] === "bad");
  return `<div class="disp-panel" data-k="${esc(k)}">
    <div class="disp">
      <label>Account <select data-f="a" id="da-${id}">${accounts.map(a => `<option value="${a.id}" ${a.id === st.a ? "selected" : ""}>${a.label} (${PACE_LABEL[pace(a).state]})</option>`).join("")}</select></label>
      <label>Machine <select data-f="m" id="dm-${id}">${machines.map(m => `<option value="${m.id}" ${m.id === st.m ? "selected" : ""}>${esc(m.name)} (${freeActive(m)}/${m.maxActive} active free)</option>`).join("")}</select></label>
    </div>
    <ul class="why-list">${rs.map(([c, t]) => `<li class="${c}">${esc(t)}</li>`).join("")}</ul>
    <code class="cmd">${esc(goalCmd(it, st))}</code>
    <div class="actions"><button class="btn primary" data-start="${esc(k)}" ${blocked ? "disabled" : ""}>Dispatch goal</button>
      <button class="btn" data-cancel="${esc(k)}">Cancel</button><span class="why">Mockup: adds a sample goal, runs nothing.</span></div>
  </div>`;
}

function wireDispatch(root, items) {
  const find = k => items.find(it => keyOf(it) === k);
  root.querySelectorAll("[data-assign]").forEach(b => b.onclick = () => { openDispatch(find(b.dataset.assign)); rerender(); });
  root.querySelectorAll("[data-cancel]").forEach(b => b.onclick = () => { delete dispatchState[b.dataset.cancel]; rerender(); });
  root.querySelectorAll(".disp-panel select").forEach(s => s.onchange = () => { dispatchState[s.closest(".disp-panel").dataset.k][s.dataset.f] = s.value; rerender(); });
  root.querySelectorAll("[data-start]").forEach(b => b.onclick = () => startGoal(find(b.dataset.start), "you"));
}

function startGoal(it, who) {
  const k = keyOf(it), st = dispatchState[k] ?? suggest();
  if (!st) return;
  const m = mById(st.m), a = accounts.find(x => x.id === st.a), short = shortName(it.repo), id = "d" + it.n;
  const s = { id, m: m.id, harness: a.harness, target: `${m.tmux}:${short}-${it.n}`, repo: it.repo,
    issue: it.kind === "issue" ? it.n : null, pr: it.kind === "pr" ? it.n : null, branch: it.branch ?? "(new branch)",
    line: `Drive ${it.repo}#${it.n} to merged`, last: new Date(), started: new Date(),
    tasks: [[`Claim #${it.n}`, 0]], pane: goalCmd(it, st).split("\n").map(l => ["dim", l]).concat([["hl", "Starting goal…"]]) };
  sessions.push(s);
  log(`${who === "you" ? "dispatched" : "auto-dispatched"} ${short}#${it.n} to ${a.harness} (mock, nothing ran)`, s, who);
  delete dispatchState[k];
  if (who === "you") go("session", id);
}
