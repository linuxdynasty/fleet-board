# 04b. Hub scrubber cost

- Status: Accepted (a measurement and its decision)
- Date: 2026-10-10
- Part of: [README](README.md), step 4; follows [04](04-technology-review.md) Q7 (replay budget)
- Output: `internal/scrub/hub_pass_test.go` (the benchmarks),
  [`spikes/scrubcost/results/`](../../spikes/scrubcost/results/README.md) (raw output)
- Plan: row 5l, issue #33; the re-checks are rows 6 and 17

## Question and answer

The hub scrubs the free text of messages the agent has already scrubbed
([02e](02e-contract-rules.md), "Size limits"). #33 asked, before the hub's
ingest lands: is that pass cheap enough, or does the scrubber need a faster
first-pass scan?

**Decision.**

1. **No faster first-pass scan is needed before the first producer.** At the
   real rates the hub's scrubbing is under 1 % of one core even on the
   heaviest realistic model (`StartGoal` steps at the peak command rate, every
   message the worst text found: 0.10 core, 0.8 % of the 12 cores measured on).
2. **Scrubbing stays in the per-stream ingest goroutine**, never in the writer
   transaction (03f, "Backpressure chain").
3. **The hub bounds the input before it scrubs** (the finding below): a string
   over its field's cap is cut to the cap or its message rejected, *before*
   `scrub.Scrub` runs. Without that, the cost follows the input's length, and
   a 1 MiB `message` takes about 16 s.
4. **Re-check on real sizes.** This used the 02e caps, not real sizes, because
   no agent sends free text yet. Row 6 (the command executor) records its real
   messages and pastes their size distribution and the hub scrub cost before it
   merges; row 17's seven-day run re-checks. More than 1 core hub-wide on real
   sizes means a cheaper first-pass scan before that row merges.

## What the hub scrubs, and how often

| Free text | Cap (02e) | Carrier | Rate, hub-wide (source) |
|---|---|---|---|
| `CommandResult.message` | 1 KiB | `ConnectRequest` arm | commands 0.05 / s steady, 0.5 / s peak (03f, `commands` row) |
| `CommandProgress.message` | 1 KiB | `ConnectRequest` arm | "+ progress steps" (03f gives no number). Modeled two ways: 5 per command (assumed) and the 14 `StartGoal` steps (`command_goal.proto`), so 7.5 msg/s at the peak |
| `UsageReading.parse_message` | 1 KiB | `Event` arm (coalesced) | passive readings at most 0.07 / s steady, 0.8 / s worst (03f, `usage_readings` row) |
| `ProbeHealth.message` | 256 B | `Event` arm (coalesced), `Snapshot.probes` | about 0 (03f, `probes` row) |
| `SettingRejection.message` | 256 B | `SettingsAck` | rare (a settings change) |
| The insights report | 64 KiB | the verifier (05, section 7) | one a week |

Only the two `Event` arms can ride the replay burst of 04 Q7 (2,500 events/s
per hub, 200 per agent), and both are coalesced: a replay carries at most one
per key. `CommandResult` and `CommandProgress` are not events, so the real
fraction of the burst that carries text is far below 1 %. The 100 % rows
below are bounds, not cases.

## Method

- The benchmarks `BenchmarkHubPass` (one message per corpus and cap) and
  `BenchmarkHubLoad` (one window of a modeled load per iteration; the `cores`
  metric is the iteration's CPU time divided by the window, so CPU-seconds
  per second) in `internal/scrub/hub_pass_test.go`.
  `go test -run '^$' -bench BenchmarkHub -count=10 ./internal/scrub/`, read
  with `benchstat`. The hub's call is modeled as `scrub.Scrub` with no home
  folder (only the agent passes one).
- Base commit 83b9330 plus the benchmark. Apple M4 Pro, 12 cores, 64 GB,
  macOS 27.0.1, Go 1.27.2 darwin/arm64. The machine was not idle (other work
  on it): load average 3.5 to 5 for run 1, 5 to 16 for run 2. The output of
  both runs is in `spikes/scrubcost/results/`.
- **Corpora** (the text the hub pass receives):
  - *typical*: an ordinary error line of about 165 bytes, already scrubbed by
    the agent (an assumed size);
  - *full mix*: a full-cap message, already scrubbed, mostly benign text with
    two secret sentences per KiB, each leaving three placeholders (a URL
    query value, an email, a GitHub token), six in all;
  - *unscrubbed*: the same text before any agent pass (an agent that did not
    scrub, or an old one); the first pass does all the work, in two rounds;
  - *dense*: a full-cap message that is only secrets (cheaper than the mix:
    a match is consumed in one step and the text shrinks);
  - *hostile*: four glued keys of alternating kinds followed by repeated
    `--n`, built so every one of the four rounds runs and the field becomes
    `[redacted]` (`Unsettled`). The costliest text found.
  `TestHubCorporaKeepTheirPremise` fails if a rule change makes any corpus stop
  being what its name says.
- **Real sizes hook.** `BenchmarkHubPassFile` runs the hub pass over the
  messages in the file named by `FLEET_SCRUB_BENCH_CORPUS` (one JSON string per
  line, as they arrived from the agents) and reports `cores@peak` (7.5 msg/s)
  and `cores@burst` (2,500 msg/s). Row 6 records its messages this way.

## Results

Per message (`BenchmarkHubPass`, run 1, spread 1 to 3 %):

| Corpus | 256 B cap | 1 KiB cap |
|---|---|---|
| typical (about 165 B) | 138 µs | 139 µs |
| full mix | 223 µs | 897 µs |
| unscrubbed | 419 µs | 1.74 ms |
| dense | 80 µs | 133 µs |
| hostile | 2.15 ms | 12.9 ms |

Short strings pay mostly a fixed cost per call (a 53-byte string took 140 µs).
Allocation (`-benchmem`, run 2): the hostile 1 KiB message allocates 30 KiB in
38 allocations, the full mix 6 KiB in 10, so memory is not a constraint.

Load (`BenchmarkHubLoad`, 1 KiB cap). "Cores" is CPU-seconds per second summed
over every stream, so every row is **hub-wide, all streams**. The 2,500
events/s of the burst are 12.5 streams at the 200 events/s agent cap, so one
stream's goroutine spends 1/12.5 of the burst rows (see "One stream").

| Load | Messages/s | Cores | Of 12 |
|---|---|---|---|
| steady, typical | 0.3 | 0.00004 | 0.0 % |
| peak (5 progress per command): typical / full mix / unscrubbed / hostile | 3 | 0.0004 / 0.0027 / 0.0053 / 0.040 | ≤ 0.3 % |
| usage readings, worst rate: full mix / hostile | 0.8 | 0.0007 / 0.011 | ≤ 0.1 % |
| peak with 14 `StartGoal` steps: full mix / hostile | 7.5 | 0.0068 / 0.098 | 0.06 % / 0.8 % |
| burst, 1 % of events carry text, full mix | 25 | 0.023 | 0.2 % |
| burst, 10 % | 250 | 0.23 | 1.9 % |
| burst, every event (a bound, not a case): full mix / unscrubbed / dense | 2,500 | 2.2 to 2.5 / 4.4 / 0.34 | 19 to 21 % / 37 % / 3 % |

The steady, peak and burst rows are from run 1; the usage and `StartGoal`
rows are from run 2, because those scenarios were added after run 1. Rows
in both runs agree (peak full mix 0.0027 and 0.0029, peak hostile 0.040 and
0.039, 2,500 full mix 2.47 and 2.25). Run 1's peak hostile row spread ±29 %:
an iteration is a 0.4 s window and few iterations fit in the default
benchtime, so a burst of other work on the machine shows. The repeat spread
±3 %.

**One stream.** At the 200 events/s agent replay cap, one stream's ingest
goroutine spends about 0.18 to 0.2 core on the full mix and 0.35 on unscrubbed
text. Hostile text at that rate would need 2.6 cores, so that stream is
CPU-bound at about 77 messages a second and backs up only itself (03f,
"Backpressure chain").

## The input must be bounded before the scrub

`scrub.Scrub` scrubs everything and then cuts to `MaxBytes`, so its cost
follows the *input's* length, not the cap. The same hostile text, one call:

| Input | Time |
|---|---|
| 1 KiB (the cap) | 13 ms (19 ms in a review run) |
| 64 KiB | 0.97 s |
| 1 MiB | 15.6 s |

The 1 MiB message cap on `Connect` allows a 1 MiB `message`. An enrolled agent
that sent one would hold its stream's ingest goroutine for about 16 s, and
repeating it keeps the goroutine busy; the numbers above assume the cap is
enforced. So the rule, now in 02e and in the acceptance of the first hub
ingest PRs (plan row 5):

> The hub never calls the scrubber on more bytes than the field's cap. A
> string over its cap is cut to the cap on a UTF-8 boundary, or its message is
> rejected, before `scrub.Scrub` runs.

A cut before the scrub can leave the start of a secret that straddles the cap,
too short for its rule to match. An honest agent never sends over the cap (it
scrubbed and cut already), so only a misbehaving one is affected; rejecting
avoids it entirely. The ingest PR picks one and tests it with a seeded
hostile message far over its cap.

## What this does not cover

- Real message sizes and text (row 6 supplies them; row 17 repeats).
- Machines other than the Apple M4 Pro. On a hub with 4 cores of the same
  speed the shares are three times those above: 2.5 % for the heaviest
  realistic row.
- The cost of the scrub being called at all: the hub's telemetry for `Changed`,
  `Cut` and `Unsettled` belongs to the ingest PRs (05b).

## Raw excerpt

`benchstat` of run 1, `sec/op` (the files in `spikes/scrubcost/results/` hold
every sample):

```
HubPass/256B/typical-12       138.2µ ± 1%     HubPass/1KiB/typical-12      138.6µ ± 2%
HubPass/256B/mix-12           223.4µ ± 2%     HubPass/1KiB/mix-12          896.5µ ± 2%
HubPass/256B/unscrubbed-12    418.9µ ± 2%     HubPass/1KiB/unscrubbed-12   1.743m ± 2%
HubPass/256B/dense-12         79.76µ ± 1%     HubPass/1KiB/dense-12        133.0µ ± 2%
HubPass/256B/hostile-12       2.148m ± 2%     HubPass/1KiB/hostile-12      12.93m ± 2%
```

`benchstat` of run 2, `cores` (the burst rows ran at a load average of 5 to 10):

```
HubLoad/peak/mix-12                  2.873m ± 3%    HubLoad/peak_startgoal/mix-12       6.802m ± 3%
HubLoad/peak/hostile-12              39.23m ± 3%    HubLoad/peak_startgoal/hostile-12   98.02m ± 1%
HubLoad/usage_worst/mix-12           728.2µ ± 3%    HubLoad/burst_10pct/mix-12          226.8m ± 5%
HubLoad/usage_worst/hostile-12       10.60m ± 3%    HubLoad/burst_all/mix-12             2.250 ± 2%
HubLoad/burst_all/unscrubbed-12       4.413 ± 3%    HubLoad/burst_all/dense-12          352.2m ± 8%
```
