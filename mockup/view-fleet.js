// Fleet: accounts (usage shown in each harness's own terms plus the standardized burn-down), API-key accounts,
// machines (facts, forge CLI, cert, policy, labels), settings, audit log.
function renderFleetView() {
  $("view").innerHTML = `
    <div class="section-head"><h2>Accounts · weekly usage</h2><span class="hint">Shared by every machine signed in. Line = even pace to your reserve. Usage shown as each harness shows it (D2), plus the standardized chart.</span></div>
    <div class="caps">${accounts.map(burnCard).join("")}${apiAccounts.map(capCard).join("")}<div class="cap add"><div class="cap-name">OpenCode</div>
      <div class="win empty">A router, not an account: each session spends from the provider on its model line (Anthropic, OpenAI, Google, Copilot by OAuth; DeepSeek and Moonshot by API key). Usage links to that account. Never auto-dispatched on an Anthropic subscription; API-key accounts are manual only. No /usage and no /goal yet.</div></div></div>

    <div class="section-head"><h2>Machines</h2><span class="hint">active = goals using quota · total = active + waiting on CI/review · drain = finish, take nothing new</span></div>
    <div class="fleet">${machines.map(machineCard).join("")}</div>

    <section class="section" id="settings" aria-label="Settings"></section>

    <div class="section-head"><h2>Audit log</h2><span class="hint">Every action, yours and the fleet's</span></div>
    <ol class="audit">${audit.map(a => `<li><span class="num">${hhmm(a.at)}</span><b>${esc(a.who)}</b><span>${esc(a.what)}</span><span class="mono why">${esc(a.where)}</span></li>`).join("")}</ol>`;
  renderSettings();
  $("view").querySelectorAll("[data-sid]").forEach(b => b.onclick = () => go("session", b.dataset.sid));
  $("view").querySelectorAll("[data-mact]").forEach(b => b.onclick = () => fleetAct(mById(b.dataset.m), b.dataset.mact));
  if (ui.fleetFocus) { $("fm-" + ui.fleetFocus)?.scrollIntoView({ block: "center" }); ui.fleetFocus = null; }
}

const emailOf = a => settings.showEmails ? a.email : "email hidden";

// Burn-down: x = the weekly window, y = % used. Pace line from 0 to the reserve at reset.
function burnCard(a) {
  const p = pace(a), W = 280, H = 130, L = 30, R = 8, T = 8, B = 22;
  const x = f => L + f * (W - L - R), y = v => T + (1 - v / 100) * (H - T - B);
  const tone = p.state === "reserve" ? "var(--bad)" : p.state === "ahead" ? "var(--warn)" : "var(--ok)";
  const days = [...Array(a.windowDays + 1).keys()];
  const svg = `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="${esc(`${a.label}: ${p.used}% used, pace line ${Math.round(p.target)}%`)}" class="burn">
    <rect x="${x(0)}" y="${y(100)}" width="${x(1) - x(0)}" height="${y(p.cap) - y(100)}" fill="var(--bad)" opacity=".10"></rect>
    ${[0, 50, 100].map(v => `<line x1="${x(0)}" x2="${x(1)}" y1="${y(v)}" y2="${y(v)}" stroke="var(--line)" stroke-width="1"></line><text x="${L - 5}" y="${y(v) + 3}" text-anchor="end" fill="var(--muted)" font-size="9">${v}%</text>`).join("")}
    ${days.map(d => `<text x="${x(d / a.windowDays)}" y="${H - 6}" text-anchor="middle" fill="var(--muted)" font-size="9">${d === a.windowDays ? "reset" : "d" + d}</text>`).join("")}
    <line x1="${x(0)}" y1="${y(0)}" x2="${x(1)}" y2="${y(p.cap)}" stroke="var(--muted)" stroke-width="1.5" stroke-dasharray="4 3"></line>
    <line x1="${x(p.frac)}" x2="${x(p.frac)}" y1="${y(100)}" y2="${y(0)}" stroke="var(--line)" stroke-width="1"></line>
    <circle cx="${x(p.frac)}" cy="${y(p.target)}" r="3" fill="var(--muted)"></circle>
    <circle cx="${x(p.frac)}" cy="${y(p.used)}" r="5" fill="${tone}" stroke="var(--panel)" stroke-width="2"></circle>
    <text x="${x(0) + 4}" y="${y(100) + 10}" fill="var(--bad)" font-size="9">reserve ${a.reserve}%</text></svg>`;
  const running = sessions.filter(s => isGoal(s) && s.harness === a.harness && ["working", "stuck"].includes(goalState(s))).length;
  const src = a.sourceKind === "probe" ? `source: probe ${a.source} ${rel(a.read)}` : `source: passive (${a.passive ?? "status line"}) ${rel(a.read)}`;
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill ${p.state === "reserve" ? "stuck" : p.state === "ahead" ? "waiting" : "working"}">${PACE_LABEL[p.state]}</span></div>
    <div class="why">${esc(a.plan)} · ${esc(emailOf(a))} · consent ${esc(a.consent.join(", "))} · mode ${esc(a.mode)}</div>
    <table class="shown" aria-label="As shown by ${esc(a.harness)}">${a.shown.map(([g, v, r]) => `<tr><td>${esc(g)}</td><td class="num"><b>${esc(v)}</b></td><td class="why">${esc(r)}</td></tr>`).join("")}</table>
    ${svg}
    <div class="burn-legend"><span><i style="background:${tone}"></i>used ${p.used}%${a.buckets.length > 1 ? ` (${esc(bindingBucket(a).label)}; ${a.buckets.filter(b => b !== bindingBucket(a)).map(b => `${esc(b.label)} ${b.used}%`).join(", ")})` : ""}${bindingBucket(a).remaining ? " (shown as % left)" : ""}</span>
      <span><i class="dash"></i>line ${Math.round(p.target)}%</span><span>resets in <b data-until="${a.resets}">${until(a.resets)}</b></span></div>
    <div class="src"><span class="real">real</span> ${esc(src)} · ${running} active goal${running === 1 ? "" : "s"}</div></div>`;
}

// API-key accounts: dollar cap per period, manual dispatch only.
function capCard(a) {
  const pct = Math.min(100, Math.round(a.spentUsd / a.capUsd * 100)), tone = pct >= 90 ? "var(--bad)" : pct >= 70 ? "var(--warn)" : "var(--ok)";
  const link = sessions.filter(s => s.acct === a.id).map(s => `${s.win} on ${mById(s.m).short}`);
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill waiting">manual only</span></div>
    <div class="why">${esc(a.provider)} via OpenCode · API key · BUDGET_KIND_DOLLAR_CAP · never auto-dispatched</div>
    <div class="win"><div class="win-top"><span>$${a.spentUsd.toFixed(2)} of $${a.capUsd} this ${esc(a.period)}</span><span class="num">${pct}%</span></div><div class="track"><div class="fill" style="width:${pct}%;background:${tone}"></div></div></div>
    <div class="why">sessions spending here: ${esc(link.join(", ") || "none")}</div>
    <div class="src"><span class="samp">sample</span> source: ${esc(a.source)} ${rel(a.read)}</div></div>`;
}

function machineCard(m) {
  const ss = sessions.filter(s => s.m === m.id), stale = Date.now() - m.beat > 30e3, certDays = Math.round((m.certExpires - Date.now()) / 86400e3);
  const avail = ss.filter(s => liveState(s) === "available").length;
  return `<article class="fm${ui.fleetFocus === m.id ? " focus" : ""}" id="fm-${m.id}">
    <div class="top"><span class="dot ${stale ? "bad" : m.drained ? "warn" : "ok"}"></span><span class="name">${esc(m.name)}</span><span class="ip">${esc(m.ip)}</span></div>
    <div class="sub">${esc(m.user)} · ${esc(m.os)} · ${m.ram} GB${m.hub ? " · hub" : ""} · agent ${esc(m.agentVersion)}</div>
    <div class="labels">${m.labels.map(l => `<span class="label">${esc(l)}</span>`).join("")}</div>
    <div class="fm-stats"><span><b class="num">${activeOn(m)}/${m.maxActive}</b> active</span><span><b class="num">${totalOn(m)}/${m.maxTotal}</b> total</span><span><b class="num">${avail}</b> available</span>
      <span>heartbeat <b data-t="${+m.beat}">${rel(m.beat)}</b></span>${m.drained ? `<span class="pill waiting">draining</span>` : `<span class="pill working">accepting goals</span>`}</div>
    <div class="facts">
      <span><b>${esc(m.tmuxVersion)}</b> · default server: ${esc(m.defaultServer)}</span>
      ${m.forge.map(f => `<span><b>${esc(f.tool)}</b> signed in as ${esc(f.user)} (${esc(f.host)}) · push: ${f.access.map(([r, p]) => `${esc(shortName(r))} ${esc(p)}`).join(", ")}</span>`).join("")}
      <span class="${certDays <= 14 ? "warn-t" : ""}">agent cert expires in ${certDays}d${certDays <= 14 ? " · renews at ⅔ lifetime; re-enroll if offline past expiry" : ""}</span>
      <span class="${m.policyChanged ? "warn-t" : ""}">policy ${esc(m.policyHash)} · ${m.policyChanged ? "changed on disk, applies after agent restart" : "unchanged"}</span>
    </div>
    <div class="tools">${m.harnesses.map(h => `<span class="tool">${esc(h)} ${esc(m.versions?.[h] ?? "")}</span>`).join("")}</div>
    <div class="fm-sess">${ss.map(s => `<button class="chip" data-sid="${s.id}"><span class="sdot ${liveState(s)}"></span>${esc(s.sess)}:${s.idx} ${esc(s.win)}</button>`).join("") || `<span class="why">No windows</span>`}</div>
    ${ui.confirm?.mid === m.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div><code>${esc(ui.confirm.cmd)}</code>
      <div class="actions"><button class="btn primary" data-mact="do" data-m="${m.id}">${esc(ui.confirm.go)}</button><button class="btn" data-mact="cancel" data-m="${m.id}">Cancel</button></div></div>` :
    `<div class="actions"><button class="btn" data-mact="${m.drained ? "resume" : "drain"}" data-m="${m.id}">${m.drained ? "Undrain" : "Drain…"}</button>
      <button class="btn" data-mact="attach" data-m="${m.id}">Attach from your terminal…</button></div>`}
  </article>`;
}

function fleetAct(m, act) {
  const n = totalOn(m);
  if (act === "cancel") ui.confirm = null;
  if (act === "drain") ui.confirm = { mid: m.id, label: `Stop giving ${m.name} new goals. Its ${n} goal${n === 1 ? "" : "s"} keep going until merged. Never kills anything.`, cmd: drainCmd(m, true), go: "Drain" };
  if (act === "resume") ui.confirm = { mid: m.id, label: `Let ${m.name} take new goals again. A local \`fleet-agent drain\` cannot be undone from the hub (REJECTED_BY_POLICY · DRAINING).`, cmd: drainCmd(m, false), go: "Undrain", undrain: true };
  if (act === "attach") ui.confirm = { mid: m.id, label: "Attach to this machine's tmux from your own terminal. The line runs on your machine; the hub never runs ssh.", cmd: attachMachine(m), go: "Copy line", copy: attachMachine(m) };
  if (act === "do") {
    const c = ui.confirm;
    if (c.copy) { navigator.clipboard?.writeText(c.copy).then(() => {}, () => {}); log(`copied attach line for ${m.short}`); }
    else if (c.undrain) { m.drained = false; log(`undrained ${m.short}: accepting goals (Undrain{})`); }
    else { m.drained = true; log(`drained ${m.short} (Drain{reason: owner})`); }
    ui.confirm = null;
  }
  rerender();
}
