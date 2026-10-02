// Dispatcher: weekly-usage pacing per account, two slot limits per machine, available sessions (E11)
// and goal dispatch as a StartGoal command (G3). Pure functions first, UI panel after.
const HOUR = 3600e3, DAY = 24 * HOUR;
const acctFor = h => accounts.find(a => a.harness === h);
const anyAcct = id => accounts.find(a => a.id === id) ?? apiAccounts.find(a => a.id === id);
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
  const waste = (cap - u) / Math.max(1, hoursLeft(a));
  return { target, cap, used: u, frac, state, lastDay, waste };
}
const PACE_LABEL = { reserve: "reserve reached", ahead: "ahead of pace", on: "on pace", behind: "behind pace" };
const dispatchable = a => a.mode === "fleet" && ["on", "behind"].includes(pace(a).state);

// A reset that already passed (relative to the mock clock) rolls forward by whole windows. The reading was taken
// before that reset, so it is marked stale until the next probe; it never shows "resets in now".
function rollResets(now = Date.now()) {
  accounts.forEach(a => { while (a.resets <= now) { a.resets += a.windowDays * DAY; a.stale = true; } });
}
rollResets();

// Waste outlook: will this subscription leave quota unused when the window resets? Simple linear projection:
//   burn rate     = % used ÷ hours elapsed in the window (average so far)
//   projected use = % used + burn rate × hours left (capped at 100)
//   unused        = usable share (100 − reserve) − projected use, floored at 0
// The reserve is left out on purpose: it is kept for you, so leaving it unused is not waste.
// An account "wastes" when ≥ WASTE_MIN points would go unused; it is "idle" when no goal runs on it while it is
// behind pace. Stale readings get no projection.
const WASTE_MIN = 5;
const runningOn = h => sessions.filter(s => isGoal(s) && s.harness === h && goalState(s) === "working").length;
function outlook(a) {
  const p = pace(a), elapsedH = Math.max(1, p.frac * a.windowDays * 24), rate = p.used / elapsedH;
  const projected = Math.min(100, p.used + rate * hoursLeft(a)), unused = Math.max(0, Math.round(p.cap - projected));
  const running = runningOn(a.harness), wastes = !a.stale && p.state !== "reserve" && unused >= WASTE_MIN;
  const idle = !a.stale && running === 0 && p.state === "behind";
  return { p, left: 100 - p.used, unused, running, wastes, idle, alert: wastes || idle };
}
const goalsTxt = n => `${n} goal${n === 1 ? "" : "s"} running`;
// One plain line per subscription account (Usage headline); `short` is the summary-strip version.
function wasteLine(a, short) {
  const o = outlook(a), p = o.p;
  if (a.stale) return short ? "stale reading" : `stale — reset passed, re-probe pending · last read ${o.left}% left · next reset in ${until(a.resets)} · ${goalsTxt(o.running)}`;
  const fate = p.state === "reserve" ? "reserve reached, new work goes to other accounts" : o.unused ? `on pace to leave ~${o.unused}% unused` : "on pace to use it all";
  if (short) return `${o.left}% left${p.state === "reserve" ? " · reserve reached" : o.unused ? ` · ~${o.unused}% unused` : ""}${o.idle ? " · no goals" : ""}`;
  return `${o.left}% left · resets in ${until(a.resets)} · ${goalsTxt(o.running)} · ${fate}${o.idle ? " · nothing running while behind pace" : ""}`;
}
const spendLine = a => `$${a.spentUsd.toFixed(2)} of $${a.capUsd} this ${a.period} · manual only`;

// Goal states come from the first line of the goal file (G2).
function goalState(s) {
  if (!s.line) return "none";
  if (s.line.startsWith("DONE")) return verified(s) ? "merged" : "verifying";
  if (s.line.startsWith("BLOCKED")) return s.watchAlive ? "waiting" : "stuck";
  return "working";
}
function verified(s) {
  const pr = prs.find(p => p.n === s.pr && p.repo === s.repo);
  return !s.pr ? false : pr?.state === "merged";
}
const isGoal = s => s.harness !== "shell" && !s.yours;
const activeOn = m => sessions.filter(s => s.m === m.id && isGoal(s) && ["working", "stuck"].includes(goalState(s))).length;
const totalOn = m => sessions.filter(s => s.m === m.id && isGoal(s) && !["merged", "none"].includes(goalState(s))).length;
const freeActive = m => m.drained ? 0 : Math.max(0, m.maxActive - activeOn(m));
const freeTotal = m => m.drained ? 0 : Math.max(0, m.maxTotal - totalOn(m));

// E11: a session is available for work when fleet started it and its goal is verified merged, or the developer
// marked it on the board. Idle alone never makes a session take work.
const availableFor = (m, harness) => sessions.find(s => s.m === m.id && s.harness === harness && s.idle !== false &&
  ((s.yours && s.available) || (!s.yours && goalState(s) === "merged" && !s.stopped)));

const watched = repo => settings.repos.some(r => r.on && r.repo === repo);
const shortName = repo => repo.split("/")[1] ?? repo;
const keyOf = it => `${it.provider}:${it.repo}#${it.n}`;
const claimedByFleet = it => sessions.some(s => isGoal(s) && s.repo === it.repo && (s.issue === it.n || s.pr === it.n) && goalState(s) !== "merged");
const readyIssues = () => backlog.filter(i => watched(i.repo) && !i.claim && !claimedByFleet(i));

// Pick account first (most quota that would otherwise expire), then a machine with an available session for that
// harness, then any machine with free slots. API-key accounts never enter this loop (manual only).
function suggest() {
  const accts = accounts.filter(dispatchable).sort((x, y) => pace(y).waste - pace(x).waste);
  const ms = machines.filter(m => !m.drained && freeActive(m) > 0 && freeTotal(m) > 0)
    .sort((x, y) => (!!x.hub - !!y.hub) || (freeActive(y) - freeActive(x)));
  for (const a of accts) {
    const withSess = ms.find(m => availableFor(m, a.harness));
    if (withSess) return { m: withSess.id, a: a.id };
    for (const m of ms) if (m.harnesses.includes(a.harness)) return { m: m.id, a: a.id };
  }
  return null;
}

function reasons(it, st) {
  const m = mById(st.m), a = anyAcct(st.a), out = [];
  if (it.claim) out.push(["bad", `Claimed by ${it.claim}. Never dispatched.`]);
  if (a.harness === "opencode") out.push(["bad", `OpenCode has no /goal yet: it needs fleet-board's generic goal protocol. Until then: inventory, sessions, attach.`]);
  if (a.capUsd !== undefined) out.push(["warn", `${a.label}: API key, $${a.spentUsd.toFixed(2)} of $${a.capUsd} cap this ${a.period}. Manual dispatch only; never auto.`]);
  if (m.drained) out.push(["bad", `${m.name} is draining.`]);
  else if (!m.harnesses.includes(a.harness)) out.push(["bad", `${a.harness} is not installed on ${m.name}.`]);
  else if (freeActive(m) === 0) out.push(["bad", `${m.name} has no free active slot (${activeOn(m)}/${m.maxActive}).`]);
  else if (freeTotal(m) === 0) out.push(["bad", `${m.name} is at its total limit (${totalOn(m)}/${m.maxTotal}).`]);
  else out.push(["ok", `${m.name}: ${freeActive(m)}/${m.maxActive} active and ${freeTotal(m)}/${m.maxTotal} total free · ${m.ram} GB.`]);
  if (m.versions?.[a.harness] && a.harness === "codex" && m.versions.codex === "0.157.1") out.push(["bad", `codex ${m.versions.codex} on ${m.short} is older than the supported versions: the agent will refuse to start it.`]);
  const reuse = availableFor(m, a.harness);
  out.push(reuse ? ["ok", `Will use available session ${reuse.win} on ${m.short} (${reuse.yours ? "marked available on the board" : "fleet-started, goal merged"}); re-checked just before typing.`]
    : ["dim", `No available ${a.harness} session on ${m.short}: new window ${a.harness} ${it.display} in session fleet.`]);
  if (a.buckets) {
    const p = pace(a);
    out.push([p.state === "reserve" ? "bad" : p.state === "ahead" ? "warn" : "ok", `${a.label}: ${p.used}% used (${bindingBucket(a).label}), pace line ${Math.round(p.target)}%, reserve at ${p.cap}%, resets in ${until(a.resets)}. ${PACE_LABEL[p.state]}.`]);
    accounts.filter(x => x.id !== a.id && !dispatchable(x)).forEach(x => out.push(["dim", `${x.label} skipped: ${PACE_LABEL[pace(x).state]}.`]));
  }
  if (it.provider === "jira") out.push(["dim", `Jira ticket ${it.display}: code goes to ${it.targetRepo}; claiming it sets the status to In Progress.`]);
  return out;
}

const dispatchState = {};
function openDispatch(it) {
  Object.keys(dispatchState).forEach(k => delete dispatchState[k]);
  dispatchState[keyOf(it)] = suggest() ?? { m: machines[0].id, a: accounts[0].id };
}

function dispatchPanel(it) {
  const k = keyOf(it), st = dispatchState[k], id = k.replace(/[^a-z0-9]/gi, "_"), m = mById(st.m), a = anyAcct(st.a);
  const rs = reasons(it, st), blocked = rs.some(r => r[0] === "bad");
  const g = a.buckets ? startGoalCmd(it, m, a, availableFor(m, a.harness)) : null;
  return `<div class="disp-panel" data-k="${esc(k)}">
    <div class="disp">
      <label>Account <select data-f="a" id="da-${id}">${accounts.map(x => `<option value="${x.id}" ${x.id === st.a ? "selected" : ""}>${esc(x.label)} (${PACE_LABEL[pace(x).state]})</option>`).join("")}
        ${apiAccounts.map(x => `<option value="${x.id}" ${x.id === st.a ? "selected" : ""}>${esc(x.label)} via OpenCode (manual only)</option>`).join("")}</select></label>
      <label>Machine <select data-f="m" id="dm-${id}">${machines.map(x => `<option value="${x.id}" ${x.id === st.m ? "selected" : ""}>${esc(x.name)} (${freeActive(x)}/${x.maxActive} active free)</option>`).join("")}</select></label>
    </div>
    <ul class="why-list">${rs.map(([c, t]) => `<li class="${c}">${esc(t)}</li>`).join("")}</ul>
    ${g ? `<div class="why">The agent on ${esc(m.short)} will type this at the ready prompt:</div><code class="cmd">${esc(g.keys)}</code>
    ${dt("disp", `<span>command sent to the agent (one command_id):</span><code class="cmd">${esc(g.cmd)}</code><span>steps: ${esc(g.steps)}</span><span class="conv">${esc(g.note)}</span>`)}` : ""}
    <div class="actions"><button class="btn primary" data-start="${esc(k)}" ${blocked ? "disabled" : ""}>Dispatch goal</button>
      <button class="btn" data-cancel="${esc(k)}">Cancel</button><span class="why">Mockup: adds a sample goal, sends nothing.</span></div>
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
  const m = mById(st.m), a = anyAcct(st.a), reuse = availableFor(m, a.harness), id = "d" + String(it.n).replace(/\W/g, "");
  const g = startGoalCmd(it, m, a, reuse), fleetWins = sessions.filter(s => s.m === m.id && s.sess === "fleet");
  const idx = reuse ? reuse.idx : Math.max(-1, ...fleetWins.map(s => s.idx)) + 1;
  const s = { id, m: m.id, harness: a.harness, sess: reuse ? reuse.sess : "fleet", idx, win: reuse ? reuse.win : `${a.harness} ${it.display}`, paneId: reuse ? reuse.paneId : `%${50 + sessions.length}`,
    repo: it.repo, host: it.host, provider: it.provider, display: it.display, issue: it.kind === "issue" ? it.n : null, pr: it.kind === "pr" ? it.n : null, branch: it.branch ?? "(new branch)",
    line: `Drive ${it.display} to merged`, last: new Date(), started: new Date(), tasks: [[`Claim ${it.display}`, 0]],
    pane: [["dim", g.cmd], ["dim", `step 1/7 preflight ok · step 3/7 pick_session ${reuse ? "reuse " + reuse.win : "new window"}`], ["", `> ${g.keys}`], ["hl", "Starting goal…"]] };
  s.target = `${s.sess}:${s.idx}`;
  if (reuse) { const i = sessions.indexOf(reuse); sessions.splice(i, 1, s); } else sessions.push(s);
  log(`${who === "you" ? "dispatched" : "auto-dispatched"} ${it.display} to ${a.harness}${reuse ? ` in available session ${reuse.win}` : ""} (mock, nothing ran)`, s, who);
  delete dispatchState[k];
  if (who === "you") go("session", id);
}
