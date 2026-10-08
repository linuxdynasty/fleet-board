// Machines: one card per machine (state, harnesses with their goal-hook status, goal windows, drain, attach line)
// and the audit log. Plain words by default; raw fields (policy hash, GoalHooksState, typed commands) sit behind
// the "details" toggle. Goal-hook states are sample data (fleet-data.js `goalHooks`, 01d).
function renderMachinesView() {
  $("view").innerHTML = `
    ${systemSection()}
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

// System copy, keyed by SystemSignalKind, SystemMeasureKind and SystemDirection. Words only: every number, level and
// hold shown comes from the signal's measures (SystemMeasure), never from this table or from 05b.
const SYS_NAME = { WRITER_BATCH_LAG: "Hub writer keeping up", OUTBOX_BACKLOG: "Commands waiting for online machines", WATCH_DROPS: "Browser streams that fell behind",
  INTEGRATION_SYNC_LAG: "GitHub sync", LOG_STORE_USAGE: "Session log store", DISK_FREE: "Disk free for the hub's database", BACKUP_AGE: "Last backup",
  USAGE_PROBE_FRESHNESS: "Usage readings fresh", AGENT_CONNECTED: "Always-on machines connected", CLOCK_SKEW: "Machine clocks agree with the hub" };
const SYS_WORD = { WRITER_BATCH_LAG: "wait", OUTBOX_BACKLOG: "oldest", WATCH_DROPS: "dropped", INTEGRATION_SYNC_LAG: "behind", LOG_STORE_USAGE: "used",
  DISK_FREE: "free", BACKUP_AGE: "age", USAGE_PROBE_FRESHNESS: "oldest", AGENT_CONNECTED: "longest silence", CLOCK_SKEW: "worst",
  WRITER_COMMIT_TIME: "commit", WRITER_QUEUE_FULL: "queue full" };
const SYS_DIR = { ABOVE: "over", BELOW: "under" };
const sysName = s => SYS_NAME[s.kind] ?? s.kind;
const sysWord = (s, m) => SYS_WORD[m.kind === "MAIN" ? s.kind : m.kind] ?? m.kind;
const sysDur = v => v > 0 && v < 1 ? Math.round(v * 1000) + " ms" : v < 60 ? +v.toFixed(2) + " s" : v < 3600 ? Math.round(v / 60) + " min" : v < 172800 ? Math.round(v / 3600) + " h" : Math.round(v / 86400) + " days";
const sysNum = (v, unit) => v == null ? "not known" : unit === "SECONDS" ? sysDur(v) : unit === "PERCENT" ? v + " %" : unit === "BYTES" ? +(v / 2 ** 30).toFixed(1) + " GiB" : String(v);
const sysValue = s => s.measures.map(m => `${sysWord(s, m)} ${sysNum(m.value, m.unit)}`).join(" · ");
// One measure's levels: "warn over 1 s · critical over 5 s"; equal levels are told apart by their holds ("over 100 % for 10 min · for 1 h").
function sysLevels(m) {
  const hold = h => h ? `for ${sysDur(h)}` : "";
  const lvl = (label, v, h) => [label, SYS_DIR[m.direction], sysNum(v, m.unit), hold(h)].filter(Boolean).join(" ");
  if (m.warn != null && m.warn === m.crit) return `${lvl("", m.warn, m.warnHold)} · ${hold(m.critHold) || "critical"}`;
  const out = [];
  if (m.warn != null) out.push(lvl("warn", m.warn, m.warnHold));
  if (m.crit != null) out.push(lvl("critical", m.crit, m.critHold));
  if (m.critClear != null) out.push(`critical ends ${SYS_DIR[m.direction === "BELOW" ? "ABOVE" : "BELOW"]} ${sysNum(m.critClear, m.unit)}`);
  return out.join(" · ");
}
const sysLimits = s => s.measures.map(m => (s.measures.length > 1 ? `${sysWord(s, m)}: ` : "") + sysLevels(m)).join("; ");
const sysRaw = m => `SystemMeasure{kind ${m.kind}, value ${m.value ?? "unset"}, unit ${m.unit}, direction ${m.direction}, warning_threshold ${m.warn ?? "unset"}, critical_threshold ${m.crit ?? "unset"}`
  + `${m.warnHold ? `, warning_hold ${m.warnHold}s` : ""}${m.critHold ? `, critical_hold ${m.critHold}s` : ""}${m.critClear != null ? `, critical_clear_threshold ${m.critClear}` : ""}}`;

// System section (05b): the hub's and the agents' own health. Ten signals with fixed levels; sample readings here.
// A signal that leaves OK raises one triage item of kind SYSTEM (deduplicated); this list is GetSystemStatus.
function systemSection() {
  const bad = systemSignals.filter(s => s.state === "warning" || s.state === "critical").length;
  return `<section aria-label="System"><div class="section-head"><h2>System</h2><span class="samp">sample</span><span class="hint">${bad ? `${bad} thing${bad === 1 ? "" : "s"} to look at` : "everything is fine"} · fleetd v0.3.1 · up 6d 4h · metrics on, OTLP off, pprof off</span></div>
    <ul class="sys">${systemSignals.map(s => `<li class="${s.state}"><b>${esc(sysName(s))}</b><span class="why">${esc(sysValue(s))}</span>${s.state === "ok" ? "" : `<span class="pill waiting">${esc(s.state)}</span>`}${s.note ? `<span class="warn-t small-t">${esc(s.note)}</span>` : ""}
      ${dt("sy-" + s.kind + s.subject, `<span>SystemSignal{kind SYSTEM_SIGNAL_KIND_${esc(s.kind)}, subject ${esc(s.subject)}, state ${esc(s.state.toUpperCase())}} · ${esc(sysLimits(s))}</span>${s.measures.map(m => `<span>${esc(sysRaw(m))}</span>`).join("")}${s.state === "ok" ? "" : `<span>raises TriageItem{kind SYSTEM, fingerprint system:${esc(s.kind.toLowerCase())}:${esc(s.subject)}}</span>`}`)}</li>`).join("")}</ul></section>`;
}

// GoalHooksState (01d) in plain words, with a tone for the line.
function hookPlain(h, st) {
  if (h === "opencode") return ["", "no /goal, so no goal hooks"];
  if (!st) return ["", "hook state not reported"];
  if (!st.installed) return ["warn", `hooks not installed${st.why ? ": " + st.why : ""}`];
  if (!st.verified) return ["warn", "hooks installed, self-check failed: reinstall on the machine"];
  if (st.trust === "PENDING") return ["warn", `hooks installed, trust pending: run ${TRUST_APPROVE[h]} on the machine to approve${h === "muse" ? " (Muse takes the hooks as a plugin)" : ""}`];
  if (st.repo_ships_own) return ["ok", "hooks installed; eshu ships its own, so fleet's stay off there"];
  return ["ok", "hooks installed and checked"];
}
const hookRaw = st => st ? `GoalHooksState{${Object.entries(st).filter(([k]) => k !== "why").map(([k, v]) => `${k}: ${v === "" ? '""' : v}`).join(", ")}}` : "GoalHooksState: not reported";

function harnessList(m) {
  const hooks = goalHooks[m.id] ?? {};
  return `<ul class="hk" aria-label="Harnesses on ${esc(m.short)}"><li class="why">harness · goal hooks <span class="samp">sample</span></li>${m.harnesses.map(h => { const [tone, text] = hookPlain(h, hooks[h]);
    return `<li>${agentChip(h)}<span class="num why">${esc(m.versions?.[h] ?? "")}</span><span class="${tone ? tone + "-t" : "why"}">${esc(text)}</span></li>`; }).join("")}</ul>`;
}

// MachineAgentHealth from the heartbeat (AgentHealth, 05b section 6), sample values.
const agentHealthLine = m => { const h = agentHealth[m.id]; return h ? `agent health (sample): ring ${h[0]} events · ${h[1]} MiB · unacknowledged results ${h[2]} · reconnects ${h[3]} · memory ${h[4]} MiB` : "agent health: not reported"; };

function machineCard(m) {
  const ss = sessions.filter(s => s.m === m.id), goals = ss.filter(s => !s.yours), own = ss.filter(s => s.yours);
  // STALE after 45 s without a heartbeat, OFFLINE after 3 minutes (defaults.md).
  const age = Date.now() - m.beat, stale = age > 45e3, offline = age > 180e3, certDays = Math.round((m.certExpires - Date.now()) / 86400e3);
  const avail = ss.filter(s => liveState(s) === "available").length;
  const warns = [certDays <= 14 && `agent certificate expires in ${certDays} days; it renews itself while the machine is online`,
    m.policyChanged && "the machine's local policy file changed; it applies after the agent restarts"].filter(Boolean);
  const facts = [`${m.user} · ${m.os} · ${m.ram} GB${m.hub ? " · hub" : ""} · agent ${m.agentVersion}`, `labels: ${m.labels.join(", ")}`,
    `${m.tmuxVersion} · default server: ${m.defaultServer}`, ...m.forge.map(f => `${f.tool} signed in as ${f.user} (${f.host}) · push: ${f.access.map(([r, p]) => `${shortName(r)} ${p}`).join(", ")}`),
    `agent cert expires in ${certDays}d (renews at ⅔ lifetime)`, agentHealthLine(m), `policy ${m.policyHash} · ${m.policyChanged ? "changed on disk (PolicyChanged)" : "unchanged"}`,
    ...m.harnesses.filter(h => h !== "opencode").map(h => `${h}: ${hookRaw((goalHooks[m.id] ?? {})[h])}`)];
  return `<article class="fm${ui.fleetFocus === m.id ? " focus" : ""}" id="fm-${m.id}">
    <div class="top"><span class="dot ${offline ? "bad" : stale ? "warn" : m.drained ? "warn" : "ok"}"></span><span class="name">${esc(m.name)}</span>${offline ? `<span class="pill waiting">offline</span>` : stale ? `<span class="pill waiting">stale</span>` : ""}</div>
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
