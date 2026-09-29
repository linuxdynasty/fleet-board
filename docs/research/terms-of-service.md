# Subscription terms vs. fleet-board

Researched 2026-09-28. Not legal advice. Re-check before relying on it; terms
change and several pages were only partly readable.

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

**Risk: highest.** The Consumer Terms' script clause and "ordinary, individual
usage" sit awkwardly with unattended 24/7 fleets, even though headless modes
are documented. [I]

**Product constraint (clear):** fleet-board must never collect, store or proxy
Claude credentials. Each user signs in to the unmodified `claude` binary on
their own machines; fleet-board only starts and observes that binary.

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

**Risk: lowest of the three.** Multi-machine sign-in is documented. [I]

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

## Across all three

Fine: single owner, own accounts, own machines; same account on several of
your machines; reading `/usage` / `/status` in your own session; staying
inside quotas.

Risky: unattended scripted use of consumer plans (Anthropic most), fully
lights-out operation (Meta AUP), and consistently using 100% of quota.

## Design implications

1. Never touch credentials. Drive the unmodified harness binaries only.
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

## Sources

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
- Secondary: https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/
