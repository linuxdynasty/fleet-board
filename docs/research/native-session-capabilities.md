# Native session qualification record

Recorded 2026-10-18 for #265. This is a **candidate** matrix, not a list of
enabled backends. `D` means documented by the linked provider page; `S` means
source-inspected at the named commit; `L` means live-proven at an exact
installed version; `NC` means NOT_CHECKED. `D` does not imply `L`, provider
permission for Fleetboard's deployment, or an implemented agent caller.
Where a cell contains both a fact and a gap, both are shown. All native
Fleetboard paths are disabled. The installed native interface version,
end-to-end local proof, and approved deployment are `NC` for every row.

| Candidate interface | Qualified version | Start | Send | Stream | Resume | Interrupt | Approvals | Usage | Account identity | Subscription billing | API billing | Deployment |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Claude Agent SDK | none; installed native version NC | D [C1,C4] | D [C4,C5] | D [C6] | D [C4] | NC | D [C7]; Fleetboard callback NC | D cost [C8]; quota NC | NC | D plan use [C3]; deployment permission NC [C1] | D key path [C1]; separately qualified | D local process [C1]; Fleetboard use NC |
| Normal Claude CLI `-p` | none; installed native version NC | D [C2] | D prompt/stream input [C2] | D stream JSON [C2] | D session ID [C2] | D SIGINT [C2]; safe caller NC | D permission flags [C2]; Fleetboard approval handling NC | D cost estimate [C2]; quota NC | NC | D plan use [C3]; deployment permission NC [C1] | D key route [C2]; separately qualified | D local CLI [C2]; prelaunch guard NC |
| Codex app-server | none; installed native version NC | D [O1] | D [O1] | D [O1] | D [O1] | D [O1] | D [O1] | NC | D [O1] auth modes; identity signal NC | D [O1] ChatGPT sign-in; Fleetboard billing proof NC | D [O1] separate API key | D local server [O1]; Fleetboard use NC |
| Muse SDK/MSP through `muse serve` | none; `next` docs, installed native version NC | D [M1] | D [M1] | D [M1] | NC | NC | D [M1]; exact behavior NC | NC | D [M2] auth modes; identity signal NC | NC entitlement for embedded SDK | D [M2] API auth | D Developer Preview local serve [M1]; Fleetboard use NC |
| Cursor TypeScript SDK | none; installed native version NC | D [U1] | D [U1] | D [U1] | D [U1] | D [U1] cancel | NC local approval equivalence | D [U1] usage billing; pacing signal NC | NC | D [U1] user/team plan billing; Fleetboard proof NC | D [U1] key use, billed to plan | D local SDK [U1]; Fleetboard use NC |
| OpenCode SDK local client/server | none; installed native version NC | D [P1] | D [P1] | D [P1] | NC | NC | NC | NC | D [P2] provider-specific; identity signal NC | NC for Anthropic subscription plugin [P2] | D [P2] Anthropic API provider | D local server [P1]; Fleetboard use NC |

Bare Claude `--bare -p` is excluded from the plan-backed candidate: [C2] says it skips OAuth and keychain login and uses an API key or another provider credential. It needs a separately qualified API-billed route, never a silent fallback.

`NC` also covers completion events and behavior under restart, duplicate
delivery, stale approval, quota reset and machine loss for all five. Terminal
parsing fixtures in the existing specs do not prove a native interface. A
candidate must be rechecked for an exact package/binary version and recorded
per version, interface, auth and billing mode, and deployment before an agent
advertises its capability. Unsupported or unknown combinations stay disabled
with a reason; a working Anthropic API provider in OpenCode proves no Claude
subscription entitlement. Raw credentials remain outside Fleetboard.

## Primary sources read for this record

- [C1: Claude Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview): local process, sessions, streaming, permissions and usage. Its current getting-started note says third-party developers need prior approval to offer claude.ai login or limits.
- [C2: Claude programmatic CLI](https://code.claude.com/docs/en/headless): `claude -p`; bare mode skips subscription OAuth/keychain auth. Normal `-p` loads project hooks and MCP servers without a workspace trust dialog or per-server approval prompt, so #268 needs a prelaunch boundary.
- [C4: SDK sessions](https://code.claude.com/docs/en/agent-sdk/sessions): create, follow up and resume by session ID. [C5: SDK streaming input](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode): prompts over a live client. [C6: SDK streaming output](https://code.claude.com/docs/en/agent-sdk/streaming-output): message events.
- [C7: SDK user input and approvals](https://code.claude.com/docs/en/agent-sdk/user-input): permission callbacks. [C8: SDK cost and usage](https://code.claude.com/docs/en/agent-sdk/cost-tracking): cost/token records, not proof of subscription quota state.
- [C3: Claude plan article](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan): October 7, 2026 update positively says SDK, `claude -p` and third-party apps can still use subscription limits. It also says Max and Team plans have monthly API credits. The C1 prior-approval wording creates an unresolved Fleetboard deployment question; it does not erase C3's positive plan guidance.
- [O1: Codex app-server](https://developers.openai.com/codex/app-server): structured thread and turn calls, events, approval requests and distinct ChatGPT/API-key authentication.
- [M1: Muse SDK/MSP](https://meta-models.github.io/muse-code-sdk/next/): Developer Preview, local `muse serve` integration. [M2: Muse auth](https://dev.meta.ai/docs/muse-code/auth) covers auth modes; embedded subscription use remains NC.
- [U1: Cursor TypeScript SDK](https://cursor.com/docs/sdk/typescript): local create, resume, send, cancel, usage and user/team billing. Local approval and sandbox equivalence remain NC.
- [P1: OpenCode SDK](https://docs.opencode.ai/docs/sdk/): local client/server. [P2: provider auth](https://opencode.ai/v2/docs/cli/providers/) documents provider-specific auth, including Anthropic API; it does not grant subscription use.

## Source learning, not copied implementation

- [T3 Code `a01c48f86245f2a779f9af84042f3b09fbdeb91f`](https://github.com/pingdotgg/t3code/tree/a01c48f86245f2a779f9af84042f3b09fbdeb91f): [`EventSink.ts`](https://github.com/pingdotgg/t3code/blob/a01c48f86245f2a779f9af84042f3b09fbdeb91f/apps/server/src/orchestration-v2/EventSink.ts#L545-L568) appends events, applies them, enqueues effects and writes a receipt; its [transaction wrapper](https://github.com/pingdotgg/t3code/blob/a01c48f86245f2a779f9af84042f3b09fbdeb91f/apps/server/src/orchestration-v2/EventSink.ts#L240-L261) publishes after commit. [`EffectOutbox.ts`](https://github.com/pingdotgg/t3code/blob/a01c48f86245f2a779f9af84042f3b09fbdeb91f/apps/server/src/orchestration-v2/EffectOutbox.ts#L123-L141) separates replay-safe from process-bound effects after a server loss. Fleetboard needs those distinctions in its existing machine-side agent stream, command outbox and one-writer store across machines.
- [OpenCode `43bb7ba3a2855c26ff0e7db261cd3098efce6b61`](https://github.com/anomalyco/opencode/tree/43bb7ba3a2855c26ff0e7db261cd3098efce6b61): [`session.ts`](https://github.com/anomalyco/opencode/blob/43bb7ba3a2855c26ff0e7db261cd3098efce6b61/packages/core/src/session/session.ts#L33-L35) carries an optional input id; [`run-coordinator.ts`](https://github.com/anomalyco/opencode/blob/43bb7ba3a2855c26ff0e7db261cd3098efce6b61/packages/core/src/session/run-coordinator.ts#L6-L29) serializes work per key; [`execution.ts`](https://github.com/anomalyco/opencode/blob/43bb7ba3a2855c26ff0e7db261cd3098efce6b61/packages/core/src/session/execution.ts#L76-L84) records a durable in-flight claim; [`connection.ts`](https://github.com/anomalyco/opencode/blob/43bb7ba3a2855c26ff0e7db261cd3098efce6b61/packages/client/src/solid/connection.ts#L147-L165) reconnects the event stream. Fleetboard must bind these ideas to machine ownership and hub replay rules without importing OpenCode's client.
- The `LICENSE` files at both pinned commits were checked with `gh api` on 2026-10-18: both say **MIT License** (T3 Code copyright T3 Tools Inc.; OpenCode copyright opencode). Reuse of implementation text would need its copyright and license notice. This change copies no implementation text. Pinning a development commit does not prove an installed version.
