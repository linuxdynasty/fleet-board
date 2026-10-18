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
| Claude Agent SDK / normal `claude -p` | none; installed native version NC | D [C1,C2] | D [C1] | D [C1] | D [C1] | NC | D [C1]; prelaunch guard NC [C2] | D [C1] | NC | D [C3] under plan limits; Fleetboard qualification NC [C1,C3] | D [C1]; API key route separate | D local SDK process [C1]; Fleetboard use NC |
| Codex app-server | none; installed native version NC | D [O1] | D [O1] | D [O1] | D [O1] | D [O1] | D [O1] | NC | D [O1] auth modes; identity signal NC | D [O1] ChatGPT sign-in; Fleetboard billing proof NC | D [O1] separate API key | D local server [O1]; Fleetboard use NC |
| Muse SDK/MSP through `muse serve` | none; `next` docs, installed native version NC | D [M1] | D [M1] | D [M1] | NC | NC | D [M1]; exact behavior NC | NC | D [M2] auth modes; identity signal NC | NC entitlement for embedded SDK | D [M2] API auth | D Developer Preview local serve [M1]; Fleetboard use NC |
| Cursor TypeScript SDK | none; installed native version NC | D [U1] | D [U1] | D [U1] | D [U1] | D [U1] cancel | NC local approval equivalence | D [U1] usage billing; pacing signal NC | NC | D [U1] user/team plan billing; Fleetboard proof NC | D [U1] key use, billed to plan | D local SDK [U1]; Fleetboard use NC |
| OpenCode SDK local client/server | none; installed native version NC | D [P1] | D [P1] | D [P1] | NC | NC | NC | NC | D [P2] provider-specific; identity signal NC | NC for Anthropic subscription plugin [P2] | D [P2] Anthropic API provider | D local server [P1]; Fleetboard use NC |

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
- [C3: Claude plan article](https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan): October 7, 2026 update positively says SDK, `claude -p` and third-party apps can still use subscription limits. It also says Max and Team plans have monthly API credits. The C1 prior-approval wording creates an unresolved Fleetboard deployment question; it does not erase C3's positive plan guidance.
- [O1: Codex app-server](https://developers.openai.com/codex/app-server): structured thread and turn calls, events, approval requests and distinct ChatGPT/API-key authentication.
- [M1: Muse SDK/MSP](https://meta-models.github.io/muse-code-sdk/next/): Developer Preview, local `muse serve` integration. [M2: Muse auth](https://dev.meta.ai/docs/muse-code/auth) covers auth modes; embedded subscription use remains NC.
- [U1: Cursor TypeScript SDK](https://cursor.com/docs/sdk/typescript): local create, resume, send, cancel, usage and user/team billing. Local approval and sandbox equivalence remain NC.
- [P1: OpenCode SDK](https://docs.opencode.ai/docs/sdk/): local client/server. [P2: provider auth](https://opencode.ai/v2/docs/cli/providers/) documents provider-specific auth, including Anthropic API; it does not grant subscription use.

## Source learning, not copied implementation

- [T3 Code `a01c48f86245f2a779f9af84042f3b09fbdeb91f`](https://github.com/pingdotgg/t3code/tree/a01c48f86245f2a779f9af84042f3b09fbdeb91f): `apps/server/src/orchestration-v2/EventSink.ts` and `EffectOutbox.ts` suggest admission and effect receipts committed before publish, ordered per session; the provider adapter, Claude SDK and Codex app-server paths show where transport-specific logic sits. Fleetboard needs its existing machine-side agent stream, command outbox and one-writer store across machines.
- [OpenCode `43bb7ba3a2855c26ff0e7db261cd3098efce6b61`](https://github.com/anomalyco/opencode/tree/43bb7ba3a2855c26ff0e7db261cd3098efce6b61): `packages/core/src/session/{session,run-coordinator,execution}.ts` suggest input identity, serialization and write-ahead execution; `packages/client/src/solid/connection.ts` suggests reconnect and cursor handling. Fleetboard must bind those ideas to machine ownership and hub replay rules, without importing OpenCode's client.
- The `LICENSE` files at both pinned commits were checked with `gh api` on 2026-10-18: both say **MIT License** (T3 Code copyright T3 Tools Inc.; OpenCode copyright opencode). Reuse of implementation text would need its copyright and license notice. This change copies no implementation text. Pinning a development commit does not prove an installed version.
