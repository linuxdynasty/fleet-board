// Machines: one card per machine (state, harnesses with their goal-hook status, goal windows, drain, attach line)
// and the audit log. Plain words by default; raw fields (policy hash, GoalHooksState, typed commands) sit behind
// the "details" toggle. Goal-hook states are sample data (fleet-data.js `goalHooks`, 01d).
function renderMachinesView() {
  $("view").innerHTML = `
    <div class="section-head"><h2>Machines</h2><span class="hint">active = goals using quota · total = active + waiting on CI or review · drain = finish what runs, take nothing new · <button class="link" id="toAudit">audit log ↓</button></span></div>
    <div class="fleet">${machines.map(machineCard).join("")}</div>
    <details class="fold" id="audit" data-dk="audit" ${ui.dt.audit ? "open" : ""}><summary>Audit log <span class="hint">${audit.length} entries · every action, yours and the fleet's</span></summary>
      <ol class="audit">${audit.map(a => `<li><span class="num">${hhmm(a.at)}</span><b>${esc(a.who)}</b><span>${esc(a.what)}</span><span class="mono why">${esc(a.where)}</span></li>`).join("")}</ol></details>`;
  const v = $("view");
  v.querySelectorAll("[data-sid]").forEach(b => b.onclick = () => go("session", b.dataset.sid));
  v.querySelectorAll("[data-mact]").forEach(b => b.onclick = () => machineAct(mById(b.dataset.m), b.dataset.mact));
  $("toAudit").onclick = () => { $("audit").open = true; $("audit").scrollIntoView({ block: "start" }); };
  if (ui.fleetFocus) { $("fm-" + ui.fleetFocus)?.scrollIntoView({ block: "center" }); ui.fleetFocus = null; }
  if (ui.auditFocus) { $("audit").scrollIntoView({ block: "start" }); ui.auditFocus = false; }
}

// GoalHooksState (01d) in plain words, with a tone for the line.
function hookPlain(h, st) {
  if (h === "opencode") return ["", "no /goal, so no goal hooks"];
  if (!st) return h === "muse" ? ["", "no fleet hooks yet (Muse takes them as a plugin); goals run on Muse's own /goal"] : ["", "no fleet hooks"];
  if (!st.installed) return ["warn", `hooks not installed${st.why ? ": " + st.why : ""}`];
  if (!st.verified) return ["warn", "hooks installed, self-check failed: reinstall on the machine"];
  if (st.trust_pending) return ["warn", `hooks installed, trust pending — open ${h === "codex" ? "Codex" : h} /hooks to approve`];
  if (st.repo_ships_own) return ["ok", "hooks installed; eshu ships its own, so fleet's stay off there"];
  return ["ok", "hooks installed and checked"];
}
const hookRaw = st => st ? `GoalHooksState{${Object.entries(st).filter(([k]) => k !== "why").map(([k, v]) => `${k}: ${v === "" ? '""' : v}`).join(", ")}}` : "GoalHooksState: not reported";

function harnessList(m) {
  const hooks = goalHooks[m.id] ?? {};
  return `<ul class="hk" aria-label="Harnesses on ${esc(m.short)}"><li class="why">harness · goal hooks <span class="samp">sample</span></li>${m.harnesses.map(h => { const [tone, text] = hookPlain(h, hooks[h]);
    return `<li>${agentChip(h)}<span class="num why">${esc(m.versions?.[h] ?? "")}</span><span class="${tone ? tone + "-t" : "why"}">${esc(text)}</span></li>`; }).join("")}</ul>`;
}

function machineCard(m) {
  const ss = sessions.filter(s => s.m === m.id), goals = ss.filter(s => !s.yours), own = ss.filter(s => s.yours);
  const stale = Date.now() - m.beat > 30e3, certDays = Math.round((m.certExpires - Date.now()) / 86400e3);
  const avail = ss.filter(s => liveState(s) === "available").length;
  const warns = [certDays <= 14 && `agent certificate expires in ${certDays} days; it renews itself while the machine is online`,
    m.policyChanged && "the machine's local policy file changed; it applies after the agent restarts"].filter(Boolean);
  const facts = [`${m.user} · ${m.os} · ${m.ram} GB${m.hub ? " · hub" : ""} · agent ${m.agentVersion}`, `labels: ${m.labels.join(", ")}`,
    `${m.tmuxVersion} · default server: ${m.defaultServer}`, ...m.forge.map(f => `${f.tool} signed in as ${f.user} (${f.host}) · push: ${f.access.map(([r, p]) => `${shortName(r)} ${p}`).join(", ")}`),
    `agent cert expires in ${certDays}d (renews at ⅔ lifetime)`, `policy ${m.policyHash} · ${m.policyChanged ? "changed on disk (PolicyChanged)" : "unchanged"}`,
    ...m.harnesses.filter(h => h !== "opencode").map(h => `${h}: ${hookRaw((goalHooks[m.id] ?? {})[h])}`)];
  return `<article class="fm${ui.fleetFocus === m.id ? " focus" : ""}" id="fm-${m.id}">
    <div class="top"><span class="dot ${stale ? "bad" : m.drained ? "warn" : "ok"}"></span><span class="name">${esc(m.name)}</span><span class="ip">${esc(m.ip)}</span></div>
    <div class="fm-stats">${m.drained ? `<span class="pill waiting">draining</span>` : `<span class="pill working">taking goals</span>`}<span><b class="num">${activeOn(m)}/${m.maxActive}</b> active</span><span><b class="num">${totalOn(m)}/${m.maxTotal}</b> total</span><span><b class="num">${avail}</b> available</span>
      <span>seen <b data-t="${+m.beat}">${rel(m.beat)}</b></span></div>
    ${warns.map(w => `<div class="warn-t small-t">${esc(w)}</div>`).join("")}
    ${harnessList(m)}
    <div class="fm-sess">${goals.map(s => `<button class="chip" data-sid="${s.id}"><span class="sdot ${liveState(s)}"></span>${esc(s.win)}</button>`).join("")}
      ${own.length ? `<button class="link" data-sid="${own[0].id}">+ ${own.length} window${own.length === 1 ? "" : "s"} of yours</button>` : ""}${ss.length ? "" : `<span class="why">No windows</span>`}</div>
    ${ui.confirm?.mid === m.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div>${ui.confirm.copy ? `<code>${esc(ui.confirm.cmd)}</code>` : dt("cf-" + m.id, `<code>${esc(ui.confirm.cmd)}</code>${ui.confirm.raw ? `<span>${esc(ui.confirm.raw)}</span>` : ""}`)}
      <div class="actions"><button class="btn primary" data-mact="do" data-m="${m.id}">${esc(ui.confirm.go)}</button><button class="btn" data-mact="cancel" data-m="${m.id}">Cancel</button></div></div>` :
    `<div class="actions"><button class="btn" data-mact="${m.drained ? "resume" : "drain"}" data-m="${m.id}">${m.drained ? "Undrain" : "Drain…"}</button>
      <button class="btn" data-mact="attach" data-m="${m.id}">Attach from your terminal…</button></div>`}
    ${dt("m-" + m.id, facts.map(f => `<span>${esc(f)}</span>`).join(""))}
  </article>`;
}

function machineAct(m, act) {
  const n = totalOn(m);
  if (act === "cancel") ui.confirm = null;
  if (act === "drain") ui.confirm = { mid: m.id, label: `Stop giving ${m.name} new goals. Its ${n} goal${n === 1 ? "" : "s"} keep going until merged. Never kills anything.`, cmd: drainCmd(m, true), go: "Drain" };
  if (act === "resume") ui.confirm = { mid: m.id, label: `Let ${m.name} take new goals again. A drain started on the machine itself can only be lifted there.`, cmd: drainCmd(m, false), go: "Undrain", undrain: true, raw: "a local `fleet-agent drain` refuses this with REJECTED_BY_POLICY · DRAINING" };
  if (act === "attach") ui.confirm = { mid: m.id, label: "Attach to this machine's tmux from your own terminal. The line runs on your machine; the hub never runs ssh.", cmd: attachMachine(m), go: "Copy line", copy: attachMachine(m) };
  if (act === "do") {
    const c = ui.confirm;
    if (c.copy) { navigator.clipboard?.writeText(c.copy).then(() => {}, () => {}); log(`copied attach line for ${m.short}`); }
    else if (c.undrain) { m.drained = false; log(`undrained ${m.short}: taking goals again (mock)`); }
    else { m.drained = true; log(`drained ${m.short}: no new goals (mock)`); }
    ui.confirm = null;
  }
  rerender();
}
