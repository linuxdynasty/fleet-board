// Fleet: machines as resources you act on, agent capacity, settings, and the audit log.
function renderFleetView() {
  $("view").innerHTML = `
    <div class="section-head"><h2>Machines</h2><span class="hint">Drain = finish current work, take nothing new</span></div>
    <div class="fleet">${machines.map(m => {
      const ss = sessions.filter(s => s.m === m.id);
      const stale = Date.now() - m.beat > 30e3;
      return `<article class="fm${ui.fleetFocus === m.id ? " focus" : ""}" id="fm-${m.id}">
        <div class="top"><span class="dot ${stale ? "bad" : m.drained ? "warn" : "ok"}"></span><span class="name">${esc(m.name)}</span><span class="ip">${m.ip}</span></div>
        <div class="sub">${esc(m.user)} · ${esc(m.os)} · ${m.ram} GB${m.hub ? " · hub" : ""}</div>
        <div class="fm-stats"><span><b class="num">${busy(m)}/${m.slots}</b> agents</span><span>collector <b data-t="${+m.beat}">${rel(m.beat)}</b></span>
          ${m.drained ? `<span class="pill waiting">draining</span>` : `<span class="pill working">accepting work</span>`}</div>
        <div class="tools">${Object.entries(m.tools).map(([k, v]) => `<span class="tool ${v ? "" : "no"}">${k}</span>`).join("")}</div>
        <div class="fm-sess">${ss.map(s => `<button class="chip" data-sid="${s.id}"><span class="sdot ${liveState(s)}"></span>${esc(s.agent)} · ${esc(s.repo)}</button>`).join("") || `<span class="why">No sessions</span>`}</div>
        ${ui.confirm?.mid === m.id ? `<div class="confirm"><div>${esc(ui.confirm.label)}</div>
          <div class="actions"><button class="btn primary" data-mact="do" data-m="${m.id}">${esc(ui.confirm.go)}</button><button class="btn" data-mact="cancel" data-m="${m.id}">Cancel</button></div></div>` :
        `<div class="actions"><button class="btn" data-mact="${m.drained ? "resume" : "drain"}" data-m="${m.id}">${m.drained ? "Resume" : "Drain…"}</button>
          <button class="btn" data-mact="ssh" data-m="${m.id}">Shell…</button></div>`}
      </article>`;
    }).join("")}</div>

    <div class="section-head"><h2>Agent capacity</h2><span class="hint">Per account, shared by every machine. Tick = hold-back line.</span></div>
    <div class="caps">${capacityCards()}</div>

    <section class="section" id="settings" aria-label="Settings"></section>

    <div class="section-head"><h2>Audit log</h2><span class="hint">Every action from any view</span></div>
    <ol class="audit">${audit.map(a => `<li><span class="num">${hhmm(a.at)}</span><b>${esc(a.who)}</b><span>${esc(a.what)}</span><span class="mono why">${esc(a.where)}</span></li>`).join("")}</ol>`;

  renderSettings();
  $("view").querySelectorAll("[data-sid]").forEach(b => b.onclick = () => go("session", b.dataset.sid));
  $("view").querySelectorAll("[data-mact]").forEach(b => b.onclick = () => fleetAct(mById(b.dataset.m), b.dataset.mact));
  if (ui.fleetFocus) { $("fm-" + ui.fleetFocus)?.scrollIntoView({ block: "center" }); setTimeout(() => { ui.fleetFocus = null; }, 1500); }
}

function fleetAct(m, act) {
  const n = busy(m);
  if (act === "cancel") ui.confirm = null;
  if (act === "drain") ui.confirm = { mid: m.id, label: `Stop giving ${m.name} new work. Its ${n} running session${n === 1 ? "" : "s"} keep going until they finish.`, go: "Drain", what: "drained" };
  if (act === "resume") { m.drained = false; log(`resumed ${m.short}: accepting work`); }
  if (act === "ssh") ui.confirm = { mid: m.id, label: `ssh ${m.user}@${m.ip}`, go: "Copy command", what: "copied shell command", copy: `ssh ${m.user}@${m.ip}` };
  if (act === "do") {
    const c = ui.confirm;
    if (c.copy) navigator.clipboard?.writeText(c.copy).then(() => {}, () => {});
    else { m.drained = true; log(`drained ${m.short}`); }
    ui.confirm = null;
  }
  rerender();
}

function capacityCards() {
  return agents.map(a => {
    const h = headroom(a);
    const status = h === null ? ["unknown", "idle"] : h >= settings.headroomMin ? ["available", "working"] : ["held back", "stuck"];
    const running = sessions.filter(s => s.agent === a.id && s.state !== "done").length;
    const bars = a.windows.length ? a.windows.map(w => {
      const tone = w.used >= 100 - settings.headroomMin ? "var(--bad)" : w.used >= 60 ? "var(--warn)" : "var(--ok)";
      return `<div class="win"><div class="win-top"><span>${w.label}</span><span class="num">${w.used}% used · resets in ${until(w.resets)}</span></div>
        <div class="track"><div class="fill" style="width:${w.used}%;background:${tone}"></div><span class="mark" style="left:${100 - settings.headroomMin}%"></span></div></div>`;
    }).join("") : `<div class="win empty">No usage reading. Only used when you pick it by hand.</div>`;
    return `<div class="cap"><div class="cap-top">${agentChip(a.id)}<span class="cap-name">${a.label}</span><span class="pill ${status[1]}">${status[0]}</span></div>
      ${bars}<div class="src"><span class="${a.real ? "real" : "samp"}">${a.real ? "real" : "sample"}</span> ${esc(a.source.replace(/^(real|sample) · /, ""))} · ${running} running</div></div>`;
  }).join("") + `<div class="cap add"><div class="cap-name">Add an agent</div><div class="win empty">Needs a launch command, where it writes sessions, and where it reports usage.</div></div>`;
}
