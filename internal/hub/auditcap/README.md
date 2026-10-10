# internal/hub/auditcap

The rate cap on `machine.enroll_failed` audit rows
([02g](../../../docs/specs/02g-agent-security.md), [defaults](../../../docs/specs/defaults.md),
row 5 ruling fork 12). No I/O: the gateway asks it, then writes what it says.

```go
c := auditcap.New()
due, row := c.Note(now, "ENROLL_TOKEN_USED", "192.0.2.1") // due: an earlier minute's summary; row: nil when suppressed
s := c.Flush()                                              // at stop: the minute in progress
```

- One row per (reason, peer) per minute; `PerWindow` (10) rows per minute in
  all; the rest are counted in one `Summary` per minute (reason counts,
  distinct peers, suppressed count). A pair's next `Row` carries
  `Suppressed`, its refusals since its last row.
- The peer is the caller's address without its port, or every connection
  would be a new pair.
- Windows are whole minutes of the clock passed in. A clock that steps back
  stays in the current minute.
- Every map is bounded by `MaxTracked` (1,024). Past it a pair's count is not
  carried (the summary still counts it) and `DistinctPeers` stops there with
  `PeersCapped`.
- **In memory, by decision.** A restart empties it: in the minute of a
  restart at most 10 more rows and one more summary. A summary is handed out
  by the next `Note` after its minute or by `Flush` at stop, never by a
  timer, so on a quiet hub it waits for the next refusal or the stop. The
  counter `fleet_hub_enroll_total{result="refused"}` and the `enroll.refused`
  log line still record every refusal.

Tests: `CGO_ENABLED=0 go test -race ./internal/hub/auditcap/`: per-pair and
global caps with the carried count, a five-minute flood fully accounted for
(rows plus summaries equal the refusals), `Flush`, the bounds, a clock step
back, and concurrent callers.
