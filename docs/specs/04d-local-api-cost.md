# Local API writer cost evidence

Two isolated measurements cover the local API changes. They do not qualify the
full Q4/A50 load budget in [04](04-technology-review.md).

The [approval-list measurement](../../spikes/approval-list-acceptance/results/README.md)
compares the actual input validator with the former second page scan. Against
1,002 approvals, its median writer callback fell from 25.88 to 16.79 ms and
allocated 49.89% fewer bytes. Full Store.Write time fell 3.20%; its batch wait
is included. Both variants used the same process and fixture. The original
fixture volume was not recorded, so this evidence makes no disk-specific claim.

The [operation deadline measurement](../../spikes/deadline-cost/results/README.md)
compares the repair that returns the durable operation deadline with the former
wrong fresh reply. Across 20 five-call samples per arm, SendKeys callback median
increased by 0.204 ms and full RPC median by 0.15%. DecideApproval callback median
increased by 0.039 ms and full RPC median fell 0.07%. Allocation counts increased
11.18% and 5.32%, respectively. Neither predeclared investigation threshold
(+1 ms callback or +5% full RPC) was crossed. Both arms verified their expected
fresh reply, durable command and later public GetOperation values.

Each report includes raw rows, commands and exits, profile text, host details,
metric boundaries and reproduction instructions. The deadline fixture was on
the internal APFS Data volume; its source checkout was external APFS. Rebased
source identities are recorded without claiming that the measurements reran.

These are local, isolated comparisons. Full concurrent traffic, the A50 p99
budget, Linux, cold OS cache and production network behavior are NOT_CHECKED.
The evidence does not justify changing the store's durability or batch window.

Both probes used the historical store at main revision `6ac9e9b6`. Their
reproduction overlays pin that store after later rebases. CommitWake's separate
paired evidence is linked from the deadline report; its hint follows COMMIT,
outside the writer-callback measurement. Combined current-stack latency remains
NOT_CHECKED, and the recorded full RPC medians remain historical comparisons.
