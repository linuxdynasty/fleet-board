// Usage: is any subscription idle or going to waste? One plain line per account first (outlook() in
// fleet-dispatch.js), then each account as its harness shows it plus the standardized burn-down, API-key accounts
// (spend vs cap, manual only) and the OpenCode router. Raw contract names sit behind the "details" toggle.
function renderUsageView() {
  $("view").innerHTML = `
    <section class="waste" aria-label="Is any subscription going to waste?">
      <div class="section-head"><h2>Will any subscription go to waste?</h2><span class="hint">Projected from this week's burn rate so far. Your reserve is not counted as waste.</span></div>
      <ul class="waste-list">${accounts.map(wasteItem).join("")}${apiAccounts.map(spendItem).join("")}</ul>
    </section>

    <div class="section-head"><h2>Accounts · this week</h2><span class="hint">Shared by every machine signed in. Dashed line = even pace down to your reserve. The table is the tool's own screen.</span></div>
    <div class="caps">${accounts.map(burnCard).join("")}${apiAccounts.map(capCard).join("")}<div class="cap add"><div class="cap-name">OpenCode</div>
      <div class="win empty">A router, not an account: each session spends from the provider of its model (Anthropic, OpenAI, Google and Copilot by sign-in; DeepSeek and Moonshot by API key). Never started on an Anthropic subscription automatically. No usage screen and no /goal yet.</div></div></div>`;
}

const emailOf = a => settings.showEmails ? a.email : "email hidden";

function wasteItem(a) {
  const o = outlook(a), cls = a.stale ? "stale" : o.p.state === "reserve" ? "bad" : o.alert ? "alert" : "ok";
  return `<li class="${cls}">${agentChip(a.harness)}<b>${esc(a.label)}</b><span>${esc(wasteLine(a))}</span>
    ${dt("w-" + a.id, `<span>projected unused = (100 − reserve ${a.reserve}%) − (${o.p.used}% + burn rate × ${Math.round(hoursLeft(a))} h left), burn rate = % used ÷ hours elapsed</span><span>binding bucket: ${esc(bindingBucket(a).label)} · LimitBucket.percent_used ${o.p.used} · resets_time ${esc(new Date(a.resets).toISOString())}</span>`)}</li>`;
}
const spendItem = a => `<li class="api">${agentChip(a.provider.split(" ")[0].toLowerCase())}<b>${esc(a.label)}</b><span>${esc(spendLine(a))}</span></li>`;

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
    <circle cx="${x(p.frac)}" cy="${y(p.used)}" r="5" fill="${tone}" stroke="var(--panel)" stroke-width="2"${a.stale ? ` opacity=".45"` : ""}></circle>
    <text x="${x(0) + 4}" y="${y(100) + 10}" fill="var(--bad)" font-size="9">reserve ${a.reserve}%</text></svg>`;
  const src = a.sourceKind === "probe" ? `read from ${a.source} ${rel(a.read)}` : `read from the status line ${rel(a.read)}`;
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill ${a.stale ? "" : p.state === "reserve" ? "stuck" : p.state === "ahead" ? "waiting" : "working"}">${a.stale ? "stale" : PACE_LABEL[p.state]}</span></div>
    <div class="why">${esc(a.plan)} · ${esc(emailOf(a))}</div>
    ${a.stale ? `<div class="warn-t small-t">stale — reset passed, re-probe pending. These numbers are from before the reset.</div>` : ""}
    <table class="shown" aria-label="As shown by ${esc(a.harness)}">${a.shown.map(([g, v, r]) => `<tr><td>${esc(g)}</td><td class="num"><b>${esc(v)}</b></td><td class="why">${esc(r)}</td></tr>`).join("")}</table>
    ${svg}
    <div class="burn-legend"><span><i style="background:${tone}"></i>used ${p.used}%${a.buckets.length > 1 ? ` (${esc(bindingBucket(a).label)}; ${a.buckets.filter(b => b !== bindingBucket(a)).map(b => `${esc(b.label)} ${b.used}%`).join(", ")})` : ""}${bindingBucket(a).remaining ? " (shown as % left)" : ""}</span>
      <span><i class="dash"></i>line ${Math.round(p.target)}%</span><span>${a.stale ? "next reset" : "resets"} in <b data-until="${a.resets}">${until(a.resets)}</b></span></div>
    <div class="src"><span class="real">real</span> ${esc(src)} · ${goalsTxt(runningOn(a.harness))}</div>
    ${dt("u-" + a.id, `<span>goal consent: ${esc(a.consent.join(", "))} · mode: ${esc(a.mode)}</span><span>UsageReading.shown (as shown) · LimitBucket.percent_used, resets_time (chart) · source ${esc(a.sourceKind === "probe" ? "USAGE_SOURCE_PROBE" : "USAGE_SOURCE_PASSIVE")}${a.passive ? ` · "${esc(a.passive)}"` : ""}</span>`)}</div>`;
}

// API-key accounts: spent so far as the tool reports it (all-time, no period) against the owner's one limit; manual dispatch only.
function capCard(a) {
  const pct = Math.min(100, Math.round(a.spentUsd / a.capUsd * 100)), tone = pct >= 90 ? "var(--bad)" : pct >= 70 ? "var(--warn)" : "var(--ok)";
  const link = sessions.filter(s => s.acct === a.id).map(s => `${s.win} on ${mById(s.m).short}`);
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill waiting">manual only</span></div>
    <div class="why">${esc(a.provider)} through OpenCode · paid per use · never started automatically</div>
    <div class="win"><div class="win-top"><span>spent so far $${a.spentUsd.toFixed(2)} of your $${a.capUsd} limit</span><span class="num">${pct}%</span></div><div class="track"><div class="fill" style="width:${pct}%;background:${tone}"></div></div></div>
    <div class="why">sessions spending here: ${esc(link.join(", ") || "none")}</div>
    <div class="src"><span class="samp">sample</span> read from ${esc(a.source)} ${rel(a.read)}</div>
    ${dt("u-" + a.id, `<span>SpendReading · BUDGET_KIND_DOLLAR_CAP · SessionAccountLink</span>`)}</div>`;
}
