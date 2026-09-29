// Settings panel. In the real hub these live in fleet.yaml; here they only change the page.
let settingsOpen = true;

function fleetYaml() {
  const lines = ["hub: 192.168.1.118", "repos:"];
  settings.repos.filter(r => r.on).forEach(r => lines.push(`  - ${r.repo}`));
  lines.push("dispatch:", `  headroom_min_percent: ${settings.headroomMin}`, "  prefer_non_hub: true", "  one_agent_per_branch: true",
    `agents: [${agents.map(a => a.id).join(", ")}]`, "machines:");
  machines.forEach(m => lines.push(`  - name: ${m.name}`, `    host: ${m.user}@${m.ip}`, `    ram_gb: ${m.ram}`, `    max_agents: ${m.slots}`));
  return lines.join("\n");
}

function renderSettings() {
  $("settings").innerHTML = `<details id="settingsBox" ${settingsOpen ? "open" : ""}>
    <summary>Settings <span class="hint">${settings.repos.filter(r => r.on).length} repos watched · hold back under ${settings.headroomMin}% · ${machines.reduce((n, m) => n + m.slots, 0)} agent slots</span></summary>
    <div class="set-grid">
      <fieldset><legend>Watched repos</legend>
        ${settings.repos.map((r, i) => `<label class="chk"><input type="checkbox" id="repo-${i}" data-repo="${i}" ${r.on ? "checked" : ""}> <span class="mono">${esc(r.repo)}</span></label>`).join("")}
        <div class="add-row"><input id="newRepo" placeholder="owner/repo" aria-label="Add repo"><button class="btn" id="addRepo">Add</button></div>
        <div class="why" id="repoErr"></div>
      </fieldset>
      <fieldset><legend>Max agents per machine</legend>
        ${machines.map(m => `<label class="slot"><span>${esc(m.name)} <span class="why">${m.ram} GB</span></span><input type="number" min="0" max="8" id="slots-${m.id}" data-slots="${m.id}" value="${m.slots}"></label>`).join("")}
      </fieldset>
      <fieldset><legend>Dispatch</legend>
        <label class="slot"><span>Hold an agent back when less than this % is left</span><input type="number" min="0" max="90" step="5" id="headroom" value="${settings.headroomMin}"></label>
        <div class="why">Unknown usage is never auto-picked. One agent per PR branch. Machines other than the hub go first.</div>
      </fieldset>
      <fieldset class="yaml"><legend>fleet.yaml</legend><pre class="cmd">${esc(fleetYaml())}</pre></fieldset>
    </div></details>`;

  $("settingsBox").ontoggle = e => { settingsOpen = e.target.open; };
  document.querySelectorAll("[data-repo]").forEach(c => c.onchange = () => { settings.repos[+c.dataset.repo].on = c.checked; renderAll(); });
  document.querySelectorAll("[data-slots]").forEach(inp => inp.onchange = () => {
    const v = Math.max(0, Math.min(8, parseInt(inp.value, 10) || 0));
    mById(inp.dataset.slots).slots = v; renderAll();
  });
  $("headroom").onchange = e => { settings.headroomMin = Math.max(0, Math.min(90, parseInt(e.target.value, 10) || 0)); renderAll(); };
  $("addRepo").onclick = () => {
    const v = $("newRepo").value.trim();
    if (!/^[\w.-]+\/[\w.-]+$/.test(v)) { $("repoErr").textContent = "Use the form owner/repo, for example eshu-hq/embedded-postgres."; return; }
    if (!settings.repos.some(r => r.repo === v)) settings.repos.push({ repo: v, on: true });
    renderAll();
  };
}
