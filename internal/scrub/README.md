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
2. When `Home` is set (an absolute folder, not `/`), writes each whole
   occurrence of it as `~` and keeps the rest of the path: `/Users/al/repos/x`
   becomes `~/repos/x`, but `/Users/alice` and `/srv/Users/al` stay. A home
   path is personal data, not a secret (#139), so only the agent passes it.
3. Swaps each secret for a placeholder, keeping the part that says what it
   was (see the table).
4. Cuts the result to `MaxBytes`. The cut never splits a character, never
   keeps half a placeholder (it backs up to before the whole replacement), and
   never ends on the home folder. A cap of zero or less gives `""`: every
   field has a cap.

Running `Text` on its own output returns it unchanged, so the hub's second pass
changes nothing the agent already scrubbed. `FuzzText` checks that, the cap and
valid UTF-8 for any text, home and cap.

## What counts as a secret

02e names four kinds: tokens, emails, URL query values and passwords in URLs.
It does not say which token shapes count, so this list was chosen here. The
rule for choosing: the token formats the fleet itself mints, the tokens of the
services a fleet machine talks to (GitHub, the harness model providers and the
clouds behind them), and the generic forms any of those take in an error
message. Each line is a pattern in `rules.go`; a new shape adds a line here
and a case in `scrub_test.go`.

| Shape | Example in, out | Why |
|---|---|---|
| Fleet enroll token | `fbe1_<87 chars>` → `fbe1_[redacted]` | the fleet's own (02g); any `fbe<N>_` version |
| Fleet API token | `fbt_<id>.<secret>` → `fbt_[redacted]` | the fleet's own (02f), used by the CLI |
| GitHub tokens | `ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_` → prefix + `[redacted]` | the GitHub integration and `gh` on every machine (03j) |
| `sk-` keys | `sk-ant-api03-…`, `sk-proj-…`, `sk-or-…` → `sk-[redacted]` | Anthropic, OpenAI and OpenRouter keys behind Claude Code, Codex and OpenCode |
| AWS access key id | `AKIA…`, `ASIA…` → `AKIA[redacted]` | Claude Code on Amazon Bedrock |
| Google API key | `AIza` + 35 chars → `AIza[redacted]` | Gemini through OpenCode |
| JSON Web Token | `eyJ….eyJ….sig` → `[redacted]` | bearer tokens of many services |
| Bearer token | `Bearer <16+ chars>` → `Bearer [redacted]` | any HTTP API |
| Authorization header | `Authorization: Basic …` → `Authorization: Basic [redacted]` | any HTTP API, any scheme |
| Private key block | `-----BEGIN … PRIVATE KEY-----` to its END line, or to the end of the text when cut short → `[redacted]` | the hub's CA key and SSH keys (02g) |
| Secret-named value | `GITHUB_TOKEN=…`, `--password='…'`, `"api_key": "…"` → name + `[redacted]` | a variable, flag or JSON key whose name ends in password, passwd, secret, token, api key, access key or private key |
| URL query value | `?code=abc&state=x` → `?code=[redacted]&state=[redacted]` | 02e: every value, not just ones that look secret |
| Password in a URL | `postgres://al:hunter2@db` → `postgres://al:[redacted]@db` | 02e; up to the last `@` before the path |
| Email | `al@example.com` → `[email]` | 02e; 01e hides emails by default |

Left alone on purpose: `input_tokens: 1234` (a name must end in the word, and
only `=` or a JSON key counts, not a bare colon), `task-runner` (`sk-` must
start a word), `ssh al@mac-mini` (no domain), host and port pairs.

## Speed

Pure standard library (`regexp`, which runs in time linear in the input), one
pass over the text with all rules joined. Three rules can start inside a run
of word characters (a variable name, an email's local part, a URL scheme); each
has a bounded length before its first fixed character. Unbounded, 64 KiB of
chained secrets took 3 to 13 s; bounded, 50 to 70 ms on the same machine
(`TestTextWorstCaseStaysLinear`, `BenchmarkTextWorst`). A 1 KiB message takes
about 0.5 ms (`BenchmarkTextMessage`), so the hub scrubs before it hands a
batch to its single writer, never inside the write transaction.

Tests: `CGO_ENABLED=0 go test ./internal/scrub/`. Fuzz:
`go test -run '^$' -fuzz FuzzText ./internal/scrub/` (also in `make fuzz`).
