// Typed-command previews. The hub never runs ssh or a shell string: agents dial out (ADR 0004) and receive the
// typed kinds in api/fleet/agent/v1/command*.proto. Every preview here is rendered through esc() by its caller.
// The only ssh line anywhere is attachLine(): the copyable "attach from your own terminal" fallback (ADR 0006).
const fmtVal = v => Array.isArray(v) ? `[${v.join(", ")}]` : String(v);
const fmtCmd = (name, fields) => `${name}{${Object.entries(fields).filter(([, v]) => v !== undefined && v !== null && v !== "").map(([k, v]) => `${k}: ${fmtVal(v)}`).join(", ")}}`;
const paneKey = s => `{server: default, pane_id: ${s.paneId}}`;
const sessTarget = s => s.yours ? `{server: DEFAULT, tmux_id: $${s.idx + 1} (${s.sess})}` : `{server: DEFAULT, name: ${s.sess}}`;
const repoRef = (host, path) => `${host}/${path}`;
const attachLine = (m, s) => `ssh -t ${m.user}@${m.name} tmux attach -t ${s.sess} \\; select-window -t ${s.target}`;
const attachMachine = m => `ssh -t ${m.user}@${m.name} tmux attach`;
const repoCfg = repo => settings.repos.find(r => r.repo === repo);
const PROVIDER = { github: "GitHub", gitlab: "GitLab", jira: "Jira", bitbucket: "Bitbucket", linear: "Linear" };

// ADR 0008: effective consent = repo default ∩ account's allowed set.
// Repo consent (else the fleet default), intersected with the account's.
const consentFor = (a, repo) => (repoCfg(repo)?.consent ?? settings.consent).filter(c => !a?.consent || a.consent.includes(c));

// The /goal line each harness will receive. Repos with eshu-style hooks use the `/goal consent <acts> -- <text>` form
// (eshu hook convention); everywhere else consent is written into the objective text (01b, G3).
function goalLine(objective, a, repo) {
  const acts = consentFor(a, repo), cfg = repoCfg(repo);
  if (cfg?.hooks === "eshu") return { text: `/goal consent ${acts.join(", ")} -- ${objective}`, note: "eshu hook convention: the repo's UserPromptSubmit router writes the CONSENT: line" };
  const merge = acts.includes("merge") ? "" : "; do not merge";
  return { text: `/goal ${objective}. Consent: ${acts.join(", ")}${merge}.`, note: "generic goal: consent written into the objective text (no consent subcommand exists in any harness)" };
}

// StartGoal (G3) preview for a work item on machine m with account a.
function startGoalCmd(it, m, a, reuse) {
  const line = goalLine(`drive ${it.display} to merged`, a, it.repo);
  const cmd = fmtCmd("StartGoal", { work_item: it.display, repo: repoRef(it.host, it.repo), harness: a.harness, account: a.id, consent: consentFor(a, it.repo),
    worktree_mode: repoCfg(it.repo)?.hooks === "eshu" ? "REPO_DRIVER" : "AGENT_ENSURES", reuse_pane: reuse ? reuse.paneId : undefined,
    in_session: reuse ? undefined : "fleet", window_name: reuse ? undefined : `${a.harness} ${it.display}` });
  return { cmd, keys: line.text, note: line.note, steps: (reuse ? "preflight → pick_session → prepare_branch" : "preflight → ensure_worktree → ensure_window") + " → register_goal → start_log_capture → wait_ready → (set_model, set_effort, clear_context when asked) → write_goal_file → type_goal → confirm_goal_file (step_count is always 14; skipped steps are not reported)" };
}

// G4 goal control through the harness's own /goal (RunHarnessCommand, class GOAL), driven by the machine's goal_control
// (GOAL_CONTROL in fleet-data.js): Interrupt where the harness has no pause, typed text where it has no resume.
function goalCtl(s, sub, text) {
  const c = GOAL_CONTROL[s.harness] ?? { subs: [], pause: "none", resume: "none" }, base = { pane: paneKey(s), command: "/goal", expected_class: "GOAL", catalog_hash: CATALOG[s.harness]?.hash };
  const no = { ok: false, cmd: "", label: `${GOAL_SYNTAX[s.harness]} · GOAL_SUBCOMMAND_UNSUPPORTED`, what: "" };
  if (sub === "pause" && c.pause === "interrupt") return { ok: true, cmd: fmtCmd("Interrupt", { pane: paneKey(s) }), label: `${s.harness} has no /goal pause: this presses Esc instead; the goal stays set`, what: "interrupted goal" };
  if (sub === "resume" && c.resume === "type_text") return { ok: true, cmd: fmtCmd("SendKeys", { pane: paneKey(s), keys: [`literal "${c.resumeText}"`, "Enter"], purpose: "GOAL_RESUME" }), label: `${s.harness} has no /goal resume: the agent types "${c.resumeText}" at the ready prompt`, what: "resumed goal" };
  if (!c.subs.includes(sub)) return no;
  const args = sub === "edit" && s.harness === "muse" ? `edit ${text}` : sub;
  return { ok: true, cmd: fmtCmd("RunHarnessCommand", { ...base, args }), label: `keys the agent will type: /goal ${args}`, what: `${sub === "clear" ? "cleared" : sub + "d"} goal` };
}

const sendKeysCmd = (s, text, extra) => fmtCmd("SendKeys", { pane: paneKey(s), keys: [`literal "${text}"`, "Enter"], purpose: "OPERATOR_TEXT", ...extra });
const answerCmd = (s, p, key) => fmtCmd("SendKeys", { pane: paneKey(s), keys: [`literal "${key}"`], purpose: "PROMPT_ANSWER", answering_prompt: p.promptId });
const harnessCmd = (s, command, cls, args) => fmtCmd("RunHarnessCommand", { pane: paneKey(s), command, args, expected_class: cls.toUpperCase(), catalog_hash: CATALOG[s.harness]?.hash });
const respawnCmd = s => fmtCmd("RespawnPane", { pane: paneKey(s), harness: s.harness, resume: true });
const killCmd = s => fmtCmd("KillWindow", { window: `{server: default, window_id: @${s.idx}}` });
const availCmd = (s, on) => fmtCmd("SetPaneAvailability", { pane: paneKey(s), available_for_work: on, reason: on ? "MARKED_ON_BOARD" : "CLEARED_ON_BOARD", fresh_context_on_reuse: on ? "false unless ticked" : undefined });
const attachCmd = (s, ro) => fmtCmd("OpenAttach", { attach_id: `att-${s.id}`, session: sessTarget(s), cols: 160, rows: 44, read_only: ro });
const ensureSessionCmd = o => fmtCmd("EnsureSession", { session_id: o.id, server: "DEFAULT", name: o.name, workdir: o.repo ? `{repo: ${o.repo}}` : undefined, harness: o.harness || undefined, reuse: o.reuse, owner_principal: "you" });
const drainCmd = (m, on) => on ? fmtCmd("Drain", { reason: "owner" }) : "Undrain{}";

// Move = stop here, dispatch there (v2 has no move command).
function moveCmds(s, to) {
  const a = accounts.find(x => x.harness === s.harness), line = goalLine(`continue ${s.display} on ${s.branch}; pull first`, a, s.repo);
  const stop = GOAL_SUBS[s.harness]?.includes("clear") ? fmtCmd("RunHarnessCommand", { pane: paneKey(s), command: "/goal", args: "clear", expected_class: "GOAL" }) : fmtCmd("Interrupt", { pane: paneKey(s) });
  const start = fmtCmd("StartGoal", { work_item: s.display, repo: repoRef(s.host ?? "github.com", s.repo), harness: s.harness, account: a?.id, consent: consentFor(a, s.repo), in_session: "fleet", window_name: `${s.harness} ${s.display}` });
  return `${mById(s.m).short}: ${stop}\n${to.short}: ${start}\nkeys the agent will type on ${to.short}: ${line.text}`;
}
