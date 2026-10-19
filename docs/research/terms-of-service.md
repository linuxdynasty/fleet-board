# Subscription terms vs. fleet-board

Researched 2026-09-28; Cursor added 2026-10-08 (plan item 0.24, #101). Not
legal advice. Re-check before relying on it; terms change and several pages
were only partly readable.

**2026-10-18 update.** [ADR 0033](../adr/0033-native-sessions-and-handoff.md)
removes the blanket product headless ban. The [native capability record](native-session-capabilities.md)
tracks exact candidate interfaces and unresolved qualification. This earlier
research's terminal-only design consequences are historical, not active bans.

Usage pattern checked: one owner, their own paid subscription per harness,
signed in on several of their own machines, a self-hosted controller starting
unattended goal sessions around the clock and pacing weekly quota. No sharing,
no reselling, no API keys.

Tags: **[E]** the page says it; **[I]** our inference; **[2nd]** secondary
source.

## Anthropic: Claude Pro/Max via Claude Code

Pages re-read directly on 2026-09-28: Consumer Terms (effective Oct 8, 2025)
and the Claude Code legal page.

- Consumer Terms prohibit accessing the Services "through automated or
  non-human means, whether through a bot, script, or otherwise", except via an
  API key "or where we otherwise explicitly permit it". [E]
- Claude Code legal page: Pro/Max limits "assume ordinary, individual usage of
  Claude Code and the Agent SDK". OAuth "is designed to support ordinary use of
  Claude Code". [E]
- Developers must not "route requests through Free, Pro, or Max plan
  credentials on behalf of their users", and may not "collect, store, or
  intermediate Claude.ai credentials or session tokens". [E]
- It "does not prevent an end user from signing in to the unmodified Claude
  Code binary with their own Claude subscription". [E]
- Enforcement "may [happen] without prior notice". [E]
- Claude Code documents headless `claude -p` and `claude setup-token` ("for CI
  pipelines and scripts") with a subscription. [E, via research agent]
- Weekly limits were added in 2025 after some users ran Claude Code 24/7. [2nd,
  TechCrunch 2025-07-28]
- No device or concurrent-session cap found.
- **Agent SDK / `claude -p` change, paused.** Anthropic announced moving
  Agent SDK, `claude -p` and third-party app usage off subscription limits to
  a separate credit, then wrote "Update June 15: We're pausing the changes".
  For now they "still draw from your subscription's usage limits". Interactive
  Claude Code is in the group that stays on the subscription. The same page
  says teams running shared production automation should use an API key.
  [E, support.claude.com/en/articles/15036540, updated June 16, 2026]
  **Superseded billing snapshot:** the October 7, 2026 update on the same
  primary page says Claude Max and Team include monthly API credits and that
  Agent SDK, `claude -p` and third-party apps can still draw from subscription
  limits. That is positive documented support for a candidate path. The
  [SDK overview](https://code.claude.com/docs/en/agent-sdk/overview) still
  says prior approval is needed for a third-party product to offer claude.ai
  login or limits. Fleetboard's exact deployment and auth qualification is
  unresolved; no native route is enabled on this evidence alone.
- **Unverified:** a user in paperclipai/paperclip#2681 quotes an Anthropic
  email: "Starting April 4 … you'll no longer be able to use your Claude
  subscription limits for third-party harnesses including OpenClaw." Not
  found on any Anthropic page (searched anthropic.com, claude.com,
  support.claude.com, code.claude.com on 2026-09-29).

**Risk: highest.** The Consumer Terms' script clause and "ordinary, individual
usage" sit awkwardly with unattended 24/7 fleets, even though headless modes
are documented. [I]

**Product constraint (clear):** fleet-board must never collect, store or proxy
Claude credentials. The owner signs in through Claude's supported flow on
their own worker. Normal SDK or `claude -p` is a documented candidate under
ADR 0033. Bare `-p` skips OAuth/keychain auth and cannot silently replace the
subscription path. Normal `-p` loads project hooks and MCP servers without a
workspace trust dialog or server approval prompt; #268 must prove a prelaunch
boundary.

## OpenAI: ChatGPT Plus/Pro via Codex CLI

- Codex signs in with a ChatGPT plan; `codex login --device-auth` for headless
  machines; copying `~/.codex/auth.json` to another machine is documented
  ("Treat `~/.codex/auth.json` like a password"). [E, learn.chatgpt.com/docs/auth]
- Same page recommends API keys "for programmatic Codex CLI workflows, such as
  CI/CD jobs". A recommendation, not a prohibition. [E]
- Terms of Use reportedly bar "automatically or programmatically extract[ing]
  data or Output". Page returned 403; snippet only. **Re-read by hand.**
- 5-hour and weekly limits shared across CLI, web and IDE. [E]
- No device, concurrency or 24/7 rule found.

**Risk: lowest of the four.** Multi-machine sign-in is documented. [I]

## Meta: Muse Code "Power Usage" subscription

- Subscription "works exclusively through the Muse Code CLI when
  authenticated with a Meta Model API account". [E]
- API keys are for non-interactive environments; `muse exec` (with `--yolo`
  for unattended runs) is documented, but whether it may draw on the
  subscription is not stated. [E / unclear]
- Model API ToS bars evading "rate limits, usage quotas" and using credentials
  to circumvent subscription limits. Staying under the limit is not
  circumvention. [E / I]
- Acceptable Use Policy bars agents that "evade human oversight or
  accountability" and "splitting tasks across sessions to avoid detection". [E]
- Subscriptions are "in limited testing". [E]
- **Not read:** the Meta Subscription Terms of Service and the Muse
  Supplemental Terms. Biggest gap.

**Risk: medium, mostly unknown.**

## Anysphere: Cursor Pro/Pro+/Ultra via `cursor-agent`

Pages read directly on 2026-10-08: Terms of Service (last updated September 3,
2026), Acceptable Use Policy (AUP, last updated August 11, 2026), the CLI
overview, headless, authentication and slash-command docs, Models & Pricing,
Usage and limits, the pricing page, and six forum threads answered by Cursor
staff (each responder's forum profile is in the `CursorStaff` group). The docs
now call the binary `agent`; staff and our probe
([01b](../specs/01b-harness-commands.md#cursor)) call it `cursor-agent`.

The use checked: fleet-board types prompts, goals and `/usage` into the
interactive `cursor-agent` screen in a tmux pane, on the owner's own plan and
machines; it never runs `-p` and never reads `~/.cursor/cli-config.json`.

- The Terms pull the AUP in: "By using the Service, you agree to these Terms
  including our Acceptable Use Policy." The AUP applies "regardless of
  subscription tier or how the Service is accessed", and "In the event of a
  conflict between this AUP and your Agreement, this AUP controls with respect
  to acceptable use." [E]
- **The AUP bars** "Accessing the Service through automated or non-human
  means, whether through a bot, script, or otherwise". Unlike Anthropic's
  copy of that sentence, it has no API-key or "where we otherwise explicitly
  permit it" exception in the text. Breaking the AUP "could result in action
  against your account, up to suspension or termination". [E]
- Cursor's own docs offer automation of the same CLI: "print automation for
  scripts and CI pipelines" (CLI overview), "Use Cursor CLI in scripts and
  automation workflows" (headless), and "For automation, scripts, or CI
  environments, use API key authentication" (authentication). [E]
- Scripted use spends the user's plan: "User API keys bill to that user's
  plan" (TypeScript SDK); staff: "Billing is usage based under the plan and
  limits of the account tied to the API key" (2026-07-10). Pricing plans for
  it: "Power users (multiple agents/automation): Often $200+/mo total usage".
  Cursor itself sells "always-on agents" on schedules (Automations changelog,
  page `03-05-26`). [E]
- Staff put the line at the client, not at automation: "If you want
  automation outside the IDE, use the CLI or SDK, that's the supported and
  safe path" (2026-07-04); the headless CLI "is exactly what it's built for,
  scripts, CI, automation" (2026-07-10); a tool that uses "your access token"
  to call "our private, non-public client endpoints" breaks §1.5, and "Using
  your subscription outside official clients can trigger abuse enforcement,
  up to and including an account ban" (2026-08-10); "you're using the
  official clients, you're within the ToS" (2026-08-16). [E, forum]
- Asked whether the August 11 AUP line bars calling Cursor through the
  official API or SDK from another program, staff linked the threads above and
  wrote "TL;DR: It sounds like your use case is just fine." (2026-08-17). [E,
  forum] Forum replies are not part of the contract: the Terms and what they
  incorporate are "the entire and exclusive understanding" (§17.1). [E / I]
- §1.5 bars (iii) "rent, lease, lend, or sell the Service", (vi) "probe, scan
  or attempt to penetrate the Service" and (viii) "harvest, scrape, or extract
  data from the Service". Staff read (iii) as "offering third parties access
  to Cursor itself or Cursor-powered usage under your account" (2026-05-04).
  [E] Our usage probe types `/usage` in the owner's own pane: not the security
  probing of (vi), and not harvesting data. [I]
- The AUP also bars "circumventing any rate limits" and "Manipulating,
  circumventing, or interfering with Anysphere's subscription, billing, or
  usage-metering systems, including through direct or automated means".
  Reading `/usage` and staying under it does neither. [E / I]
- "You accept responsibility for all activities that occur under your
  account" (§3). Cursor may suspend "at any time without notice", for example
  when "preventing abuse" (§9). [E]
- **Devices.** Staff, 2026-09-04: one person on "two or three of your own
  devices" at once is fine; the guard targets "when one account signs in from
  a lot of different computers in a short time window"; "inactive devices stop
  being counted after about a day"; "The only thing that's actually not
  allowed under the Terms of Service is sharing your login with someone
  else." [E, forum] No device number on any terms page.
- **Limits.** Two monthly pools (Cursor Models, Other Models). "Usage resets
  monthly with your billing cycle. Unused usage does not roll over." Past the
  included amount, on-demand usage, if the user turned it on, is billed "at
  the same API rates". [E] No concurrency or 24/7 rule found.
- **Not found:** any page on another program typing into the interactive
  screen. The documented automation path is `-p` with an API key, which
  fleet-board never uses. The docs do show starting the interactive CLI with
  a prompt: `agent "refactor the auth module to use JWT tokens"`. `/usage` is
  not on the docs' slash-command list, though our probe saw it (01b). [E / I]

**Risk: low to medium.** Read word for word, the AUP's script line would also
ban the `-p` scripts Cursor documents, so it likely does not mean all automation of the
official client. Docs, pricing and staff all treat that automation on the
user's own plan as supported, and usage is billed at API rates, so a fleet
gets no cheaper compute than a person would. What stays open: fleet-board's
way in (typing into the screen) is not the documented one, the AUP line has no
written exception, and forum replies do not bind Cursor. [I]

**Ruling (arbiter-fleet, 2026-10-08, #101): auto-dispatch on Cursor is
allowed**, on the same footing as Claude Code. That means the owner's own plan;
the unmodified `cursor-agent` binary in a tmux pane the owner can see; never
`-p`; no credential read; `auto_dispatch` off until the owner turns it on; and
the per-account `interactive-only` mode
([ADR 0008](../adr/0008-goal-consent-and-account-modes.md)) to keep any Cursor
account out. One Cursor-specific limit applies: pacing spends only included
usage, so an account whose `/usage` shows On-Demand on, or does not show it, is
excluded from auto-dispatch (plan item 0.20, #97). Fleet-board never signs
Cursor in anywhere, so the owner keeps each Cursor account on the few machines
they signed it in on (staff said two or three). Written confirmation from
Cursor (hi@cursor.com) is not needed for the owner's own use. As with every
provider, it is needed before offering fleet-board to others (design
implication 7). If Cursor says this use is not allowed, or its terms change to
say so, set Cursor accounts to `interactive-only` and reopen 0.24.

**Product constraint (clear):** never read `~/.cursor/cli-config.json` (it
holds `authInfo`) or any Cursor token. The current terminal path drives
`cursor-agent`; the official SDK is a disabled candidate whose local
approval and billing behavior must qualify under ADR 0033.

## Across all four

| Provider | Script or bot clause | Automating the CLI on the plan | Risk |
|---|---|---|---|
| Anthropic | yes, except an API key or explicit permission | headless documented; limits "assume ordinary, individual usage" | highest |
| OpenAI | reported only ("programmatically extract"), page 403 | API keys recommended for CI, not required | lowest |
| Meta | none found; the AUP bars evading human oversight | `muse exec` documented; plan use not stated | medium, mostly unknown |
| Cursor | yes (AUP), no exception in the text | documented, staff call it supported, billed to the plan | low to medium |

Fine: single owner, own accounts, own machines; same account on several of
your machines (Cursor: a few); reading `/usage` / `/status` in your own
session; staying inside quotas.

Risky: unattended scripted use of consumer plans (Anthropic most), fully
lights-out operation (Meta AUP), Cursor's AUP script line read literally, and
consistently using 100% of quota.

## Design implications

1. Never touch credentials. Use a qualified native local interface or the
   existing terminal path, with the harness's own login on the worker.
2. Keep the fleet identifiable as automation; do not disguise sessions.
3. Offer a human gate at merge as a setting (the owner currently allows
   goals to merge).
4. Default pacing target below 100% (the owner reserve already does this).
5. Cap concurrent sessions per account.
6. Support Team/Enterprise and API-key accounts for users who want sustained
   24/7 load under commercial terms.
7. Before offering fleet-board to others: get written confirmation from each
   provider's support, and read Meta's subscription and Muse supplemental
   terms.
8. Cursor: pace on included usage only; an account with On-Demand on or not
   shown is excluded from auto-dispatch (0.20). Fleet-board never signs an
   account in, so the owner keeps each Cursor account to a few machines.

## Sources

- https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan
- https://github.com/paperclipai/paperclip/issues/2681 (user report, unverified)

- https://www.anthropic.com/legal/consumer-terms
- https://code.claude.com/docs/en/legal-and-compliance
- https://code.claude.com/docs/en/authentication
- https://code.claude.com/docs/en/headless
- https://support.claude.com/en/articles/11145838-using-claude-code-with-your-pro-or-max-plan
- https://learn.chatgpt.com/docs/auth
- https://learn.chatgpt.com/docs/pricing
- https://help.openai.com/en/articles/11369540-using-codex-with-your-chatgpt-plan (403)
- https://openai.com/policies/row-terms-of-use/ (403)
- https://dev.meta.ai/docs/muse-code/subscriptions
- https://dev.meta.ai/docs/muse-code/auth
- https://dev.meta.ai/docs/muse-code/extending
- https://dev.meta.ai/legal/terms-of-service
- https://dev.meta.ai/legal/acceptable-use-policy
- https://www.meta.com/help/subscriptions/1021145227643680/
- Cursor, all read 2026-10-08:
  - https://cursor.com/terms-of-service (last updated September 3, 2026)
  - https://cursor.com/acceptable-use-policy (last updated August 11, 2026)
  - https://cursor.com/pricing
  - https://cursor.com/docs/models-and-pricing
  - https://cursor.com/help/models-and-usage/usage-limits
  - https://cursor.com/docs/cli/overview
  - https://cursor.com/docs/cli/headless
  - https://cursor.com/docs/cli/reference/authentication
  - https://cursor.com/docs/cli/reference/slash-commands
  - https://cursor.com/docs/sdk/typescript
  - https://cursor.com/changelog/03-05-26 (Automations)
  - https://forum.cursor.com/t/using-cursor-frontier-models-like-composer-2-5-in-external-harnesses-e-g-codex/164676
  - https://forum.cursor.com/t/inquiry-regarding-official-api-or-commercial-license-for-using-cursors-engine-as-a-backend-component/165063
  - https://forum.cursor.com/t/does-using-oh-my-pi-s-cursor-provider-or-an-openai-compatible-proxy-to-the-same-endpoints-violate-cursor-s-tos/167778
  - https://forum.cursor.com/t/is-this-use-of-cursor-cloud-agents-permitted-under-cursor-s-terms/168633
  - https://forum.cursor.com/t/api-sdk-terms-of-use-question/159741
  - https://forum.cursor.com/t/can-i-use-the-same-cursor-account-on-multiple-devices-at-once/170323
  - https://cursor.com/acceptable-use (404)
- Secondary: https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/
