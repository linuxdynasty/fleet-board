// App shell: helpers, derived state, routing, summary strip, switcher, keyboard.
// Every dynamic string goes through esc() before it reaches innerHTML.
const $ = id => document.getElementById(id);
const esc = s => String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const rel = d => { const s = Math.max(0, Math.round((Date.now() - d) / 1000)); return s < 60 ? s + "s ago" : s < 3600 ? Math.floor(s / 60) + "m ago" : s < 86400 ? Math.floor(s / 3600) + "h ago" : Math.floor(s / 86400) + "d ago"; };
const until = d => { const s = Math.round((d - Date.now()) / 1000); return s <= 0 ? "now" : s < 3600 ? Math.ceil(s / 60) + "m" : s < 86400 ? Math.floor(s / 3600) + "h " + Math.floor(s % 3600 / 60) + "m" : Math.floor(s / 86400) + "d " + Math.floor(s % 86400 / 3600) + "h"; };
const hhmm = d => new Date(d).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const mById = id => machines.find(m => m.id === id);
const sById = id => sessions.find(s => s.id === id);
const pById = id => permissions.find(p => p.id === id);
const cById = id => conditions.find(c => c.id === id);
const harnessColor = { claude: "var(--accent)", codex: "var(--ok)", muse: "var(--warn)", opencode: "var(--fg)", shell: "var(--idle)" };
const agentChip = h => `<span class="agent" style="color:${harnessColor[h] ?? "var(--fg)"}">${esc(h)}</span>`;
const provChip = p => p ? `<span class="prov ${esc(p)}" title="${esc(PROVIDER[p] ?? p)}">${esc(PROVIDER[p] ?? p)}</span>` : "";
const where = s => `${mById(s.m).short} ${s.target}`;
const ref = s => s.display ?? (s.repo ? shortName(s.repo) : s.win);

const ui = { view: "triage", selected: "g2", triageIdx: 0, answered: {}, choice: {}, snoozed: {}, dismissed: {}, confirm: null, switcher: false, swIdx: 0, swQuery: "", attachRO: false };
const VIEWS = ["triage", "work", "session", "fleet"];
const STATE_PILL = { working: "working", waiting: "waiting", stuck: "stuck", verifying: "verifying", merged: "done", none: "idle", idle: "idle", available: "available" };

function log(what, s, who = "you") { audit.unshift({ at: new Date(), who, what, where: s ? where(s) : "", s: s?.id }); }
const sessionOrder = () => machines.flatMap(m => sessions.filter(s => s.m === m.id).sort((a, b) => (a.sess === "fleet") - (b.sess === "fleet") || a.sess.localeCompare(b.sess) || a.idx - b.idx));
// Live state: goal state for fleet panes; idle/available for the developer's own harness windows (E11).
const liveState = s => s.harness === "shell" ? "none" : s.yours ? (s.available ? "available" : s.idle ? "idle" : "none") : goalState(s);
const activeCond = s => s.cond && !ui.snoozed[s.cond] ? cById(s.cond) : null;

// Work items: watched change requests and issues/tickets, with a lifecycle stage.
const goalFor = it => sessions.find(s => isGoal(s) && s.repo === it.repo && (it.kind === "pr" ? s.pr === it.n : s.issue === it.n) && goalState(s) !== "merged");
function stageOf(it) {
  if (it.kind === "pr" && it.state === "merged") return "merged";
  const s = goalFor(it) ?? (it.kind === "issue" ? sessions.find(x => isGoal(x) && x.repo === it.repo && x.issue === it.n) : null);
  if (s) { const st = goalState(s); return st === "merged" ? "merged" : st; }
  if (it.kind === "issue") return "queued";
  return "unowned";
}
function workItems() {
  const issuesFromGoals = sessions.filter(s => isGoal(s) && s.issue && !backlog.some(b => b.repo === s.repo && b.n === s.issue) && !prs.some(p => p.repo === s.repo && p.issues.includes(s.issue)))
    .map(s => ({ kind: "issue", repo: s.repo, host: s.host ?? "github.com", provider: s.provider ?? "github", n: s.issue, display: s.display, t: s.line?.startsWith("Drive") ? s.line : `Issue ${s.display}` }));
  return [...backlog, ...issuesFromGoals, ...prs].filter(it => watched(it.repo));
}

// Triage: only things that need a person. Conditions (E12) are listed apart from stuck goals.
function triageItems() {
  const out = [];
  permissions.forEach(p => { if (!ui.answered[p.id] && !ui.snoozed[p.id]) out.push({ type: "perm", id: p.id, p, s: sById(p.s), rank: 0 }); });
  sessions.filter(s => isGoal(s) && goalState(s) === "stuck" && !ui.snoozed[s.id]).forEach(s => out.push({ type: "stuck", id: s.id, s, rank: 1 }));
  conditions.filter(c => !ui.snoozed[c.id] && sById(c.s)).forEach(c => out.push({ type: "cond", id: c.id, c, s: sById(c.s), rank: c.retrying ? 2 : 1 }));
  sessions.filter(s => isGoal(s) && goalState(s) === "verifying" && !ui.snoozed[s.id]).forEach(s => out.push({ type: "verify", id: s.id, s, rank: 3 }));
  accounts.filter(a => pace(a).state === "reserve" && !ui.dismissed[a.id]).forEach(a => out.push({ type: "pace", id: a.id, a, rank: 4 }));
  return out.sort((x, y) => x.rank - y.rank);
}

function counts() {
  const st = sessions.filter(isGoal).map(goalState);
  const c = k => st.filter(x => x === k).length;
  return { working: c("working"), waiting: c("waiting"), stuck: c("stuck"), needs: triageItems().length,
    avail: sessions.filter(s => liveState(s) === "available" || (!s.yours && goalState(s) === "merged" && !s.stopped)).length,
    freeA: machines.reduce((n, m) => n + freeActive(m), 0), slotsA: machines.reduce((n, m) => n + (m.drained ? 0 : m.maxActive), 0) };
}

function renderStrip() {
  const c = counts();
  $("nav").innerHTML = VIEWS.map((v, i) => `<a href="#${v}" class="tab" aria-current="${ui.view === v ? "page" : "false"}">
    <span class="k">${i + 1}</span>${v === "triage" ? `Triage <b class="badge">${c.needs}</b>` : v[0].toUpperCase() + v.slice(1)}</a>`).join("");
  $("summary").innerHTML = `<span><b>${c.working}</b> working</span><span><b>${c.waiting}</b> waiting on CI/review</span>
    <span class="bad"><b>${c.stuck}</b> stuck</span><span class="warn"><b>${c.needs}</b> need you</span><span><b>${c.freeA}/${c.slotsA}</b> active slots free</span><span><b>${c.avail}</b> available session${c.avail === 1 ? "" : "s"}</span>
    <span class="auto ${settings.autoDispatch ? "on" : ""}">auto-dispatch ${settings.autoDispatch ? "on" : "off"}</span>
    ${accounts.map(a => { const p = pace(a); const tone = p.state === "reserve" ? "bad" : p.state === "ahead" ? "warn" : "ok";
      return `<span class="use" title="${esc(`${a.label}: ${p.used}% used, pace line ${Math.round(p.target)}%, ${PACE_LABEL[p.state]}`)}">${agentChip(a.harness)}
        <span class="mini"><i class="${tone}" style="width:${p.used}%"></i><s style="left:${p.target}%"></s></span><span class="num">${p.used}%</span></span>`; }).join("")}
    ${apiAccounts.map(a => `<span class="use" title="${esc(`${a.label}: $${a.spentUsd} of $${a.capUsd} cap, manual only`)}">${agentChip("opencode")}<span class="num">$${a.spentUsd.toFixed(0)}/${a.capUsd}</span></span>`).join("")}`;
}

function rerender() {
  renderStrip();
  ({ triage: renderTriage, work: renderWorkView, session: renderSessionView, fleet: renderFleetView })[ui.view]();
  renderSwitcher();
}
const renderAll = rerender;

function go(view, sessionId) {
  if (sessionId) ui.selected = sessionId;
  ui.confirm = null;
  if (location.hash !== "#" + view) location.hash = view; else { ui.view = view; rerender(); }
}
window.addEventListener("hashchange", () => { const v = location.hash.slice(1); if (VIEWS.includes(v)) { ui.view = v; ui.confirm = null; rerender(); window.scrollTo(0, 0); } });

function switcherEntries() {
  const e = [];
  sessionOrder().forEach(s => e.push({ label: `${s.win} · ${where(s)}`, sub: `${s.harness} · ${s.line ?? (s.yours ? "yours" : s.repo)}`, tag: STATE_PILL[liveState(s)], run: () => go("session", s.id) }));
  machines.forEach(m => e.push({ label: `${m.name} · ${m.ip}`, sub: `${activeOn(m)}/${m.maxActive} active · ${totalOn(m)}/${m.maxTotal} total · ${m.ram} GB · ${m.labels.join(", ")}`, tag: "machine", run: () => { ui.fleetFocus = m.id; go("fleet"); } }));
  accounts.forEach(a => e.push({ label: `${a.label} account`, sub: `${used(a)}% used · ${PACE_LABEL[pace(a).state]}`, tag: "account", run: () => go("fleet") }));
  apiAccounts.forEach(a => e.push({ label: `${a.label} account`, sub: `$${a.spentUsd} of $${a.capUsd} · manual only`, tag: "account", run: () => go("fleet") }));
  workItems().forEach(it => e.push({ label: it.display, sub: `${PROVIDER[it.provider]} · ${it.t}`, tag: it.kind === "pr" ? "change request" : it.kindLabel ?? "issue", run: () => { ui.workFocus = keyOf(it); go("work"); } }));
  VIEWS.forEach(v => e.push({ label: `Go to ${v}`, sub: "", tag: "view", run: () => go(v) }));
  const terms = ui.swQuery.toLowerCase().split(/\s+/).filter(Boolean);
  return e.filter(x => terms.every(t => (x.label + " " + x.sub + " " + x.tag).toLowerCase().includes(t)));
}

function renderSwitcher() {
  $("switcher").hidden = !ui.switcher;
  if (!ui.switcher) return;
  const list = switcherEntries().slice(0, 12);
  ui.swIdx = Math.min(ui.swIdx, Math.max(0, list.length - 1));
  $("swList").innerHTML = list.length ? list.map((x, i) => `<li role="option" aria-selected="${i === ui.swIdx}" data-i="${i}">
      <span class="sw-label">${esc(x.label)}</span><span class="sw-sub">${esc(x.sub)}</span><span class="pill ${esc(x.tag)}">${esc(x.tag)}</span></li>`).join("")
    : `<li class="sw-empty">Nothing matches. Try a machine, repo, harness, window name or work item.</li>`;
  $("swList").querySelectorAll("li[data-i]").forEach(li => li.onclick = () => { closeSwitcher(); list[+li.dataset.i].run(); });
}
function openSwitcher() { ui.switcher = true; ui.swIdx = 0; ui.swQuery = ""; $("swInput").value = ""; renderSwitcher(); $("swInput").focus(); }
function closeSwitcher() { ui.switcher = false; renderSwitcher(); }

function nextNeedsYou() {
  const ids = [...new Set(triageItems().map(t => t.s?.id).filter(Boolean))];
  const order = sessionOrder().filter(s => ids.includes(s.id));
  if (!order.length) return;
  const i = order.findIndex(s => s.id === ui.selected);
  go("session", order[(i + 1) % order.length].id);
}

document.addEventListener("keydown", e => {
  const typing = /INPUT|SELECT|TEXTAREA/.test(e.target.tagName);
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); ui.switcher ? closeSwitcher() : openSwitcher(); return; }
  if (ui.switcher) {
    const list = switcherEntries().slice(0, 12);
    if (e.key === "Escape") closeSwitcher();
    else if (e.key === "ArrowDown") { e.preventDefault(); ui.swIdx = Math.min(ui.swIdx + 1, list.length - 1); renderSwitcher(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); ui.swIdx = Math.max(ui.swIdx - 1, 0); renderSwitcher(); }
    else if (e.key === "Enter" && list[ui.swIdx]) { e.preventDefault(); closeSwitcher(); list[ui.swIdx].run(); }
    return;
  }
  if (typing) { if (e.key === "Escape") e.target.blur(); return; }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === "/") { e.preventDefault(); openSwitcher(); }
  else if (/^[1-4]$/.test(e.key)) go(VIEWS[+e.key - 1]);
  else if (e.key === "n") nextNeedsYou();
  else if (ui.view === "triage") triageKey(e);
  else if (ui.view === "session") sessionKey(e);
});

function boot() {
  $("swInput").oninput = e => { ui.swQuery = e.target.value; ui.swIdx = 0; renderSwitcher(); };
  $("swClose").onclick = closeSwitcher;
  $("swBtn").onclick = openSwitcher;
  $("switcher").onclick = e => { if (e.target.id === "switcher") closeSwitcher(); };
  const v = location.hash.slice(1);
  if (VIEWS.includes(v)) ui.view = v;
  rerender(); tick(); setInterval(tick, 1000);
}

let lastRefresh = Date.now();
function tick() {
  $("clock").textContent = new Date().toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  if (Date.now() - lastRefresh >= 10000) {
    lastRefresh = Date.now();
    sById("g2").last = new Date(Date.now() - 4000); sById("s0").last = new Date();
    machines.forEach(m => { m.beat = new Date(Date.now() - 1000 * (1 + Math.floor(Math.random() * 5))); });
  }
  $("upd").textContent = `updated ${Math.round((Date.now() - lastRefresh) / 1000)}s ago`;
  document.querySelectorAll("[data-t]").forEach(el => { el.textContent = rel(+el.dataset.t); });
  document.querySelectorAll("[data-until]").forEach(el => { el.textContent = until(+el.dataset.until); });
}
