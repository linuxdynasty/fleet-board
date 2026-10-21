# internal/fleetcli

Read the root `AGENTS.md` first. This package is the local `fleet` CLI. Keep
argument parsing and output here; `cmd/fleet` only wires process streams and
signals. All RPCs use the public TCP API through the generated Connect client.
Treat server text and command arguments as untrusted. Read the approval secret
from the terminal with echo off and send it only in the
`X-Fleet-Approval-Secret` header on the `SETTINGS` and `ADMIN` writes (and
`GetStoreStatus`); never print it, never read a secret file, never log it.
The only token that reaches stdout is `machine add`'s own fresh enroll
command, shown once; no other command prints a token. Test the public `Run`
boundary and real daemon TCP path.
