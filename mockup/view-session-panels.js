// Session view panels: browser attach notes (J1), harness command palette (C4/F8), history (ADR 0010, v2),
// new session (F1). All rendered inside the Details drawer by view-session.js; actions wired there.

// J1: the terminal in the main area is a real tmux client in a PTY on the developer's own server, attached through a
// grouped view session so the browser's current window never moves the developer's own terminal.
function attachSection(s, m) {
  return `<section><h2>Browser attach</h2>
    <div class="why">Grouped session <span class="mono">${esc(viewSess(s))}</span>: same windows as <span class="mono">${esc(s.sess)}</span>, its own current window, destroy-unattached on. OSC 52 and hyperlinks stripped. Your prefix and bindings work unchanged (the mock emulates <span class="mono">${esc(settings.tmuxPrefix)}</span>); the window tabs cover shortcuts the browser reserves (Ctrl+W).</div>
    <label class="chk"><input type="checkbox" id="roToggle" ${T.ro ? "checked" : ""}> Read-only (input dropped on the hub and the agent)</label>
    <span class="mono small">${esc(attachCmd(s, T.ro))}</span>
    <div class="why">From your own terminal: <code class="inline">${esc(attachLine(m, s))}</code> <button class="btn sm" data-act="attachline">Copy</button></div></section>`;
}

// C4/F8: the catalogued commands for this harness install, grouped by class. DENY is never shown.
function palettePanel(s) {
  const cat = CATALOG[s.harness];
  if (!cat) return "";
  return `<div class="palette"><div class="section-head"><h2>Harness command · ${esc(s.harness)} ${esc(mById(s.m).versions?.[s.harness] ?? "")}</h2><span class="hint">catalog ${esc(cat.hash)} · discovered in the probe session · unknown commands are deny-class and never listed</span></div>
    <div class="pal-grid">${CLASSES.map(cls => `<div class="pal-col"><div class="pal-k ${cls}">${cls}${cls === "session" || cls === "review" ? " · operator" : cls === "destructive" ? " · confirm" : ""}</div>
      ${cat[cls].length ? cat[cls].map(c => `<button class="btn sm ${cls === "destructive" ? "danger" : ""}" data-cmd="${esc(c)}" data-cls="${cls}">${esc(c)}</button>`).join("") : `<span class="why">none</span>`}</div>`).join("")}</div>
    <div class="why">Each sends <span class="mono">RunHarnessCommand{pane, command, expected_class, catalog_hash}</span>; the agent refuses COMMAND_NOT_IN_CATALOG, CATALOG_STALE, COMMAND_CLASS_DENIED or PANE_BUSY. Deny class (/login, /logout, /permissions, /hooks, /config, /plugins, /usage-credits…) is never sendable.</div></div>`;
}

function paletteAct(s, command, cls) {
  const cmd = harnessCmd(s, command, cls);
  if (cls === "destructive") { ui.confirm = { sid: s.id, label: `${command} is destructive. Confirm; the machine may also require local approval:`, cmd, go: "Run " + command, danger: true, what: `ran ${command} (destructive)` }; return rerender(); }
  if (command === "/goal") { ui.confirm = { sid: s.id, label: "Use the goal controls (pause / resume / edit / clear) in the rail; they send /goal subcommands the harness supports.", cmd, go: "OK", what: "viewed /goal" }; return rerender(); }
  log(`ran ${command} (${cls}) · ${cmd}${cls === "observe" ? " · screen returned in RunHarnessCommandResult" : ""} (mock, not sent)`, s);
  if (cls === "observe") s.pane = [...s.pane, ["dim", `> ${command}`], ["dim", `(mock: ${command} output would appear here, ANSI stripped, 64 KiB cap)`]];
  rerender();
}

// ADR 0010 / K1: terminal log timeline and replay placeholder, sized with the measured numbers (01c).
function historyPanel(s) {
  const hrs = Math.max(0.05, (Date.now() - s.started) / 3600e3), raw = (hrs * RAW_MB_PER_H).toFixed(1), stored = (hrs * STORED_MB_PER_H).toFixed(2);
  const ev = history[s.id] ?? [["window created", s.yours ? "your own session: capture is off until you turn it on (SetLogCapture)" : "goal pane: terminal log captured by default"]];
  const steps = ev.length + 1, t0 = +s.started, step = (Date.now() - t0) / steps;
  return `<div class="hist"><div class="section-head"><h2>History · v2</h2><span class="hint">tmux pipe-pane → spool → zstd chunks by byte offset → hub files, indexed in SQLite</span></div>
    <ol class="timeline">${ev.map((e, i) => `<li><span class="num">${hhmm(t0 + step * i)}</span> <b>${esc(e[0])}</b> ${esc(e[1])}</li>`).join("")}<li><span class="num">${hhmm(Date.now())}</span> <b>now</b> live</li></ol>
    <div class="replay"><div class="replay-bar"><button class="btn sm" disabled>▶ replay</button><span class="track"><i style="width:100%"></i></span><span class="num">${hrs.toFixed(1)} h</span><span class="why">placeholder: terminal-style replay with time navigation and search lands with v2</span></div></div>
    <div class="meta-line"><span>raw ≈ <b>${raw} MB</b> (≈27 MB/h, mostly TUI redraw)</span><span>stored ≈ <b>${stored} MB</b> (≈0.23 MB/h, zstd-3, 115×)</span><span>harness transcript: v3, opt-in, shipped by offset</span><span>${s.yours ? "capture: off (your session)" : "capture: on (goal pane)"}</span><span>readable by owner only; delete is one audited action</span></div></div>`;
}

// F1 EnsureSession on the developer's tmux server: create or reuse by name, in an allowed repo root,
// optionally starting a harness (OpenCode included).
function newSessionPanel(m) {
  const st = ui.newState ?? (ui.newState = { name: "scratch", repo: settings.repos[0].repo, harness: "", reuse: true });
  const exists = sessions.some(s => s.m === m.id && s.sess === st.name);
  const ok = /^[a-z0-9][a-z0-9-]{0,31}$/.test(st.name);
  return `<div class="confirm newsess"><div><b>New session on ${esc(m.name)}</b> · shows up in your own <span class="mono">tmux ls</span>, tagged FLEET_SESSION_ID / FLEET_OWNER so the board knows it is fleet-started.</div>
    <div class="disp">
      <label>Name <input class="field mono" id="nsName" value="${esc(st.name)}" placeholder="[a-z0-9-]"></label>
      <label>Repo <select id="nsRepo">${settings.repos.map(r => `<option value="${esc(r.repo)}" ${r.repo === st.repo ? "selected" : ""}>${esc(r.host)}/${esc(r.repo)}</option>`).join("")}</select></label>
      <label>Harness <select id="nsHarness"><option value="">login shell</option>${m.harnesses.map(h => `<option value="${h}" ${h === st.harness ? "selected" : ""}>${h} ${esc(m.versions?.[h] ?? "")}</option>`).join("")}</select></label>
      <label class="chk"><input type="checkbox" id="nsReuse" ${st.reuse ? "checked" : ""}> Reuse/attach if a session of that name exists${exists ? " (it does)" : ""}</label>
    </div>
    ${ok ? "" : `<p class="bad-t">Name must match [a-z0-9][a-z0-9-]{0,31} (INVALID_NAME). Your own sessions may have any name; fleet-created ones are restricted.</p>`}
    <code>${esc(ensureSessionCmd({ id: `sess-${Date.now().toString(36).slice(-4)}`, name: st.name, repo: st.repo, harness: st.harness, reuse: st.reuse }))}</code>
    <div class="why">The agent joins the repo to an allowed root (never an absolute path from the hub), resolves the harness path locally, and pre-trusts the folder. ${st.harness === "opencode" ? "OpenCode: inventory, sessions and attach only; no goals until the generic goal protocol." : ""}</div>
    <div class="actions"><button class="btn primary" data-act="newgo" ${ok ? "" : "disabled"}>${exists && st.reuse ? "Reuse session" : "Create session"}</button><button class="btn" data-act="newcancel">Cancel</button></div></div>`;
}

function newSessionGo() {
  const st = ui.newState, m = mById(ui.newOpen), exists = sessions.find(s => s.m === m.id && s.sess === st.name);
  if (exists && st.reuse) { log(`reused session ${st.name} on ${m.short} (EnsureSession reuse=true, created=false; mock)`, exists); ui.newOpen = null; return go("session", exists.id); }
  const id = "n" + Date.now().toString(36).slice(-5), h = st.harness || "shell";
  const s = { id, m: m.id, harness: h, sess: st.name, idx: 0, win: h === "shell" ? "zsh" : h, paneId: `%${60 + sessions.length}`, repo: st.repo, line: null, last: new Date(), started: new Date(), tasks: [], idle: h !== "shell", available: false, fleetSess: true,
    pane: [["dim", `EnsureSession{name: ${st.name}} → created=true`], ["", h === "shell" ? `$ ` : `${h} starting in ~/repos/${shortName(st.repo)}…`]] };
  s.target = `${s.sess}:0`;
  sessions.push(s);
  log(`created session ${st.name} on ${m.short}${h !== "shell" ? ` with ${h}` : ""} (EnsureSession; mock, nothing ran)`, s);
  ui.newOpen = null; go("session", id);
}
