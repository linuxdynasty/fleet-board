// App shell: helpers, derived state, routing, summary strip, switcher, keyboard.
// Every dynamic string goes through esc() before it reaches innerHTML.
const $ = id => document.getElementById(id);
const esc = s => String(s).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const rel = d => { const s = Math.max(0, Math.round((Date.now() - d) / 1000)); return s < 60 ? s + "s ago" : s < 3600 ? Math.floor(s / 60) + "m ago" : s < 86400 ? Math.floor(s / 3600) + "h ago" : Math.floor(s / 86400) + "d ago"; };
const until = d => { const s = Math.round((d - Date.now()) / 1000); return s <= 0 ? "now" : s < 3600 ? Math.ceil(s / 60) + "m" : s < 86400 ? Math.floor(s / 3600) + "h " + Math.floor(s % 3600 / 60) + "m" : Math.floor(s / 86400) + "d " + Math.floor(s % 86400 / 3600) + "h"; };
const hhmm = d => new Date(d).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const mById = id => machines.find(m => m.id === id);
const sById = id => sessions.find(s => s.id === id);
const qById = id => questions.find(q => q.id === id);
const agentColor = { claude: "var(--accent)", codex: "var(--ok)", muse: "var(--warn)", shell: "var(--idle)" };
const agentChip = a => `<span class="agent" style="color:${agentColor[a] ?? "var(--fg)"}">${esc(a)}</span>`;
const where = s => `${mById(s.m).short} ${s.target}`;

const ui = { view: "triage", selected: "s5", triageIdx: 0, answered: {}, choice: {}, snoozed: {}, dismissed: {}, confirm: null, switcher: false, swIdx: 0, swQuery: "" };
const VIEWS = ["triage", "work", "session", "fleet"];

function log(what, s) { audit.unshift({ at: new Date(), who: "you", what, where: s ? where(s) : "", s: s?.id }); }
const liveState = s => (s.q && ui.answered[s.q] && s.state === "waiting") ? "working" : s.state;
const sessionOrder = () => machines.flatMap(m => sessions.filter(s => s.m === m.id));

// Work items: every watched PR and backlog issue, with a lifecycle stage.
function stageOf(it) {
  const s = sessionFor(it);
  if (s) { const st = liveState(s); return st === "waiting" ? "needs" : st === "stuck" ? "stuck" : st === "done" ? "review" : "running"; }
  if (it.kind === "issue") return "queued";
  return it.ci.fail ? "cired" : "review";
}
const sessionFor = it => it.kind === "pr" ? onBranch(it) : sessions.find(s => s.ref === keyOf(it) && s.state !== "done");
const workItems = () => [...backlog, ...prs].filter(it => watched(it.repo));

// Triage: everything that wants a decision, most urgent first.
function triageItems() {
  const out = [];
  questions.forEach(q => { if (!ui.answered[q.id] && !ui.snoozed[q.id]) out.push({ type: "question", id: q.id, q, s: sById(q.s), rank: q.kind === "perm" ? 0 : 1 }); });
  sessions.filter(s => s.state === "stuck" && !ui.snoozed[s.id]).forEach(s => out.push({ type: "stuck", id: s.id, s, rank: 2 }));
  prs.filter(p => watched(p.repo) && p.ci.fail && !onBranch(p) && !ui.dismissed[keyOf(p)])
    .forEach(p => out.push({ type: "suggest", id: keyOf(p), pr: p, rank: 3 }));
  return out.sort((a, b) => a.rank - b.rank || ((a.q?.deadline ?? 9e15) - (b.q?.deadline ?? 9e15)));
}

function counts() {
  const live = sessions.map(liveState);
  return { working: live.filter(s => s === "working").length, needs: triageItems().length, stuck: live.filter(s => s === "stuck").length,
    free: machines.reduce((n, m) => n + free(m), 0), slots: machines.reduce((n, m) => n + (m.drained ? 0 : m.slots), 0) };
}

function renderStrip() {
  const c = counts();
  $("nav").innerHTML = VIEWS.map((v, i) => `<a href="#${v}" class="tab" aria-current="${ui.view === v ? "page" : "false"}">
    <span class="k">${i + 1}</span>${v === "triage" ? `Triage <b class="badge">${c.needs}</b>` : v[0].toUpperCase() + v.slice(1)}</a>`).join("");
  $("summary").innerHTML = `<span><b>${c.working}</b> working</span><span class="warn"><b>${c.needs}</b> need you</span>
    <span class="bad"><b>${c.stuck}</b> stuck</span><span><b>${c.free}/${c.slots}</b> slots free</span>
    ${agents.map(a => { const h = headroom(a); const tone = h === null ? "idle" : h < settings.headroomMin ? "bad" : h < 40 ? "warn" : "ok";
      return `<span class="use">${agentChip(a.id)}<span class="mini"><i class="${tone}" style="width:${h ?? 0}%"></i></span><span class="num">${h === null ? "?" : h + "%"}</span></span>`; }).join("")}`;
}

function rerender() {
  renderStrip();
  ({ triage: renderTriage, work: renderWorkView, session: renderSessionView, fleet: renderFleetView })[ui.view]();
  renderSwitcher();
}
const renderAll = rerender; // used by the dispatcher

function go(view, sessionId) {
  if (sessionId) ui.selected = sessionId;
  ui.confirm = null;
  if (location.hash !== "#" + view) location.hash = view; else { ui.view = view; rerender(); }
}
window.addEventListener("hashchange", () => { const v = location.hash.slice(1); if (VIEWS.includes(v)) { ui.view = v; ui.confirm = null; rerender(); window.scrollTo(0, 0); } });

// Jump-to switcher (⌘K or /): sessions first, then machines, work items and views.
function switcherEntries() {
  const e = [];
  sessionOrder().forEach(s => e.push({ label: `${s.agent} · ${where(s)} · ${s.repo}`, sub: s.task, tag: liveState(s), run: () => go("session", s.id) }));
  machines.forEach(m => e.push({ label: `${m.name} · ${m.ip}`, sub: `${busy(m)}/${m.slots} agents · ${m.ram} GB`, tag: "machine", run: () => { ui.fleetFocus = m.id; go("fleet"); } }));
  workItems().forEach(it => e.push({ label: `${shortName(it.repo)}#${it.n}`, sub: it.t, tag: it.kind, run: () => { ui.workFocus = keyOf(it); go("work"); } }));
  VIEWS.forEach(v => e.push({ label: `Go to ${v}`, sub: "", tag: "view", run: () => go(v) }));
  const terms = ui.swQuery.toLowerCase().split(/\s+/).filter(Boolean);
  return e.filter(x => terms.every(t => (x.label + " " + x.sub + " " + x.tag).toLowerCase().includes(t)));
}

function renderSwitcher() {
  const box = $("switcher");
  box.hidden = !ui.switcher;
  if (!ui.switcher) return;
  const list = switcherEntries().slice(0, 12);
  ui.swIdx = Math.min(ui.swIdx, Math.max(0, list.length - 1));
  $("swList").innerHTML = list.length ? list.map((x, i) => `<li role="option" aria-selected="${i === ui.swIdx}" data-i="${i}">
      <span class="sw-label">${esc(x.label)}</span><span class="sw-sub">${esc(x.sub)}</span><span class="pill ${esc(x.tag)}">${esc(x.tag)}</span></li>`).join("")
    : `<li class="sw-empty">Nothing matches. Try a machine, repo, agent or issue number.</li>`;
  $("swList").querySelectorAll("li[data-i]").forEach(li => li.onclick = () => { closeSwitcher(); list[+li.dataset.i].run(); });
}
function openSwitcher() { ui.switcher = true; ui.swIdx = 0; ui.swQuery = ""; $("swInput").value = ""; renderSwitcher(); $("swInput").focus(); }
function closeSwitcher() { ui.switcher = false; renderSwitcher(); }

function nextNeedsYou() {
  const order = sessionOrder().filter(s => ["waiting", "stuck"].includes(liveState(s)));
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
    sById("s1").last = new Date(Date.now() - 4000); sById("s4").last = new Date();
    machines.forEach(m => { m.beat = new Date(Date.now() - 1000 * (1 + Math.floor(Math.random() * 5))); });
  }
  $("upd").textContent = `updated ${Math.round((Date.now() - lastRefresh) / 1000)}s ago`;
  document.querySelectorAll("[data-timer]").forEach(el => { el.textContent = timerText(qById(el.dataset.timer)); });
  document.querySelectorAll("[data-t]").forEach(el => { el.textContent = rel(+el.dataset.t); });
}

function timerText(q) {
  if (ui.answered[q.id]) return "";
  if (!q.deadline) return "Waits for you. No auto-default.";
  const ms = q.deadline - Date.now();
  if (ms <= 0) return "Default applied";
  return `Default in ${Math.floor(ms / 60000)}:${String(Math.floor(ms % 60000 / 1000)).padStart(2, "0")}`;
}
