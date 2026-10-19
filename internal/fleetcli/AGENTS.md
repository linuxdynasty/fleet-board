# internal/fleetcli

Read the root `AGENTS.md` first. This package is the local `fleet` CLI. Keep
argument parsing and output here; `cmd/fleet` only wires process streams and
signals. All RPCs use the public TCP API through the generated Connect client.
Treat server text and command arguments as untrusted. Never print a token or
approval secret, read a secret file, or send the approval secret except on its
one protected RPC. Test the public `Run` boundary and real daemon TCP path.
