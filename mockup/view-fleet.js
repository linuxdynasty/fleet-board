// Fleet: machines you act on, weekly burn-down per account, settings, audit log.
function renderFleetView() {
  $("view").innerHTML = `
    <div class="section-head"><h2>Accounts · weekly usage</h2><span class="hint">Shared by every machine signed in. Line = even pace to your reserve.</span></div>
    <div class="caps">${accounts.map(burnCard).join("")}<div class="cap add"><div class="cap-name">Add an account</div>
      <div class="win empty">A harness, how to read its status screen, and your reserve. Team, Enterprise and API-key accounts work the same way.</div></div></div>

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
    <text x="${x(p.cap / 100) > 0 ? x(0) + 4 : 0}" y="${y(100) + 10}" fill="var(--bad)" font-size="9">reserve ${a.reserve}%</text></svg>`;
  const running = sessions.filter(s => s.harness === a.harness && ["working", "stuck"].includes(goalState(s))).length;
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill ${p.state === "reserve" ? "stuck" : p.state === "ahead" ? "waiting" : "working"}">${PACE_LABEL[p.state]}</span></div>
    ${svg}
    <div class="burn-legend"><span><i style="background:${tone}"></i>used ${p.used}%${a.buckets.length > 1 ? ` (${esc(bindingBucket(a).label)}; ${a.buckets.filter(b => b !== bindingBucket(a)).map(b => `${esc(b.label)} ${b.used}%`).join(", ")})` : ""}</span>
      <span><i class="dash"></i>line ${Math.round(p.target)}%</span><span>resets in <b data-until="${a.resets}">${until(a.resets)}</b></span></div>
    ${a.short ? `<div class="why">${esc(a.short.label)} window: ${a.short.used}% used</div>` : ""}
    <div class="src"><span class="real">real</span> ${esc(a.source)} read ${rel(a.read)} · ${running} active goal${running === 1 ? "" : "s"}</div></div>`;
}

function machineCard(m) {
  const ss = sessions.filter(s => s.m === m.id);
  const stale = Date.now() - m.beat > 30e3;
  return `<article class="fm${ui.fleetFocus === m.id ? " focus" : ""}" id="fm-${m.id}">
    <div class="top"><span class="dot ${stale ? "bad" : m.drained ? "warn" : "ok"}"></span><span class="name">${esc(m.name)}</span><span class="ip">${m.ip}</span></div>
    <div class="sub">${esc(m.user)} · ${esc(m.os)} · ${m.ram} GB${m.hub ? " · hub" : ""}</div>
    <div class="fm-stats"><span><b class="num">${activeOn(m)}/${m.maxActive}</b> active</span><span><b class="num">${totalOn(m)}/${m.maxTotal}</b> total</span>
      <span>collector <b data-t="${+m.beat}">${rel(m.beat)}</b></span>${m.drained ? `<span class="pill waiting">draining</span>` : `<span class="pill working">accepting goals</span>`}</div>
    <div class="tools">${m.harnesses.map(h => `<span class="tool">${h}</span>`).join("")}</div>
    <div class="fm-sess">${ss.map(s => `<button class="chip" data-sid="${s.id}"><span class="sdot ${liveState(s)}"></span>${esc(s.harness)} · ${esc(ref(s))}</button>`).join("") || `<span class="why">No sessions</span>`}</div>
    ${ui.confirm?.mid === m.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div>
      <div class="actions"><button class="btn primary" data-mact="do" data-m="${m.id}">${esc(ui.confirm.go)}</button><button class="btn" data-mact="cancel" data-m="${m.id}">Cancel</button></div></div>` :
    `<div class="actions"><button class="btn" data-mact="${m.drained ? "resume" : "drain"}" data-m="${m.id}">${m.drained ? "Resume" : "Drain…"}</button>
      <button class="btn" data-mact="ssh" data-m="${m.id}">Shell…</button></div>`}
  </article>`;
}

function fleetAct(m, act) {
  const n = totalOn(m);
  if (act === "cancel") ui.confirm = null;
  if (act === "drain") ui.confirm = { mid: m.id, label: `Stop giving ${m.name} new goals. Its ${n} goal${n === 1 ? "" : "s"} keep going until merged.`, go: "Drain" };
  if (act === "resume") { m.drained = false; log(`resumed ${m.short}: accepting goals`); }
  if (act === "ssh") ui.confirm = { mid: m.id, label: `ssh ${m.user}@${m.ip}`, go: "Copy command", copy: `ssh ${m.user}@${m.ip}` };
  if (act === "do") {
    const c = ui.confirm;
    if (c.copy) navigator.clipboard?.writeText(c.copy).then(() => {}, () => {});
    else { m.drained = true; log(`drained ${m.short}`); }
    ui.confirm = null;
  }
  rerender();
}
