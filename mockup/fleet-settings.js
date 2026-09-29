// Settings panel. In the real hub these live in fleet.yaml; here they only change the page.
let settingsOpen = true;
const CONSENT_OPTS = ["push", "pr-open", "merge"];

function fleetYaml() {
  const l = ["repos:"];
  settings.repos.filter(r => r.on).forEach(r => l.push(`  - ${r.repo}`));
  l.push("dispatch:", `  auto: ${settings.autoDispatch}`, `  consent: [${settings.consent.join(", ")}]`, "  pacing: even", "  prefer_non_hub: true", "accounts:");
  accounts.forEach(a => l.push(`  - name: ${a.id}`, `    harness: ${a.harness}`, `    reserve_percent: ${a.reserve}`, `    usage_command: ${a.source}`));
  l.push("machines:");
  machines.forEach(m => l.push(`  - name: ${m.name}`, `    host: ${m.user}@${m.ip}`, `    ram_gb: ${m.ram}`, `    max_active: ${m.maxActive}`, `    max_total: ${m.maxTotal}`, `    harnesses: [${m.harnesses.join(", ")}]`));
  return l.join("\n");
}

function renderSettings() {
  $("settings").innerHTML = `<details id="settingsBox" ${settingsOpen ? "open" : ""}>
    <summary>Settings <span class="hint">${settings.repos.filter(r => r.on).length} repos · auto-dispatch ${settings.autoDispatch ? "on" : "off"} · consent ${esc(settings.consent.join(", "))}</span></summary>
    <div class="set-grid">
      <fieldset><legend>Watched repos</legend>
        ${settings.repos.map((r, i) => `<label class="chk"><input type="checkbox" id="repo-${i}" data-repo="${i}" ${r.on ? "checked" : ""}> <span class="mono">${esc(r.repo)}</span></label>`).join("")}
        <div class="add-row"><input id="newRepo" placeholder="owner/repo" aria-label="Add repo"><button class="btn" id="addRepo">Add</button></div>
        <div class="why" id="repoErr"></div>
      </fieldset>
      <fieldset><legend>Goals</legend>
        <label class="chk"><input type="checkbox" id="autoD" ${settings.autoDispatch ? "checked" : ""}> Start the next goal when a worker frees up</label>
        <div class="why">Consent written into every dispatched goal:</div>
        ${CONSENT_OPTS.map(c => `<label class="chk"><input type="checkbox" id="cons-${c}" data-cons="${c}" ${settings.consent.includes(c) ? "checked" : ""}> <span class="mono">${c}</span></label>`).join("")}
        <div class="why">Turning off merge adds a human gate: goals stop at an approved, green PR.</div>
      </fieldset>
      <fieldset><legend>Reserve per account</legend>
        ${accounts.map(a => `<label class="slot"><span>${esc(a.label)} <span class="why">kept for you</span></span><input type="number" min="0" max="90" step="5" id="res-${a.id}" data-res="${a.id}" value="${a.reserve}"></label>`).join("")}
      </fieldset>
      <fieldset><legend>Limits per machine (active · total)</legend>
        ${machines.map(m => `<label class="slot"><span>${esc(m.short)} <span class="why">${m.ram} GB</span></span><span class="pair">
          <input type="number" min="0" max="8" id="ma-${m.id}" data-ma="${m.id}" value="${m.maxActive}" aria-label="${esc(m.short)} max active">
          <input type="number" min="0" max="16" id="mt-${m.id}" data-mt="${m.id}" value="${m.maxTotal}" aria-label="${esc(m.short)} max total"></span></label>`).join("")}
      </fieldset>
      <fieldset class="yaml"><legend>fleet.yaml</legend><pre class="cmd">${esc(fleetYaml())}</pre></fieldset>
    </div></details>`;

  const num = (v, lo, hi) => Math.max(lo, Math.min(hi, parseInt(v, 10) || 0));
  $("settingsBox").ontoggle = e => { settingsOpen = e.target.open; };
  document.querySelectorAll("[data-repo]").forEach(c => c.onchange = () => { settings.repos[+c.dataset.repo].on = c.checked; rerender(); });
  $("autoD").onchange = e => { settings.autoDispatch = e.target.checked; log(`turned auto-dispatch ${e.target.checked ? "on" : "off"}`); rerender(); };
  document.querySelectorAll("[data-cons]").forEach(c => c.onchange = () => {
    settings.consent = CONSENT_OPTS.filter(o => o === c.dataset.cons ? c.checked : settings.consent.includes(o));
    log(`goal consent is now: ${settings.consent.join(", ") || "none"}`); rerender();
  });
  document.querySelectorAll("[data-res]").forEach(i => i.onchange = () => { accounts.find(a => a.id === i.dataset.res).reserve = num(i.value, 0, 90); rerender(); });
  document.querySelectorAll("[data-ma]").forEach(i => i.onchange = () => { mById(i.dataset.ma).maxActive = num(i.value, 0, 8); rerender(); });
  document.querySelectorAll("[data-mt]").forEach(i => i.onchange = () => { mById(i.dataset.mt).maxTotal = num(i.value, 0, 16); rerender(); });
  $("addRepo").onclick = () => {
    const v = $("newRepo").value.trim();
    if (!/^[\w.-]+\/[\w.-]+$/.test(v)) { $("repoErr").textContent = "Use the form owner/repo, for example eshu-hq/embedded-postgres."; return; }
    if (!settings.repos.some(r => r.repo === v)) settings.repos.push({ repo: v, on: true });
    rerender();
  };
}
