// Insights (key 7): what the hub's own data says (docs/specs/05-insights.md). Efficiency per subscription, goal outcomes
// per harness, where work gets stuck, size estimate versus actual, the latest written report and suggestions.
// All numbers are sample data (fleet-data-insights.js). Rates need 5 goals; a hidden rate shows "too few goals".
// The report is untrusted model text: it is shown sanitized (headings, lists, links as text, no HTML), never as markup.
const dismissedSug = {};
const slope = ys => { const n = ys.length, mx = (n - 1) / 2, my = ys.reduce((a, b) => a + b, 0) / n;
  const sxy = ys.reduce((s, y, i) => s + (i - mx) * (y - my), 0), sxx = ys.reduce((s, _, i) => s + (i - mx) ** 2, 0); return Math.round(sxy / sxx); };
const hrs = h => h >= 1 ? `${h.toFixed(1)} h` : `${Math.round(h * 60)} min`;
const sample = `<span class="samp">sample</span>`;

function effCard(e) {
  const a = accounts.find(x => x.id === e.acct), usable = 100 - a.reserve;
  const unused = e.used.map(u => Math.max(0, usable - u)), usedShare = e.used.map(u => Math.round(Math.min(u, usable) / usable * 100));
  const mean = arr => Math.round(arr.reduce((s, v) => s + v, 0) / arr.length), tr = slope(unused);
  const bars = e.used.map((u, i) => `<div class="win"><div class="win-top"><span>window ${i + 1}${i === e.used.length - 1 ? " (latest)" : ""}</span><span class="num">${usedShare[i]}% of usable · ${unused[i]} pts unused</span></div>
    <div class="track"><div class="fill" style="width:${usedShare[i]}%;background:${unused[i] >= 20 ? "var(--warn)" : "var(--ok)"}"></div></div></div>`).join("");
  return `<div class="cap"><div class="cap-top">${agentChip(a.harness)}<span class="cap-name">${esc(a.label)}</span><span class="pill ${mean(unused) >= 20 ? "waiting" : "working"}">${mean(unused) >= 20 ? "wasting" : "well used"}</span></div>
    <div class="why">usable quota = 100 − your ${a.reserve}% reserve = ${usable}. The reserve is never counted as waste.</div>${bars}
    <div class="why">average ${mean(usedShare)}% of usable used before reset · ${mean(unused)} points unused at reset · waste ${tr === 0 ? "steady" : tr < 0 ? `shrinking ${-tr} pts per window` : `growing ${tr} pts per window`} · ${e.idleBehindH} h idle while behind pace · the fleet spent ${e.fleetUsed} of the used points</div>
    ${dt("ie-" + a.id, `<span>QuotaWindow{bucket_key ${esc(a.buckets[0].key)}, used_at_end, usable_used, unused, fleet_used, behind, idle_behind} · EfficiencySummary.waste_trend_points ${tr}</span><span>GetInsights{sections: EFFICIENCY}</span>`)}</div>`;
}

function outcomeRow(o) {
  const few = o.ran < 5, rate = (n, d) => few ? `<span class="why">too few goals</span>` : `${Math.round(n / d * 100)}%`, per = n => few ? "–" : (n / o.ran).toFixed(1);
  return `<tr><td>${agentChip(o.harness)}${o.harness === "opencode" ? ` <span class="why">manual only</span>` : ""}</td><td class="num">${o.ran}</td><td class="num">${rate(o.merged, o.ran)}</td>
    <td class="num">${o.merged >= 5 ? `${hrs(o.p50)} / ${hrs(o.p90)}` : "–"}</td><td class="num">${per(o.nudges)}</td><td class="num">${rate(o.stuck, o.ran)}</td><td class="num">${per(o.reviews)}</td><td class="num">${per(o.restarts)}</td>
    <td class="num">${o.pts && o.merged ? (o.pts / o.merged).toFixed(1) : "–"}</td></tr>`;
}

const stuckList = rows => `<ul class="why-list">${rows.map(t => `<li class="${t.cls ?? "warn"}">${t.html}</li>`).join("")}</ul>`;

// Sanitized markdown for the report: headings, bullets and paragraphs; comments dropped; links shown as text.
function reportHtml(text) {
  return text.split("\n").filter(l => !l.startsWith("<!--")).map(l => {
    const t = esc(l.replace(/\[([^\]]*)\]\(([^)]*)\)/g, (_, a, u) => `${a} (${u})`));
    return l.startsWith("## ") ? `<h4>${t.slice(3)}</h4>` : l.startsWith("# ") ? `<h3>${t.slice(2)}</h3>` : l.startsWith("- ") ? `<div class="rep-li">• ${t.slice(2)}</div>` : `<p>${t}</p>`; }).join("");
}

function renderInsightsView() {
  const open = suggestions.filter(s => !dismissedSug[s.id]), r = latestReport;
  $("view").innerHTML = `
    <div class="section-head"><h2>Insights</h2>${sample}<span class="hint">${esc(insightRange.label)} · computed ${rel(insightRange.computed)} · history begins ${esc(insightRange.since)} · numbers only, no model involved</span></div>
    ${open.length ? `<section aria-label="Suggestions"><div class="section-head"><h3>Suggestions</h3><span class="hint">rules over the numbers below; they advise, they never change a setting</span></div>
      <ul class="waste-list">${open.map(s => `<li class="alert"><b>${esc(s.title)}</b><span class="why">${esc(s.ev)}</span><button class="btn" data-sug="${s.id}">Dismiss</button>${dt("sg-" + s.id, `<span>Suggestion{kind SUGGESTION_KIND_${esc(s.kind)}, evidence[]} · DismissSuggestion{id, expected_version}</span>`)}</li>`).join("")}</ul></section>` : ""}
    <div class="section-head"><h3>Are the subscriptions used before they reset?</h3><span class="hint">last 4 weekly windows per account</span></div>
    <div class="caps">${efficiency.map(effCard).join("")}</div>
    <div class="section-head"><h3>What finished, by harness</h3><span class="hint">goals that ended in the range · a goal that never ran or was cancelled is not in the rates</span></div>
    <div class="cap"><table class="shown ins-table" aria-label="Goal outcomes by harness"><thead><tr><th>harness</th><th>ran</th><th>merged</th><th>time to merge (p50 / p90)</th><th>nudges / goal</th><th>stuck</th><th>review rounds / goal</th><th>restarts / goal</th><th>quota pts / merged</th></tr></thead>
      <tbody>${outcomes.map(outcomeRow).join("")}</tbody></table>
      <div class="why">quota points are percentage points of each account's weekly quota: compare them inside one account. Group by model, repo, account, machine or size in the real build.</div>
      ${dt("io", `<span>GetInsights{sections: OUTCOMES, group_by: HARNESS} → OutcomeRow{ran, merged, merge_rate_percent, time_to_merge_p50, p90, nudges_total, ever_stuck, review_rounds_total, restarts_total, quota_points_per_merged, low_confidence}</span>`)}</div>
    <div class="section-head"><h3>Where work gets stuck</h3><span class="hint">counts and times; never prompt or screen text</span></div>
    <div class="caps"><div class="cap"><div class="cap-name">Waiting on a person</div>${stuckList(stuck.prompts.map(p => ({ html: `${agentChip(p[0])} ${esc(p[1])} prompts: ${p[2]} on ${p[3]} goals, waited ${esc(p[4])}` })))}</div>
      <div class="cap"><div class="cap-name">Provider trouble</div>${stuckList([...stuck.conditions.map(c => ({ html: `${agentChip(c[0])} ${esc(c[1])} (${esc(c[2])}): ${c[3]} times on ${c[4]} goals, ${esc(c[5])} open` })), ...stuck.incidents.map(i => ({ cls: "bad", html: `incident on ${esc(i[0])}: ${esc(i[1])}, ${esc(i[2])}, ${i[3]} goals on ${i[4]} machines` }))])}</div>
      <div class="cap"><div class="cap-name">After updates and on start</div>${stuckList([...stuck.parser.map(p => ({ cls: "bad", html: `${agentChip(p[0])} usage parsing failed ${p[3]} of ${p[4]} times on ${esc(p[1])}, first seen soon after the update from ${esc(p[5])}` })), ...stuck.starts.map(s => ({ html: `${esc(s[0])} · ${agentChip(s[1])}: ${s[3]} start attempts failed (<span class="mono">${esc(s[2])}</span>)` }))])}</div></div>
    ${dt("is", `<span>GetInsights{sections: STUCK} → StuckInsights{prompts[] PromptBlockRow{prompt_class}, conditions[], incidents[], parser_failures[] ParserFailureRow{after_update}, start_failures[], stuck_reasons[]}</span>`)}
    <div class="section-head"><h3>Size estimate against what happened</h3><span class="hint">median time to merge per size</span></div>
    <div class="cap"><table class="shown ins-table" aria-label="Size accuracy"><tbody>${sizeAccuracy.map(s => `<tr><td>${esc(s[0])}</td><td class="num">${s[1]} merged</td><td class="num">median ${hrs(s[2])}</td><td class="why">${s[3] == null ? "" : `${s[3]}% took longer than the next size's median`}</td></tr>`).join("")}</tbody></table></div>
    <div class="section-head"><h3>Latest weekly report</h3><span class="hint">written by ${agentChip(r.harness)} ${esc(r.model)} ${rel(r.when)} · cost ${r.pts} points of the ${esc(r.bucket)} quota · spare quota only</span></div>
    <div class="cap"><div class="why">Model-written text, shown sanitized: no HTML, no images, links as plain text. The numbers it was written from are one click away.</div>
      <div class="report">${reportHtml(r.text)}</div>
      <div class="actions"><button class="btn" data-ins="full">Full report</button><button class="btn" data-ins="numbers">Show the numbers</button><button class="btn" data-ins="run">Run the report now…</button></div>
      <div class="why" id="insNote">Weekly, Monday 07:00 · runs only when an account is behind pace or would waste quota, never into your reserve · switch it off in Settings → Routines.</div>
      ${dt("ir", `<span>InsightReport{excerpt, text, usage_consumed, bundle_digest ${esc(r.digest)}, aliases[]} · ListInsightReports, GetInsightReport{include_bundle}</span><span>routine ROUTINE_BUILTIN_INSIGHTS_WEEKLY · done_rule REPORT_COLLECTED · only_spare_quota · worktree_mode SCRATCH</span>`)}</div>`;
  const v = $("view");
  v.querySelectorAll("[data-sug]").forEach(b => b.onclick = () => { dismissedSug[b.dataset.sug] = true; log(`dismissed suggestion (mock)`); rerender(); });
  v.querySelectorAll("[data-ins]").forEach(b => b.onclick = () => { $("insNote").textContent = b.dataset.ins === "run" ? "Mock: RunRoutine would start a run now; a spare-quota wait shows as ROUTINE_NEEDS_SPARE_QUOTA." : "Mock: opens GetInsightReport in the real build."; });
}
