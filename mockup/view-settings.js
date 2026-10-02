// Settings view: watched repos, goals and consent (ADR 0008), reserves, per-machine limits, browser terminal, privacy
// (share_identity_email), integrations (ADR 0012), the effective value per machine with its layer (ADR 0009 /
// SettingSource) and the fleet.yaml preview. The last two are collapsed; raw names sit behind "details".
const CONSENT_OPTS = ["push", "pr-open", "merge"];
const LAYER_CLASS = l => l.startsWith("local") ? "capped" : l.startsWith("machine") ? "machine" : l.startsWith("group") ? "group" : "fleet";
const FIELD_LABEL = { max_active: "goals using quota at once", max_total: "goals in total", default_server: "tmux server", cert_lifetime: "agent certificate lifetime",
  local_approval: "ask on the machine before kill or restart", idle_threshold: "idle after", window_name_template: "window name" };

function fleetYaml() {
  const l = ["repos:"];
  settings.repos.filter(r => r.on).forEach(r => l.push(`  - repo: ${r.host}/${r.repo}`, `    provider: ${r.provider}`, `    hooks: ${r.hooks}`));
  l.push("dispatch:", `  auto: ${settings.autoDispatch}`, `  consent: [${settings.consent.join(", ")}]`, "  pacing: even", "  prefer_available_sessions: true", `  window_name_template: "${settings.windowTemplate}"`, `  idle_threshold: ${settings.idleMinutes}m`, `  share_identity_email: ${settings.showEmails}`, "accounts:");
  accounts.forEach(a => l.push(`  - name: ${a.id}`, `    harness: ${a.harness}`, `    mode: ${a.mode}`, `    reserve_percent: ${a.reserve}`, `    consent: [${a.consent.join(", ")}]`, `    usage_command: ${a.source}`));
  apiAccounts.forEach(a => l.push(`  - name: ${a.id}`, `    harness: opencode`, `    mode: manual-only`, `    budget: {cap_usd: ${a.capUsd}, period: ${a.period}}`));
  l.push("groups:", "  - label: laptop", "    max_active: 2", "    max_total: 4", "  - label: always-on", "    max_active: 2", "    cert_lifetime: 90d", "machines:");
  machines.forEach(m => l.push(`  - name: ${m.name}`, `    labels: [${m.labels.join(", ")}]`, `    max_active: ${m.maxActive}`, `    max_total: ${m.maxTotal}`, `    harnesses: [${m.harnesses.join(", ")}]`));
  return l.join("\n");
}

function layeredRows(m) {
  const rows = [["max_active", m.maxActive, m.sources.max_active], ["max_total", m.maxTotal, m.sources.max_total], ["default_server", m.defaultServer, m.sources.default_server],
    ["cert_lifetime", "90 days", m.sources.cert_lifetime], ["local_approval", m.sources.local_approval === "local ceiling" ? "required" : "off", m.sources.local_approval],
    ["idle_threshold", `${settings.idleMinutes} min`, m.sources.idle_threshold], ["window_name_template", settings.windowTemplate, "fleet default"]];
  return rows.map(([f, v, src]) => `<tr><td title="${esc(f)}">${esc(FIELD_LABEL[f])}</td><td class="mono">${esc(v)}</td><td><span class="layer ${LAYER_CLASS(src)}">${esc(src === "local ceiling" ? "capped by local policy" : src)}</span></td></tr>`).join("");
}

function integrationCard(i) {
  const cap = (b, t) => `<span class="${b ? "ok-t" : "why"}">${b ? "✓" : "–"} ${t}</span>`;
  return `<div class="integ ${i.status}"><div class="cap-top"><span class="prov ${esc(i.id.split("-")[0])}">${esc(i.provider)}</span><span class="cap-name">${esc(i.provider)}</span><span class="pill ${i.status === "connected" ? "working" : ""}">${esc(i.status)}</span></div>
    ${i.how ? `<div class="why">${esc(i.how)} · the hub never writes to a provider; claims, pushes and merges happen inside goals with your own tools</div>` : ""}
    <div class="why">${esc(i.roles.join(" + "))} · claims by ${esc(i.claims.join(", "))}</div>
    <div class="caps-line">${cap(i.checks, "checks")} ${cap(i.reviews, "reviews")} ${cap(i.merge, "merge detection")}</div>
    ${i.watched ? `<div class="why">watched: ${i.watched.map(w => `<span class="mono">${esc(w)}</span>`).join(", ")}</div>` : ""}${i.note ? `<div class="why">${esc(i.note)}</div>` : ""}</div>`;
}

function renderSettingsView() {
  $("view").innerHTML = `
    <div class="section-head"><h2>Settings</h2><span class="hint">${settings.repos.filter(r => r.on).length} repos watched · auto-dispatch ${settings.autoDispatch ? "on" : "off"} · consent ${esc(settings.consent.join(", "))} · emails ${settings.showEmails ? "shown" : "hidden"}. A machine's own policy can only tighten these.</span></div>
    <div class="set-grid">
      <fieldset><legend>Watched repos</legend>
        ${settings.repos.map((r, i) => `<label class="chk"><input type="checkbox" id="repo-${i}" data-repo="${i}" ${r.on ? "checked" : ""}> <span class="mono">${esc(r.host)}/${esc(r.repo)}</span> ${provChip(r.provider)} <span class="why">hooks: ${esc(r.hooks)}</span>${r.sample ? `<span class="samp">sample</span>` : ""}</label>`).join("")}
        <div class="add-row"><input id="newRepo" placeholder="host/owner/repo" aria-label="Add repo"><button class="btn" id="addRepo">Add</button></div>
        <div class="why" id="repoErr"></div>
        <div class="why">hooks: <span class="mono">eshu</span> = the repo ships its own goal hooks; <span class="mono">generic</span> = fleet's user-level hooks run the goal.</div>
        ${dt("s-hooks", `<span>eshu: /goal consent &lt;acts&gt; -- &lt;text&gt; form (eshu hook convention), GoalHooksState.repo_ships_own = true</span><span>generic: EnsureGoalHooks installs the fleet hooks; consent is written into the objective text</span>`)}
      </fieldset>
      <fieldset><legend>Goals</legend>
        <label class="chk"><input type="checkbox" id="autoD" ${settings.autoDispatch ? "checked" : ""}> Start the next goal when a worker or an available session frees up</label>
        <div class="why">Every goal may (repo default; each account can narrow it):</div>
        ${CONSENT_OPTS.map(c => `<label class="chk"><input type="checkbox" id="cons-${c}" data-cons="${c}" ${settings.consent.includes(c) ? "checked" : ""}> <span class="mono">${c}</span></label>`).join("")}
        <div class="why">Turning off merge adds a human gate: goals stop at an approved, green change request. <span class="mono">pr-open</span> means a change request on any code host.</div>
        <label class="slot"><span>Window name template</span><input class="field mono" id="winTpl" value="${esc(settings.windowTemplate)}" aria-label="Window name template"></label>
        <label class="slot"><span>Idle threshold (min)</span><input type="number" min="1" max="60" id="idleMin" value="${settings.idleMinutes}" aria-label="Idle threshold"></label>
      </fieldset>
      <fieldset><legend>Reserve per account</legend>
        ${accounts.map(a => `<label class="slot"><span>${esc(a.label)} <span class="why">% kept for you</span></span><input type="number" min="0" max="90" step="5" id="res-${a.id}" data-res="${a.id}" value="${a.reserve}"></label>`).join("")}
        ${apiAccounts.map(a => `<label class="slot"><span>${esc(a.label)} <span class="why">cap · manual only</span></span><span class="mono">$${a.capUsd}/${esc(a.period)}</span></label>`).join("")}
      </fieldset>
      <fieldset><legend>Limits per machine (active · total)</legend>
        ${machines.map(m => `<label class="slot"><span>${esc(m.short)} <span class="why">${m.ram} GB · ${esc(m.labels.join(", "))}</span></span><span class="pair">
          <input type="number" min="0" max="8" id="ma-${m.id}" data-ma="${m.id}" value="${m.maxActive}" aria-label="${esc(m.short)} max active">
          <input type="number" min="0" max="16" id="mt-${m.id}" data-mt="${m.id}" value="${m.maxTotal}" aria-label="${esc(m.short)} max total"></span></label>`).join("")}
      </fieldset>
      <fieldset><legend>Browser terminal</legend>
        <label class="slot"><span>tmux prefix <span class="why">this mock only</span></span><select id="tmuxPrefix" aria-label="tmux prefix">${["C-b", "C-a"].map(p => `<option ${settings.tmuxPrefix === p ? "selected" : ""}>${p}</option>`).join("")}</select></label>
        <div class="why">The real board passes your keys straight to tmux, so your own prefix and bindings work; this only tells the mock which key is the prefix.</div>
        ${dt("s-term", "<span>keys travel as TermDown.input on the Terminal stream; no contract field for the prefix</span>")}
      </fieldset>
      <fieldset><legend>Privacy</legend>
        <label class="chk"><input type="checkbox" id="showEmails" ${settings.showEmails ? "checked" : ""}> Show account emails (owner only)</label>
        <div class="why">Off: the board shows only the account name. On: the email exactly as the tool's status screen shows it.</div>
        ${dt("s-priv", "<span>share_identity_email; off = agents send only a salted account key</span>")}
      </fieldset>
    </div>

    <div class="section-head"><h2>Integrations</h2><span class="hint">Where work comes from and where code lives. Planned ones list what they will be able to do; the hub never asks a provider for more.</span></div>
    <div class="integs">${integrations.map(integrationCard).join("")}</div>
    ${dt("s-integ", `<span>IntegrationCapabilities{roles, work-item kinds, claim_mechanisms, sync_modes, checks, reviews, merge_detection}</span>${integrations.map(i => `<span>${esc(i.provider)} sync: ${esc(i.sync)}</span>`).join("")}`)}

    <details class="fold" data-dk="layers" ${ui.dt.layers ? "open" : ""}><summary>Effective values per machine <span class="hint">machine → group → fleet default; local policy is the ceiling</span></summary>
      <div class="layer-grid">${machines.map(m => `<table class="layers" aria-label="${esc(m.short)} settings"><caption>${esc(m.name)} · ${esc(m.labels.join(", "))}</caption>${layeredRows(m)}</table>`).join("")}</div>
      <div class="why">"capped by local policy" = the machine's own policy file lowered the value. ${dt("s-layers", "<span>~/.config/fleet-agent/policy.yaml · SettingsAck.capped_fields · SettingSource.layer = SETTING_LAYER_LOCAL_CEILING</span>")}</div></details>
    <details class="fold yaml" data-dk="yaml" ${ui.dt.yaml ? "open" : ""}><summary>fleet.yaml <span class="hint">what these settings write</span></summary><pre class="cmd">${esc(fleetYaml())}</pre></details>`;

  const num = (v, lo, hi) => Math.max(lo, Math.min(hi, parseInt(v, 10) || 0));
  document.querySelectorAll("[data-repo]").forEach(c => c.onchange = () => { settings.repos[+c.dataset.repo].on = c.checked; rerender(); });
  $("autoD").onchange = e => { settings.autoDispatch = e.target.checked; log(`turned auto-dispatch ${e.target.checked ? "on" : "off"}`); rerender(); };
  $("showEmails").onchange = e => { settings.showEmails = e.target.checked; log(`account emails ${e.target.checked ? "shown" : "hidden"}`); rerender(); };
  $("winTpl").onchange = e => { settings.windowTemplate = e.target.value.trim() || "{harness} {work}"; rerender(); };
  $("idleMin").onchange = e => { settings.idleMinutes = num(e.target.value, 1, 60); rerender(); };
  $("tmuxPrefix").onchange = e => { settings.tmuxPrefix = e.target.value === "C-a" ? "C-a" : "C-b"; log(`browser terminal emulates prefix ${settings.tmuxPrefix} (mock only)`); rerender(); };
  document.querySelectorAll("[data-cons]").forEach(c => c.onchange = () => {
    settings.consent = CONSENT_OPTS.filter(o => o === c.dataset.cons ? c.checked : settings.consent.includes(o));
    log(`goal consent is now: ${settings.consent.join(", ") || "none"}`); rerender();
  });
  document.querySelectorAll("[data-res]").forEach(i => i.onchange = () => { accounts.find(a => a.id === i.dataset.res).reserve = num(i.value, 0, 90); rerender(); });
  document.querySelectorAll("[data-ma]").forEach(i => i.onchange = () => { mById(i.dataset.ma).maxActive = num(i.value, 0, 8); rerender(); });
  document.querySelectorAll("[data-mt]").forEach(i => i.onchange = () => { mById(i.dataset.mt).maxTotal = num(i.value, 0, 16); rerender(); });
  $("addRepo").onclick = () => {
    const v = $("newRepo").value.trim(), mt = /^([\w.-]+)\/((?:[\w.-]+\/)+[\w.-]+)$/.exec(v);
    if (!mt) { $("repoErr").textContent = "Use host/owner/repo, for example github.com/eshu-hq/embedded-postgres or gitlab.acme.dev/group/subgroup/repo."; return; }
    const host = mt[1], repo = mt[2].toLowerCase(), provider = host.includes("gitlab") ? "gitlab" : host.includes("bitbucket") ? "bitbucket" : "github";
    if (!settings.repos.some(r => r.repo === repo && r.host === host)) settings.repos.push({ repo, host, provider, on: true, hooks: "generic" });
    rerender();
  };
}
