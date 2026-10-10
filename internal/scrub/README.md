# internal/scrub

The shared secret scrubber (02e, "Size limits"; plan row 2c). Free text the
agent writes (`CommandResult.message`, `CommandProgress.message`,
`parse_message`, `ProbeHealth.message`) goes through it on the agent before it
is sent (row 6), and again on the hub before it is stored (row 5). The hub also
scrubs the insights report with it (05, "Verification").

```go
r := scrub.Scrub(msg, scrub.Options{MaxBytes: 1024, Home: home}) // agent: home folder becomes ~
msg = scrub.Text(msg, scrub.Options{MaxBytes: 1024})             // hub: same rules, no home folder
```

`Text` returns the scrubbed text. `Scrub` returns a `Result`: the same text,
plus three flags for the caller's own telemetry, since the package has no
I/O, logs or metrics:

- `Changed`: a secret or the home folder was replaced (turning invalid UTF-8
  into U+FFFD alone does not count);
- `Cut`: the scrubbed text was longer than the cap and was cut (or the cap is
  zero or less and the input was not empty);
- `Unsettled`: the rounds ran out and the field became `[redacted]` (step 5).

Rows 5 and 6 count messages with each flag set.

What `Scrub` does, in order:

1. Turns bytes that are not valid UTF-8 into U+FFFD.
2. When `Home` is set, writes each whole occurrence of it as `~` and keeps the
   rest of the path: `/Users/al/repos/x` becomes `~/repos/x`, and
   `file:///Users/al/x` becomes `file://~/x`, but `/Users/alice`,
   `/Users/al.bak` and `/srv/Users/al` stay. A home is accepted only if it is
   absolute, not `/`, and made of name characters (letters, digits, `.`, `_`,
   `-`, any non-ASCII) and `/`; anything else is ignored. A home path is
   personal data, not a secret (#139), so only the agent passes it.
3. Removes private key blocks, then swaps each other secret for a
   placeholder, keeping the part that says what it was (see the table). Key
   blocks go first so a name before one (`PRIVATE_KEY=-----BEGIN …`) cannot
   end its value at the key's first space.
4. Does step 2 again. Whether the home is a whole name depends on the bytes
   around it, and those may have been part of a secret: in
   `/Users/al` + `x@b.co`, the home is only whole once the email is
   `[email]`.
5. Repeats steps 3 and 4 until a round changes nothing. A redaction can
   delete the byte that stopped another rule (a `]`, a quote, a `#`, a space)
   or make a home whole, and the next round then finds a new secret: in
   `?secret=#;-----END` the `#` becomes `[redacted]` in round 1, and round 2
   sees the query value run on through `;-----END`. Rounds are bounded at 4
   (3 that change the text and 1 that confirms). Redaction can make text
   longer (`a=b` becomes `a=[redacted]`), so the bound, not the length, is
   what ends the loop. Most text with a secret settles in 2 rounds. Glued
   keys of different kinds take one round per key, because each key's word
   boundary appears only once its left neighbour is a placeholder:
   `AKIA…AIza…AKIA…` takes 3 changing rounds and comes back scrubbed, and
   four such keys come back as `[redacted]` whole, with `Unsettled` set.
   Text that has not settled after 4 rounds goes whole rather than being sent
   with a secret a further round might have found.
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
secret case; `TestSettleRoundBound` pins the bound from both sides.

**Callers bound the input.** `Scrub` costs time in proportion to its input,
not its cap: it scrubs everything, then cuts. 1 MiB takes 0.9 to 1.6 s, and
64 KiB built to be slow takes up to 0.9 s (see Speed). A caller with text of
no fixed size, such as a command's stderr, cuts it first to a bound far above
the cap, such as 64 KiB (the largest field, the insights report), on a UTF-8
boundary. That first cut can split a secret at the bound, and the part left
before it may be too short for its rule (a token under its minimum length).
That part reaches the output only if scrubbing shrinks everything before it
below the cap, which a bound many times the cap makes unlikely, not
impossible.

## What counts as a secret

02e names tokens, emails, URL query values and passwords in URLs, key blocks
and `curl -u`. It does not say which token shapes count, so this list was chosen here. The
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
| AWS access key id | `AKIA…` → `AKIA[redacted]`, `ASIA…` → `ASIA[redacted]`; several glued together go as one | Claude Code on Amazon Bedrock |
| Google API key | `AIza` + 35 chars → `AIza[redacted]`; several glued together go as one | Gemini through OpenCode |
| JSON Web Token | `eyJ….eyJ….sig` → `[redacted]` | bearer tokens of many services |
| Bearer token | `Bearer <16+ chars>`, or fewer ending in `=` padding (`Bearer abc==`) → `Bearer [redacted]` | any HTTP API |
| Authorization header | `Authorization: Basic …` → `Authorization: Basic [redacted]`; `map[Authorization:[Bearer …]]` → `map[Authorization:[Bearer [redacted]]]`; `HTTP_AUTHORIZATION=Basic …` → `HTTP_AUTHORIZATION=Basic [redacted]` | any HTTP API. The name may follow `_` (an environment variable). The value goes to the end of the line, a `]` or a closing quote. A known scheme word (the IANA list plus `Token`, `ApiKey`, `SSWS`, AWS) is kept; any other first word may be the credential, so `Authorization: abc123 failed` → `Authorization: [redacted]` |
| Cookie and token headers | `Cookie: …`, `Set-Cookie: …`, `PROXY_COOKIE=…`, `Private-Token: …` (any `…-Token:`) → name + `[redacted]` | session cookies; GitLab-style token headers. A cookie name may follow `_` and take `=` |
| Private key block | `-----BEGIN … PRIVATE KEY-----` (PEM) or `… PRIVATE KEY BLOCK-----` (PGP) to its END line, or to the end of the text when cut short → `[redacted]`, also as a named value (`PRIVATE_KEY=-----BEGIN …` → `PRIVATE_KEY=[redacted]`) | the hub's CA key and SSH keys (02g); signing keys |
| Secret-named value | see below | a variable, flag or key whose name ends in a secret word |
| URL query value | `?code=abc&state=x` → `?code=[redacted]&state=[redacted]`; a fragment too: `#code=abc` → `#code=[redacted]` | 02e: every value, not just ones that look secret. OAuth redirects carry codes and tokens in the fragment |
| Password in a URL | `postgres://al:hunter2@db` → `postgres://al:[redacted]@db`; `https://al@example.com:pw@host` → `https://[email]:[redacted]@host`; with no scheme, `al@example.com:pw@host` → `[email]:[redacted]@host` | 02e; up to the last `@` before the path. The user name may hold `@` (an email, or `al@corp`); with no scheme, only an email user name marks the shape |
| curl user and password | `curl -u al:hunter2 …`, `curl --user 'al:pa:ss' …` → `al:[redacted]` | a password on a curl command line. Only within 256 bytes after `curl`: `docker run -u 1000:1000` is a user and group |
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
| `NAME=value` | `GITHUB_TOKEN=abc` → `GITHUB_TOKEN=[redacted]`; the value runs to whitespace or a quote, so `DB_PASSWORD=Xy7&k;9q,Zr` goes whole |
| bracketed name | `os.environ["TOKEN"] = "abc"` → `os.environ["TOKEN"] = "[redacted]"`; `env['API_KEY']='k'` likewise |
| quoted value (quotes kept) | `--password='p w'` → `--password='[redacted]'`; `TOKEN="a b"` → `TOKEN="[redacted]"` |
| quote never closed | `token='abc def` → `token='[redacted]`, to the end of the line |
| flag and value | `--password hunter2`, `--api-key x` → `--password [redacted]`, `--api-key [redacted]`; the value runs to whitespace or a quote; not a flag that starts with `no-` (`--no-password` takes no value) |
| connection string | `Server=db;Uid=al;Password=hunter2;Database=x` → `…;Password=[redacted];Database=x`: after a `;`, the value ends at the next `;` |
| `name: value` | `db_password: hunter2` → `db_password: [redacted]`; also `X-Api-Key:`, `refresh_token:`, `access_token:`; the value ends at whitespace, a quote, `&`, `;` or `,` |
| JSON, Python, escaped JSON | `"api_key": "…"`, `'token': '…'`, `\"token\": \"…\"` → value `[redacted]`, quotes kept; an escaped quote inside the value (`"ab\"cd"`) stays inside it. A bare JSON or dict value goes too: `{"password": 12345}` → `{"password": [redacted]}` (not an object or array) |
| Ruby and PHP `=>` | `"password" => "x"`, `'api_key' => 'k'`, `:password => "x"` → value `[redacted]`, quotes kept |

`name: value` does not count for token, pass or pwd: `input_tokens: 1234` is
a count, `token: expired` is prose, and `--- PASS: TestX` is a test result.
The OAuth names `refresh_token` and `access_token` do count. A token in that
spot is still caught by its prefix when it has one.

**Left alone on purpose:** `input_tokens: 1234`, `tokens: 12`,
`token: expired`, `max_tokens=4096` (the name must end in the word),
`bypass=true`, `--bypass`, `--- PASS: TestX`, `pass: 12`, `PWD=/srv/x`,
`"pwd": "/srv/x"`, `--no-password`, `cookies: 3`, `the bearer token expired`,
`issue #12`, `--verbose`, `task-runner` (`sk-` must start a word),
`ssh al@mac-mini` (no domain), `docker run -u 1000:1000`, host and port
pairs, URLs with no query.

**Not covered (known leaks, left for a later change):**

- A name with no value of its own takes the next word as its value, even
  another secret's name: `API_KEY= DB_PASSWORD='my secret'` →
  `API_KEY= [redacted]'my secret'`; `password: --token Zq7Xk` →
  `password: [redacted] Zq7Xk`; `token= secret = Zq7Xk` →
  `token= [redacted] = Zq7Xk`. A pattern that skipped to the second name made
  all text about 30% slower, so it was not added.
- A password in a `.netrc` line: `machine db login al password hunter2`.
- `docker login -p hunter2` and `mysql -phunter2`: short flags with no
  secret name.
- A connection string that starts with a bare `Pwd`: `Pwd=x;Server=y` (a bare
  `PWD` is the shell's working folder).
- A `name: value` value holding `&`, `;` or `,` keeps its tail:
  `password: ab,cd` → `password: [redacted],cd`.
- A bearer token outside an Authorization header that is short and has no
  `=` padding, or sits on the line after `Bearer`: `Bearer abc123`.
- Tokens of a variable length glued together: in `ghp_<16+>ghp_<16+>` the
  first token's body takes the next `ghp`, and the second body stays.

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
  its `1`; a fragment value is worth more. A `)` is part of it
  (`?token=ab)cd` → `?token=[redacted]`), so in `(see https://x/?a=b)` the
  `)` goes too. A URL may hold `)`, and keeping a secret's tail is worse than
  losing a bracket.
- A GitHub SSH remote looks like an email and goes like one:
  `git@github.com:o/r.git` → `[email]:o/r.git`.
- Every email goes. Row 8 (C2) needs a way to keep the account email on a
  usage screen when `share_identity_email` is on; that option is not built
  here, and row 8 adds it.

## How the rules are written

Private key blocks have their own pattern, run first in each round. All
other rules are joined into one regular expression (Go `regexp`, which runs in
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
of 2 to 4, so not an idle machine), Go 1.27.2,
`go test -run '^$' -bench BenchmarkText -count=5 ./internal/scrub/`
(min, mean and max of the 5 runs). "Before" is this branch before the round-3
review fixes (5304d0a), measured back to back with "after" on the same
machine. Before the repeated rounds and the round-2 shapes, a 1 KiB message
took 549 µs.

| Input | Before | After |
|---|---|---|
| a 1 KiB command message (`BenchmarkTextMessage`) | 1399, 1437, 1492 µs | 1548, 1571, 1599 µs |
| the hub's pass over that message, already scrubbed (`BenchmarkTextScrubbed`) | 662, 680, 714 µs | 739, 743, 749 µs |
| 1 MiB of plain text, cap 1 KiB (`BenchmarkTextMiB/plain`) | 808, 819, 843 ms | 882, 886, 890 ms |
| 1 MiB full of secrets, cap 1 KiB (`BenchmarkTextMiB/secrets`) | 1435, 1487, 1552 ms | 1600, 1620, 1641 ms |
| 64 KiB of glued Google keys (`BenchmarkTextWorst`) | 474, 479, 483 ms | 0.77, 0.79, 0.80 ms |
| 64 KiB of `--n` repeated, one round, no match | 208, 212, 219 ms | 223, 227, 236 ms |
| 64 KiB, the other nine shapes | 2 to 141 ms | 2 to 152 ms |
| 64 KiB of `--n` after four alternating glued keys: every round, then `[redacted]` (`BenchmarkTextHostile`) | not measured | 882, 899, 910 ms |

The new shapes cost about 9% per byte. Glued Google keys took four rounds
before (and failed closed); they now go in one. `--n` repeated is the slowest
text per round found so far (a reviewer's search; the cause was not
profiled). The most a field can cost is about 4 times its slowest round,
which `BenchmarkTextHostile` shows: about 0.9 s for 64 KiB.

**Worst-case growth.** A pattern that can start inside a run of word
characters (an email's local part, a URL scheme, a `-Token` header name) has a
bounded length before its first fixed character. Unbounded, an attempt starts
again after each match and runs to the end of the run, so the time grows with
the square of the length. `TestTextWorstCaseStaysLinear` times 16 KiB and
64 KiB (best of 5 each) and fails if 64 KiB takes more than 8 times as long:
bounded it is 3.2 to 4.0 times across its eleven shapes; with the bounds
removed it was 14.5 to 15.3 times and the test failed. A ratio, not a time
limit, so a slow machine does not fail it.

**Load budget.** 04 Q7 budgets the hub at 2,500 events a second during a
replay. Only command results, command progress and probe health carry free
text, and the cost grows with the length (1 KiB is the cap, not the usual
size). The numbers below assume every event in that burst carries a full
1 KiB message like `BenchmarkTextMessage`, which settles in 2 rounds. The
agent's pass would need about 3.9 cores of this machine (2,500 × 1,571 µs).
The hub's pass over text the agent already scrubbed settles in its first
round: about 1.9 cores (2,500 × 743 µs). A message built to need 4 rounds of
the slowest text costs about 4 times as much per byte, so a burst of those
would not fit; the cap keeps each one to 1 KiB, about 13 ms
(measured at 1 KiB: `BenchmarkHubPass/1KiB/hostile`). So the hub scrubs
in its ingest goroutines, before it hands a batch to its single writer, never
inside the write transaction, where it would hold the writer for that long.
The agent scrubs one message at a time as it writes it.

**Hub pass, measured (row 5l, 2026-10-10).** `hub_pass_test.go` models what the
hub receives, at the 02e caps (1 KiB: `CommandResult.message`,
`CommandProgress.message`, `parse_message`; 256 B: `ProbeHealth.message`,
`SettingRejection.message`). `go test -run '^$' -bench BenchmarkHub -count=10
./internal/scrub/`, benchstat, base 83b9330 plus the benchmark, Apple M4 Pro
(12 cores), macOS 27.0.1, Go 1.27.2, load average 3.5 to 5, so not an idle
machine. Per message, one `Scrub` call with no home folder (spread 1 to 2 %):

| Corpus | 256 B | 1 KiB |
|---|---|---|
| typical: an error line of about 165 B, agent-scrubbed | 138 µs | 139 µs |
| mix: agent-scrubbed, mostly benign, two placeholders per KiB | 223 µs | 897 µs |
| unscrubbed: the same text before any agent pass (two rounds) | 419 µs | 1.74 ms |
| dense: nothing but secrets (cheaper: a match is consumed, the text shrinks) | 80 µs | 133 µs |
| hostile: uses all four rounds, ends `[redacted]` | 2.15 ms | 12.9 ms |

The README figures above still hold on the same day (`BenchmarkTextMessage`
1.59 ms, `BenchmarkTextScrubbed` 755 µs). `BenchmarkHubLoad` runs a window of
each modeled load and reports `cores`, the CPU-seconds one goroutine spends
per second. Command results and progress run at 0.05 / s steady and 0.5 / s
peak (03f) with 5 progress messages per command (assumed), probes at about
0. In the 2,500 events / s replay burst only coalesced `ProbeHealth` and
`parse_message` ride events (`CommandResult` and `CommandProgress` are not
events), so 100 % carrying text is a bound, not a case:

| Load (1 KiB) | Cores | Of 12 |
|---|---|---|
| steady 0.3 msg / s, typical | 0.00004 | 0.0 % |
| peak 3 msg / s: typical, mix, unscrubbed, hostile | 0.0004, 0.0027, 0.0053, 0.040 (±29 %) | ≤ 0.3 % |
| burst, 1 % of 2,500 events carry a mix message | 0.023 | 0.2 % |
| burst, 10 % | 0.23 | 1.9 % |
| burst, 100 %: mix, unscrubbed, dense | 2.5, 4.4, 0.34 | 21 %, 37 %, 3 % |

The insights report (64 KiB, one a week: 05, section 7) is left out of the
table: at its worst, `BenchmarkTextHostile` takes 0.93 s (±11 %, same day),
so a weekly report is about 0.000002 core. `SettingRejection.message` (256 B)
is rare and costs what the 256 B column shows.

One stream's ingest goroutine at the 200 events / s agent cap spends 0.18 core
on the mix and 0.35 unscrubbed. Hostile text at that rate would need 2.6 cores,
so that stream is CPU-bound at about 77 messages a second and backs up only
itself. Conclusion: no faster first-pass scan is needed before the first
producer; row 6 pastes the real `CommandResult.message` size distribution and
the hub scrub cost before it merges, and row 17 re-checks (04 Q7, "Hub scrubber
cost").

Tests: `CGO_ENABLED=0 go test ./internal/scrub/`. Fuzz:
`go test -run '^$' -fuzz FuzzText ./internal/scrub/` (also in `make fuzz`).
