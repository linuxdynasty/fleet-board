# internal/hub/pacing/probe

Probe assignment for one usage account (03b, "Probe assignment"). The
scheduler calls `Select` once per pass per account; the store slice persists
the result in the `probes` table and sends the commands.

`Select` takes the account, the candidate machines and the account's active
probe and returns what to do: `Assign` (a fresh `EnsureProbe` with `run_now`
on the best candidate), `Failover` (a `StopProbe` for the old machine plus a
fresh `EnsureProbe`), `RunNow` (re-issue `EnsureProbe` with `run_now` to the
active probe), `Stop` (tear down with no replacement), or `None`.

What it applies, in order: a removed account stops its probe; machines must
be `ONLINE` and not draining, carry the account key signed in (or the
owner's Codex mapping for that machine and profile), run a harness version
that reads usage (`SUPPORTED`, or `UNVERIFIED_PARSING` with the `USAGE`
parser verified or live), and advertise `usage.probe.<harness>`. Preference
is `always-on` machines first, then machines that are not laptops, then the
hub machine, then the most recently successful; the machine id breaks ties
so a pass is stable. An account with an active probe is never assigned
twice: only failover or `run_now`. Failover fires when the machine is
`OFFLINE` (never `STALE`, so a sleeping laptop keeps its probe) or after 3
consecutive failures; a machine that just failed 3 runs is skipped for the
re-pick. `run_now` runs at most once a minute per account (`GateRunNow`); a
passive reading 5 or more points off the latest probe reading asks for one
(`PassiveGap`), and it never paces alone.

`Step` moves one probe along the 03b state machine from its latest health;
`Reconcile` is the snapshot safety net that stops probe sessions the hub no
longer assigns to that machine.

Not here yet (later slices of issue #38): the `probes` table with its
unique-active rule, the writer and outbox wiring, the 30 s scheduler tick,
the `probe.failover` log and the `fleet_hub_probe_*` metrics.
