# internal/agent/conn/conntest

A real hub for agent tests: `internal/hub/gateway`'s agent listener on a real
`state.db` with its own fleet CA, served over TLS on `127.0.0.1`.

```go
h := conntest.StartHub(t, conntest.Options{})          // lives until the test ends
tok := h.Token(t, time.Now().Add(time.Hour))           // a stored token for machine "laptop"
bad := h.TokenForCA(t, otherCA, notAfter)              // stored here, but names another CA
h.Requests(); h.Machines(t); h.IssuedCerts(t); h.TokenUsed(t, tok)
h.Events(t, "enroll.")                                 // the hub's JSON log lines
conntest.StartHub(t, conntest.Options{Wrap: conntest.DropFirstReply()}) // lost reply
```

`Requests` counts every HTTP request the listener served, so a test can
prove a refused agent sent nothing. `DropFirstReply` lets the hub commit the
first Enroll, then resets the stream instead of answering.

Signals: none of its own; the hub's log lines are kept in a buffer for
`Events`. Test code only: nothing outside `_test.go` files imports it.
