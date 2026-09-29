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
const harnessColor = { claude: "var(--accent)", codex: "var(--ok)", muse: "var(--warn)", shell: "var(--idle)" };
const agentChip = h => `<span class="agent" style="color:${harnessColor[h] ?? "var(--fg)"}">${esc(h)}</span>`;
const where = s => `${mById(s.m).short} ${s.target}`;
const ref = s => s.issue ? `${shortName(s.repo)}#${s.issue}` : s.pr ? `${shortName(s.repo)} PR #${s.pr}` : s.repo;

const ui = { view: "triage", selected: "g2", triageIdx: 0, answered: {}, choice: {}, snoozed: {}, dismissed: {}, confirm: null, switcher: false, swIdx: 0, swQuery: "" };
const VIEWS = ["triage", "work", "session", "fleet"];
const STATE_PILL = { working: "working", waiting: "waiting", stuck: "stuck", verifying: "verifying", merged: "done", none: "idle" };

function log(what, s, who = "you") { audit.unshift({ at: new Date(), who, what, where: s ? where(s) : "", s: s?.id }); }
const sessionOrder = () => machines.flatMap(m => sessions.filter(s => s.m === m.id));
const liveState = s => s.harness === "shell" ? "none" : goalState(s);

// Work items: watched PRs and issues, with a lifecycle stage.
const goalFor = it => sessions.find(s => isGoal(s) && s.repo === it.repo && (it.kind === "pr" ? s.pr === it.n : s.issue === it.n) && goalState(s) !== "merged");
function stageOf(it) {
  if (it.kind === "pr" && it.state === "merged") return "merged";
  const s = goalFor(it) ?? (it.kind === "issue" ? sessions.find(x => x.repo === it.repo && x.issue === it.n) : null);
  if (s) { const st = goalState(s); return st === "merged" ? "merged" : st; }
  if (it.kind === "issue") return "queued";
  return "unowned";
}
function workItems() {
  const issuesFromGoals = sessions.filter(s => isGoal(s) && s.issue && !backlog.some(b => b.repo === s.repo && b.n === s.issue) && !prs.some(p => p.issues.includes(s.issue)))
    .map(s => ({ kind: "issue", repo: s.repo, n: s.issue, t: s.line?.startsWith("Drive") ? s.line : `Issue #${s.issue}` }));
  return [...backlog, ...issuesFromGoals, ...prs].filter(it => watched(it.repo));
}

// Triage: only things that need a person.
function triageItems() {
  const out = [];
  permissions.forEach(p => { if (!ui.answered[p.id] && !ui.snoozed[p.id]) out.push({ type: "perm", id: p.id, p, s: sById(p.s), rank: 0 }); });
  sessions.filter(s => isGoal(s) && goalState(s) === "stuck" && !ui.snoozed[s.id]).forEach(s => out.push({ type: "stuck", id: s.id, s, rank: 1 }));
  sessions.filter(s => isGoal(s) && goalState(s) === "verifying" && !ui.snoozed[s.id]).forEach(s => out.push({ type: "verify", id: s.id, s, rank: 2 }));
  accounts.filter(a => pace(a).state === "reserve" && !ui.dismissed[a.id]).forEach(a => out.push({ type: "pace", id: a.id, a, rank: 3 }));
  return out.sort((x, y) => x.rank - y.rank);
}

function counts() {
  const st = sessions.filter(isGoal).map(goalState);
  const c = k => st.filter(x => x === k).length;
  return { working: c("working"), waiting: c("waiting"), stuck: c("stuck"), needs: triageItems().length,
    freeA: machines.reduce((n, m) => n + freeActive(m), 0), slotsA: machines.reduce((n, m) => n + (m.drained ? 0 : m.maxActive), 0) };
}

function renderStrip() {
  const c = counts();
  $("nav").innerHTML = VIEWS.map((v, i) => `<a href="#${v}" class="tab" aria-current="${ui.view === v ? "page" : "false"}">
    <span class="k">${i + 1}</span>${v === "triage" ? `Triage <b class="badge">${c.needs}</b>` : v[0].toUpperCase() + v.slice(1)}</a>`).join("");
  $("summary").innerHTML = `<span><b>${c.working}</b> working</span><span><b>${c.waiting}</b> waiting on CI/review</span>
    <span class="bad"><b>${c.stuck}</b> stuck</span><span class="warn"><b>${c.needs}</b> need you</span><span><b>${c.freeA}/${c.slotsA}</b> active slots free</span>
    <span class="auto ${settings.autoDispatch ? "on" : ""}">auto-dispatch ${settings.autoDispatch ? "on" : "off"}</span>
    ${accounts.map(a => { const p = pace(a); const tone = p.state === "reserve" ? "bad" : p.state === "ahead" ? "warn" : "ok";
      return `<span class="use" title="${esc(`${a.label}: ${p.used}% used, pace line ${Math.round(p.target)}%, ${PACE_LABEL[p.state]}`)}">${agentChip(a.harness)}
        <span class="mini"><i class="${tone}" style="width:${p.used}%"></i><s style="left:${p.target}%"></s></span><span class="num">${p.used}%</span></span>`; }).join("")}`;
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
  sessionOrder().forEach(s => e.push({ label: `${s.harness} · ${where(s)} · ${ref(s)}`, sub: s.line ?? s.repo, tag: STATE_PILL[liveState(s)], run: () => go("session", s.id) }));
  machines.forEach(m => e.push({ label: `${m.name} · ${m.ip}`, sub: `${activeOn(m)}/${m.maxActive} active · ${totalOn(m)}/${m.maxTotal} total · ${m.ram} GB`, tag: "machine", run: () => { ui.fleetFocus = m.id; go("fleet"); } }));
  accounts.forEach(a => e.push({ label: `${a.label} account`, sub: `${used(a)}% used · ${PACE_LABEL[pace(a).state]}`, tag: "account", run: () => go("fleet") }));
  workItems().forEach(it => e.push({ label: `${shortName(it.repo)}#${it.n}`, sub: it.t, tag: it.kind, run: () => { ui.workFocus = keyOf(it); go("work"); } }));
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
    : `<li class="sw-empty">Nothing matches. Try a machine, repo, harness or issue number.</li>`;
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
