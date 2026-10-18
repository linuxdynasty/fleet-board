# internal/agent/conn/conntest

A real hub for agent tests: `internal/hub/gateway`'s agent listener on a real
`state.db` with its own fleet CA, served over TLS on `127.0.0.1`.

```go
h := conntest.StartHub(t, conntest.Options{})          // lives until the test ends
tok := h.Token(t, time.Now().Add(time.Hour))           // a stored token for machine "laptop"
bad := h.TokenForCA(t, otherCA, notAfter)              // stored here, but names another CA
h.Requests(); h.Machines(t); h.IssuedCerts(t); h.TokenUsed(t, tok)
h.Events(t, "enroll.")                                 // the hub's JSON log lines
h.Serials(t, machineID); h.Renewals(t, machineID)      // cert_serials, cert_renewals
conntest.StartHub(t, conntest.Options{Wrap: conntest.DropFirstReply()}) // lost reply
conntest.StartHub(t, conntest.Options{Wrap: conntest.DropFirstReplyTo("RenewCert"), Now: clock})
h.Restart(t)                                           // Shutdown (Bye{HUB_RESTART}), reopen state.db, same port
h.LastEpoch(t, machineID); h.Connections(t, machineID) // machines.last_epoch, machine_connections
free := h.HoldWriter(t)                                // block state.db's writer until free()
h.SetState(t, machineID, "ONLINE"); h.State(t, machineID) // seed and read machines.state
h.Housekeep(t, now)                                    // the hub's machine-state job once (STALE, OFFLINE, cuts)
m := h.Machine(t, machineID)                           // statedb.ReadSnapshot: state, facts, policy, seq floor
ops := h.Enqueue(t, machineID, cmd)                    // outbox.Enqueue in a writer job, then Outbox().Notify
h.CurrentStore(); h.Telemetry(); h.NotifyCommand(ops[0]) // production API integration seam; no retention across Restart
h.Commands(t, machineID); h.Results(t, machineID)      // commands, command_results
h.LogText()                                            // every hub log line, to check order
```

`Requests` counts every HTTP request the listener served, so a test can
prove a refused agent sent nothing. `DropFirstReply` lets the hub commit the
first Enroll, then resets the stream instead of answering;
`DropFirstReplyTo` does the same for another method. `Options.Now` is the
hub's clock for tokens and certificates (a certificate issued in the past
is due for renewal; the TLS listener and the CA keep the real clock).
`Serials` reads `cert_serials` and `Renewals` reads `cert_renewals` for a
machine. `Connect` is the hub's own (plan row 5 PR 5a-ii): the agent's real
loop connects to it. `Restart` stops the listener as `fleetd run` does and
serves a new process on the same state.db and port (a new
hub_instance_id, a fresh Hello rate limit). The listening socket is held
open across the restart (`Held`): the port is never free, so another
process on a busy machine cannot take it. A test that needs the hub down
first rebinds with `BindRetry`, which retries a port in use and skips the
test, not fails it, if the port stays taken. `LastEpoch` reads
`machines.last_epoch` and `Connections` the machine's `machine_connections`
rows. `Machine` is `statedb.ReadSnapshot`: what the Hello and Snapshot
transactions stored on `machines` (plan row 5 PR 5c1). `Options.Now` is also the hub's clock for `HelloAck.hub_time` and the
Hello rate limit. `SetState` seeds `machines.state` (a real Snapshot marks `ONLINE` itself),
and `Housekeep` runs the machine-state job of `fleetd run`'s housekeeping
once at the given hub clock. `Enqueue` is the product path of a command:
`outbox.Enqueue` inside one writer job, then `Outbox().Notify` after the
commit; the hub sends it once the machine's Snapshot of its epoch
committed. `CurrentStore`, `Telemetry` and `NotifyCommand` let an
integration test build the real `api.Fleet` against this listener and use
the daemon's after-commit wake path. `Commands` reads `commands` (outbox and Operation state, the
last send's epoch and seq) and `Results` reads `command_results`. Tests that need what a real hub cannot do (a deaf hub, a
cut after HelloAck) keep the in-package fakes and route `Connect` to them
through `Wrap`.

Signals: none of its own; the hub's log lines are kept in a buffer for
`Events`. Test code only: nothing outside `_test.go` files imports it.
