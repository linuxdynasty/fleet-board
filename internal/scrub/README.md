# internal/scrub

The shared secret scrubber (02e, "Size limits"; plan row 2c). Free text the
agent writes (`CommandResult.message`, `CommandProgress.message`,
`parse_message`, `ProbeHealth.message`) goes through it on the agent before it
is sent (row 6), and again on the hub before it is stored (row 5). The hub also
scrubs the insights report with it (05, "Verification").

```go
msg = scrub.Text(msg, scrub.Options{MaxBytes: 1024, Home: home}) // agent: home folder becomes ~
msg = scrub.Text(msg, scrub.Options{MaxBytes: 1024})             // hub: same rules, no home folder
```

What `Text` does, in order:

1. Turns bytes that are not valid UTF-8 into U+FFFD.
2. When `Home` is set, writes each whole occurrence of it as `~` and keeps the
   rest of the path: `/Users/al/repos/x` becomes `~/repos/x`, and
   `file:///Users/al/x` becomes `file://~/x`, but `/Users/alice`,
   `/Users/al.bak` and `/srv/Users/al` stay. A home is accepted only if it is
   absolute, not `/`, and made of name characters (letters, digits, `.`, `_`,
   `-`, any non-ASCII) and `/`; anything else is ignored. A home path is
   personal data, not a secret (#139), so only the agent passes it.
3. Swaps each secret for a placeholder, keeping the part that says what it
   was (see the table).
4. Does step 2 again. Whether the home is a whole name depends on the byte
   after it, and that byte may have been part of a secret: in
   `/Users/al` + `x@b.co`, the home is only whole once the email is
   `[email]`. Judging it again on the scrubbed text is what lets the hub's
   pass (no home) change nothing.
5. Cuts the result to `MaxBytes`. The cut never splits a character, never
   keeps half a placeholder (it backs up to before the whole replacement), and
   never ends on the home folder. A cap of zero or less gives `""`: every
   field has a cap.

Running `Text` on its own output returns it unchanged, with or without
`Home`, so the hub's second pass changes nothing the agent already scrubbed.
`FuzzText` checks that, the cap and valid UTF-8 for any text, home and cap;
`TestTextAgentThenHub` checks the agent-then-hub pair on every home and
secret case.

## What counts as a secret

02e names four kinds: tokens, emails, URL query values and passwords in URLs.
It does not say which token shapes count, so this list was chosen here. The
rule for choosing: the token formats the fleet itself mints, the tokens of the
services a fleet machine talks to (GitHub, the harness model providers and the
clouds behind them), and the generic forms any of those take in an error
message. Each line is a pattern in `rules.go`. This README is the full list;
02e has a summary, and a change updates both, with a case in `cases_test.go`.

| Shape | Example in, out | Why |
|---|---|---|
| Fleet enroll token | `fbe1_<87 chars>` → `fbe1_[redacted]` | the fleet's own (02g); any `fbe<N>_` version |
| Fleet API token | `fbt_<id>.<secret>` → `fbt_[redacted]` | the fleet's own (02f), used by the CLI |
| GitHub tokens | `ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_` → prefix + `[redacted]` | the GitHub integration and `gh` on every machine (03j) |
| `sk-` keys | `sk-ant-api03-…`, `sk-proj-…`, `sk-or-…` → `sk-[redacted]` | Anthropic, OpenAI and OpenRouter keys behind Claude Code, Codex and OpenCode |
| AWS access key id | `AKIA…` → `AKIA[redacted]`, `ASIA…` → `ASIA[redacted]` | Claude Code on Amazon Bedrock |
| Google API key | `AIza` + 35 chars → `AIza[redacted]` | Gemini through OpenCode |
| JSON Web Token | `eyJ….eyJ….sig` → `[redacted]` | bearer tokens of many services |
| Bearer token | `Bearer <16+ chars>` → `Bearer [redacted]` | any HTTP API |
| Authorization header | `Authorization: Basic …` → `Authorization: Basic [redacted]`; `map[Authorization:[Bearer …]]` → `map[Authorization:[Bearer [redacted]]]` | any HTTP API. The value goes to the end of the line, a `]` or a closing quote. A known scheme word (the IANA list plus `Token`, `ApiKey`, `SSWS`, AWS) is kept; any other first word may be the credential, so `Authorization: abc123 failed` → `Authorization: [redacted]` |
| Cookie and token headers | `Cookie: …`, `Set-Cookie: …`, `Private-Token: …` (any `…-Token:`) → name + `[redacted]` | session cookies; GitLab-style token headers |
| Private key block | `-----BEGIN … PRIVATE KEY-----` (PEM) or `… PRIVATE KEY BLOCK-----` (PGP) to its END line, or to the end of the text when cut short → `[redacted]` | the hub's CA key and SSH keys (02g); signing keys |
| Secret-named value | see below | a variable, flag or key whose name ends in a secret word |
| URL query value | `?code=abc&state=x` → `?code=[redacted]&state=[redacted]` | 02e: every value, not just ones that look secret |
| Password in a URL | `postgres://al:hunter2@db` → `postgres://al:[redacted]@db` | 02e; up to the last `@` before the path |
| Token as a URL user name | `https://ghp_…:x-oauth-basic@github.com` → `https://ghp_[redacted]:[redacted]@github.com` | the user name is scrubbed by the rules above |
| Email | `al@example.com` → `[email]` | 02e; 01e hides emails by default |

**Secret words:** password, passwd, secret, token, salt, api key, access key,
private key, ca key, server key (`_`, `-` or nothing between the two words;
any case). salt and the CA and server keys are the fleet's own (02g). The
name may have any prefix (`GITHUB_TOKEN`, `db_password`, `account_salt`).
These forms count:

| Form | Example in, out |
|---|---|
| `NAME=value` | `GITHUB_TOKEN=abc` → `GITHUB_TOKEN=[redacted]` |
| quoted value (quotes kept) | `--password='p w'` → `--password='[redacted]'`; `TOKEN="a b"` → `TOKEN="[redacted]"` |
| quote never closed | `token='abc def` → `token='[redacted]`, to the end of the line |
| flag and value | `--password hunter2`, `--api-key x` → `--password [redacted]`, `--api-key [redacted]` |
| `name: value` | `db_password: hunter2` → `db_password: [redacted]`; also `X-Api-Key:` |
| JSON, Python, escaped JSON | `"api_key": "…"`, `'token': '…'`, `\"token\": \"…\"` → value `[redacted]`, quotes kept; an escaped quote inside the value (`"ab\"cd"`) stays inside it |

`name: value` does not count for token: `input_tokens: 1234` is a count and
`token: expired` is prose. A token in that spot is still caught by its prefix
when it has one.

**Left alone on purpose:** `input_tokens: 1234`, `tokens: 12`,
`token: expired`, `max_tokens=4096` (the name must end in the word),
`--verbose`, `task-runner` (`sk-` must start a word), `ssh al@mac-mini` (no
domain), host and port pairs, URLs with no query.

**Edges, chosen and tested:**

- A query value runs to whitespace, a quote, `<`, `>`, a backtick, `&` or
  `#`. A `)` is part of it (`?token=ab)cd` → `?token=[redacted]`), so in
  `(see https://x/?a=b)` the `)` goes too. A URL may hold `)`, and keeping a
  secret's tail is worse than losing a bracket.
- A GitHub SSH remote looks like an email and goes like one:
  `git@github.com:o/r.git` → `[email]:o/r.git`.
- Every email goes. Row 8 (C2) needs a way to keep the account email on a
  usage screen when `share_identity_email` is on; that option is not built
  here, and row 8 adds it.

## How the rules are written

All rules are joined into one regular expression (Go `regexp`, which runs in
time linear in the input) and run in one pass. In each rule's pattern, the
capture groups hold the secret, one group per alternative; the rest of the
match is kept and the placeholder replaces the group. A rule asserts nothing
to the right of a match, so cutting scrubbed text never makes a new match.

## Speed

Measured on an Apple M4 Pro, Go 1.27.2,
`go test -run '^$' -bench BenchmarkText -count=10 ./internal/scrub/`
(min, mean and max of the 10 runs):

| Input | Time per call |
|---|---|
| a 1 KiB command message (`BenchmarkTextMessage`) | 539, 548, 562 µs |
| 64 KiB of chained secrets, the slowest shape (`BenchmarkTextWorst`) | 61, 62, 64 ms |
| 64 KiB, the other five shapes | 2 to 57 ms |

**Worst-case growth.** A pattern that can start inside a run of word
characters (an email's local part, a URL scheme, a `-Token` header name) has a
bounded length before its first fixed character. Unbounded, an attempt starts
again after each match and runs to the end of the run, so the time grows with
the square of the length. `TestTextWorstCaseStaysLinear` times 16 KiB and
64 KiB (best of 5 each) and fails if 64 KiB takes more than 8 times as long:
bounded it is 3.9 to 4.2 times; with the bounds removed it was 14.5 to 15.3
times and the test failed. A ratio, not a time limit, so a slow machine does
not fail it.

**Load budget.** 04 Q7 budgets the hub at 2,500 events a second during a
replay. Only command results, command progress and probe health carry free
text, and the cost grows with the length (1 KiB is the cap, not the usual
size). If every event in that burst carried a full 1 KiB message, scrubbing would
need about 1.4 cores of this machine. So the hub scrubs in its ingest
goroutines, before it hands a batch to its single writer, never inside the
write transaction, where it would hold the writer for that long. The agent
scrubs one message at a time as it writes it.

Tests: `CGO_ENABLED=0 go test ./internal/scrub/`. Fuzz:
`go test -run '^$' -fuzz FuzzText ./internal/scrub/` (also in `make fuzz`).
