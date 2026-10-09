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
4. Does step 2 again. Whether the home is a whole name depends on the bytes
   around it, and those may have been part of a secret: in
   `/Users/al` + `x@b.co`, the home is only whole once the email is
   `[email]`.
5. Repeats steps 3 and 4 until a round changes nothing. A redaction can
   delete the byte that stopped another rule (a `]`, a quote, a `#`, a space)
   or make a home whole, and the next round then finds a new secret:
   in `?secret=#;-----END` the `#` becomes `[redacted]` in round 1, and
   round 2 sees the query value run on through `;-----END`. Rounds are
   bounded at 4 (3 that change the text and 1 that confirms). Redaction can
   make text longer (`a=b` becomes `a=[redacted]`), so the bound, not the
   length, is what ends the loop. In a one-off probe (not kept in the tree)
   of 3 million random inputs joined from up to 28 of the pieces in
   `settle_test.go`, none needed more than 2 changing rounds.
   Text that has still not settled after 4 rounds becomes `[redacted]` whole,
   rather than being sent with a secret a further round might have found.
6. Cuts the result to `MaxBytes`. The cut never splits a character or a
   placeholder (`ghp_[redacted]` cut short becomes `ghp_`), never keeps part
   of a match a second pass would scrub again (`?a=[reda` would become
   `?a=[redacted]`; the cut backs up to before `?a=`), and never ends on the
   home folder. A cap of zero or less gives `""`: every field has a cap.

**The guarantee.** Running `Text` on its own output returns it unchanged,
with the same `Home` or with none, for any input, so the hub's second pass
changes nothing the agent already scrubbed. Why it holds: the output is either
`[redacted]` or text a whole round left unchanged. A prefix of that text cut
outside every match is also left unchanged, because no rule looks at anything
to the right of its match. `FuzzText` checks the guarantee, the cap and valid
UTF-8 for any text, home and cap; `TestTextSettlesOnFragments` checks it on
200,000 inputs joined from the pieces of known hard cases;
`TestTextAgentThenHub` checks the agent-then-hub pair on every home and
secret case.

**Callers bound the input.** `Text` costs time in proportion to its input,
not its cap: it scrubs everything, then cuts (1 MiB took 0.8 to 1.5 s, see
Speed). A caller with text of no fixed size, such as a command's stderr, cuts
it first to a bound far above the cap, such as 64 KiB (the largest field,
the insights report), on a UTF-8 boundary. That first cut can split a secret
at the bound, and the part left before it may be too short for its rule (a
token under its minimum length). That part reaches the output only if
scrubbing shrinks everything before it below the cap, which a bound many
times the cap makes unlikely, not impossible.

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
| Bearer token | `Bearer <16+ chars>`, or fewer ending in `=` padding (`Bearer abc==`) → `Bearer [redacted]` | any HTTP API |
| Authorization header | `Authorization: Basic …` → `Authorization: Basic [redacted]`; `map[Authorization:[Bearer …]]` → `map[Authorization:[Bearer [redacted]]]`; `HTTP_AUTHORIZATION=Basic …` → `HTTP_AUTHORIZATION=Basic [redacted]` | any HTTP API. The name may follow `_` (an environment variable). The value goes to the end of the line, a `]` or a closing quote. A known scheme word (the IANA list plus `Token`, `ApiKey`, `SSWS`, AWS) is kept; any other first word may be the credential, so `Authorization: abc123 failed` → `Authorization: [redacted]` |
| Cookie and token headers | `Cookie: …`, `Set-Cookie: …`, `PROXY_COOKIE=…`, `Private-Token: …` (any `…-Token:`) → name + `[redacted]` | session cookies; GitLab-style token headers. A cookie name may follow `_` and take `=` |
| Private key block | `-----BEGIN … PRIVATE KEY-----` (PEM) or `… PRIVATE KEY BLOCK-----` (PGP) to its END line, or to the end of the text when cut short → `[redacted]` | the hub's CA key and SSH keys (02g); signing keys |
| Secret-named value | see below | a variable, flag or key whose name ends in a secret word |
| URL query value | `?code=abc&state=x` → `?code=[redacted]&state=[redacted]`; a fragment too: `#code=abc` → `#code=[redacted]` | 02e: every value, not just ones that look secret. OAuth redirects carry codes and tokens in the fragment |
| Password in a URL | `postgres://al:hunter2@db` → `postgres://al:[redacted]@db`; `https://al@example.com:pw@host` → `https://[email]:[redacted]@host`; with no scheme, `al@example.com:pw@host` → `[email]:[redacted]@host` | 02e; up to the last `@` before the path. The user name may hold `@` (an email, or `al@corp`); with no scheme, only an email user name marks the shape |
| Token as a URL user name | `https://ghp_…:x-oauth-basic@github.com` → `https://ghp_[redacted]:[redacted]@github.com` | the user name is scrubbed by the rules above |
| Email | `al@example.com` → `[email]` | 02e; 01e hides emails by default |

**Secret words:** password, passwd, passphrase, secret, token, salt, and api,
access, private, secret, ca or server key (`_`, `-` or nothing between the two
words; any case). salt and the CA and server keys are the fleet's own (02g).
The name may have any prefix (`GITHUB_TOKEN`, `db_password`, `account_salt`).
Two short words need more: `pass` must start a word or follow `_` (`DB_PASS`,
`--pass`, not `bypass`), and `pwd` must follow `_`, `-` or `;` (`DB_PWD`,
`--pwd`, `;Pwd=` in a connection string), because a bare `PWD` is the shell's
working folder. These forms count:

| Form | Example in, out |
|---|---|
| `NAME=value` | `GITHUB_TOKEN=abc` → `GITHUB_TOKEN=[redacted]` |
| bracketed name | `os.environ["TOKEN"] = "abc"` → `os.environ["TOKEN"] = "[redacted]"`; `env['API_KEY']='k'` likewise |
| quoted value (quotes kept) | `--password='p w'` → `--password='[redacted]'`; `TOKEN="a b"` → `TOKEN="[redacted]"` |
| quote never closed | `token='abc def` → `token='[redacted]`, to the end of the line |
| flag and value | `--password hunter2`, `--api-key x` → `--password [redacted]`, `--api-key [redacted]`; not a flag that starts with `no-` (`--no-password` takes no value) |
| `name: value` | `db_password: hunter2` → `db_password: [redacted]`; also `X-Api-Key:`, `refresh_token:`, `access_token:` |
| JSON, Python, escaped JSON | `"api_key": "…"`, `'token': '…'`, `\"token\": \"…\"` → value `[redacted]`, quotes kept; an escaped quote inside the value (`"ab\"cd"`) stays inside it. A bare JSON or dict value goes too: `{"password": 12345}` → `{"password": [redacted]}` (not an object or array) |

`name: value` does not count for token, pass or pwd: `input_tokens: 1234` is
a count, `token: expired` is prose, and `--- PASS: TestX` is a test result.
The OAuth names `refresh_token` and `access_token` do count. A token in that
spot is still caught by its prefix when it has one.

**Left alone on purpose:** `input_tokens: 1234`, `tokens: 12`,
`token: expired`, `max_tokens=4096` (the name must end in the word),
`bypass=true`, `--bypass`, `--- PASS: TestX`, `pass: 12`, `PWD=/srv/x`,
`"pwd": "/srv/x"`, `--no-password`, `cookies: 3`, `the bearer token expired`,
`issue #12`, `--verbose`, `task-runner` (`sk-` must start a word),
`ssh al@mac-mini` (no domain), host and port pairs, URLs with no query.

**Considered and not added:** GitLab (`glpat-`), Slack (`xoxb-`, `xoxp-`),
npm (`npm_`), PyPI (`pypi-`) and Hugging Face (`hf_`) token prefixes. By the
rule above they do not qualify: GitLab is a later code host (ADR 0012, 03m),
not v1, and nothing in the specs has a fleet machine talk to the others. A
GitLab token in its own header (`Private-Token: glpat-…`) or in a
secret-named variable is still removed. Adding one is a one-line change here
and in 02e when a spec needs it.

**Edges, chosen and tested:**

- A query value runs to whitespace, a quote, `<`, `>`, a backtick, `&` or
  `#`, and a `#` starts the next `name=value`. So `this.#n=1` in code loses
  its `1`; a fragment value is worth more. A `)` is part of it (`?token=ab)cd` → `?token=[redacted]`), so in
  `(see https://x/?a=b)` the `)` goes too. A URL may hold `)`, and keeping a
  secret's tail is worse than losing a bracket.
- A GitHub SSH remote looks like an email and goes like one:
  `git@github.com:o/r.git` → `[email]:o/r.git`.
- Every email goes. Row 8 (C2) needs a way to keep the account email on a
  usage screen when `share_identity_email` is on; that option is not built
  here, and row 8 adds it.

## How the rules are written

All rules are joined into one regular expression (Go `regexp`, which runs in
time linear in the input), and one round runs it once over the text. In each
rule's pattern, the capture groups hold the secret, one group per
alternative; the rest of the match is kept and the placeholder replaces the
group. A placeholder alone matches no rule, and a rule whose value can hold a
placeholder takes it whole (`?a=[redacted]` matches again as itself), so a
round leaves its own placeholders as they are. A rule asserts nothing to the
right of a match, so cutting settled text outside every match never makes a
new match. Text with no secret costs one round; text with secrets costs at
least two (one to redact, one to confirm nothing changed).

## Speed

Measured on an Apple M4 Pro (12 cores, other work running at a load average
near 4, so not an idle machine), Go 1.27.2,
`go test -run '^$' -bench BenchmarkText -count=10 ./internal/scrub/`
(min, mean and max of the 10 runs). "Before" is the single-round scrubber
with the earlier shapes, measured back to back with "after" on the same
machine; it matches this table's earlier numbers (548 µs mean) within 1%.

| Input | Before | After |
|---|---|---|
| a 1 KiB command message (`BenchmarkTextMessage`) | 541, 549, 557 µs | 1394, 1417, 1442 µs |
| 64 KiB of chained secrets, the slowest shape (`BenchmarkTextWorst`) | 61, 62, 63 ms | 137, 140, 142 ms |
| 64 KiB, the other shapes | 2 to 61 ms | 2 to 137 ms |
| 1 MiB of plain text, cap 1 KiB (`BenchmarkTextMiB/plain`) | 587, 599, 607 ms | 811, 820, 830 ms |
| 1 MiB full of secrets, cap 1 KiB (`BenchmarkTextMiB/secrets`) | 558, 561, 565 ms | 1440, 1452, 1474 ms |

Text with a secret now costs at least two rounds, one to redact and one to
confirm; that is about 2 times. The rest (about 1.4 times on plain text) is
the cost per byte of the shapes this change added. No single new shape
dominates: dropping the no-scheme email password, the largest, saved 7%.

**Worst-case growth.** A pattern that can start inside a run of word
characters (an email's local part, a URL scheme, a `-Token` header name) has a
bounded length before its first fixed character. Unbounded, an attempt starts
again after each match and runs to the end of the run, so the time grows with
the square of the length. `TestTextWorstCaseStaysLinear` times 16 KiB and
64 KiB (best of 5 each) and fails if 64 KiB takes more than 8 times as long:
bounded it is 3.7 to 4.1 times across its nine shapes (the repeated rounds
and the new shapes did not change that); with the bounds removed it was 14.5
to 15.3 times and the test failed. A ratio, not a time limit, so a slow machine does
not fail it.

**Load budget.** 04 Q7 budgets the hub at 2,500 events a second during a
replay. Only command results, command progress and probe health carry free
text, and the cost grows with the length (1 KiB is the cap, not the usual
size). If every event in that burst carried a full 1 KiB message holding
secrets, scrubbing would need about 3.5 cores of this machine (2,500 × the
1,417 µs mean; 1.4 cores before this change). The hub's own pass is cheaper:
text the agent already scrubbed settles in its first round, so it costs one
round, not two. `BenchmarkTextScrubbed` measures it at 648 to 669 µs per 1 KiB
message (5 runs), about 1.7 cores for the same all-1-KiB burst. So the hub scrubs in its ingest
goroutines, before it hands a batch to its single writer, never inside the
write transaction, where it would hold the writer for that long. The agent
scrubs one message at a time as it writes it.

Tests: `CGO_ENABLED=0 go test ./internal/scrub/`. Fuzz:
`go test -run '^$' -fuzz FuzzText ./internal/scrub/` (also in `make fuzz`).
