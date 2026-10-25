# cmd/fleetd

`fleetd` is the hub: the one process that holds state.db and serves the dashboard and CLI.

```bash
fleetd init [--config PATH]   # write fleetd.yaml if missing, the state folder and state.db
fleetd run  [--config PATH]   # start the hub; SIGTERM or Ctrl-C stops it cleanly
fleetd enroll-token --machine NAME [--expect-host H] [--ttl 15m] [--label L ...] [--start-drained]
                              # ask the running hub for a token that enrolls one machine
fleetd backup [--encrypt]     # copy state.db into <state_dir>/backups/, with or without fleetd run
fleetd restore BACKUP [--address A]
                              # replace state.db with a backup; refused while fleetd run is up
fleetd setup github [--app-name N] [--org ORG] [--installation-id ID]
                              # connect GitHub through the App manifest flow (writes the github keys)
fleetd upgrade [--version TAG] [--repo OWNER/NAME]
                              # fetch the release, check its checksum, swap the binary, restart
fleetd --version              # "fleetd dev", or the release version set with -ldflags -X
```

`setup github` binds a one-shot page on 127.0.0.1 and prints its URL:
confirm the App on GitHub and the redirect comes back with a one-time
code, which is exchanged once (`POST /app-manifests/{code}/conversions`).
`--org ORG` creates the App under that organization (a private App can
only be installed on its owning account); without it the App is created
under the owner's personal account. The private key lands in
`github-app.pem` and the client secret in `github-client-secret`, both
next to `fleetd.yaml`, mode 0600, never logged; the `github` keys
(`app_id`, `private_key_file`, `client_id`, `client_secret_file`,
`installation_id`) are added to `fleetd.yaml` and nothing secret goes
into `state.db`. With exactly one installation it writes
`installation_id`; with several it lists them for a re-run with
`--installation-id`; with none it says to install the App first. A
re-run finds the saved App and only lists, selects and saves the
installation: no browser, no new exchange; a `--installation-id` the
list does not contain is refused. The callback answers the browser with
the full confirmation page before the server shuts down. Errors name
the step only, never a secret, the one-time code or a path. It prints
"restart `fleetd` to apply": the file is read at start. The flow is in
`internal/hub/setupgithub`, tested against recorded GitHub responses
(a wrong `state`, a used code, the file modes).

`backup` prints only the path of the copy on stdout
(`<state_dir>/backups/manual-<UTC stamp>.db`, mode 0600). It takes no lock, so
it works beside a running hub and with the hub stopped. A backup holds the CA
key and the account salt: keep it like a private key. `--encrypt` writes
`manual-<UTC stamp>.db.age` with age, asking for a passphrase twice at the
terminal; it refuses when stdin is not a terminal, and there is no flag or
environment variable for the passphrase. Ctrl-C at a prompt takes effect
only after Enter, because the read is not interrupted; the backup then stops
without asking again. If the copy was published but a temporary file could not
be removed, the path is still printed and the exit code is 1. `fleetd run`
makes the nightly copies itself.

`restore` is offline: it takes `fleetd.lock`, so with `fleetd run` up it
exits 1 with `lock_held` and "stop fleetd run before restoring". It refuses
an age-encrypted backup (decrypt it first), a corrupt one, one with a schema
version this fleetd does not know, and one from another fleet than the
current `state.db`, and then changes nothing. Otherwise it keeps the fleet
id, CA and salt, mints a new `settings_epoch`, opens the 24 h reconcile
window, and keeps the replaced store as `state.db.pre-restore-<UTC stamp>`
(mode 0600). That copy is the only place left for the audit rows written
since the backup, so keep it, or export its `audit_log`, before deleting
it. `--address` (host or
host:port; the port defaults to `listen.agent`'s, else 7443) is added to the
addresses agents get and to the server certificate, signed by the same CA.
With another process holding `state.db` open (a `fleetd backup`) it says to
wait and restore again. If a step after the rename fails (the folder sync,
the cleanup), it still prints that the new store is in place and where the
old one is kept, then exits 1. The steps are in
[internal/hub/restore](../../internal/hub/restore/README.md).

`enroll-token` prints only the token on stdout, on one line, so
`TOKEN=$(fleetd enroll-token --machine laptop)` works; stderr shows the
expiry, the CA fingerprint and the `fleet-agent enroll --token …` line to run
on the machine. `--ttl` is more than 0 and at most 24h (default 15m). With no
hub running it prints "fleetd is not running", exits 1 and writes nothing.

The config file is `--config`, else `$FLEETD_CONFIG`, else
`~/.config/fleetd/fleetd.yaml`. Exit codes: 0 done (or stopped by a signal), 1
the command failed (the reason is on stderr and in the `hub.start_failed` log
line), 2 bad usage.

Today `run` serves the agent listener (TLS 1.3; `AgentService` serves
`Enroll` and `RenewCert`, every other method Unimplemented), `/metrics` on the loopback API listener, and the admin socket
`<state_dir>/admin.sock` (mode 0600, only fleetd's own OS user) that
`enroll-token` talks to. A second `run` against the same state folder is
refused while the first holds `<state_dir>/fleetd.lock`.

Build and try it:

```bash
make build
go run ./cmd/fleetd --version
```

`main.go`, `enroll.go`, `backup.go`, `restore.go` and `setup_github.go` only parse flags and call `internal/`
(`daemon`, or `setupgithub` for the GitHub flow); what
fleetd does is specified in `docs/specs/02j-hub-runtime.md`, and the build order
is in `docs/plan/v1-implementation-plan.md` (plan rows 2 to 5). `main_test.go`
runs the built binary: init, run, a refused second run, SIGTERM, and a start
after a SIGKILL. `enroll_test.go` is row 2b's acceptance: init, run, and
`enroll-token` printing a token whose CA hash equals the stored CA; refused
lifetimes; "fleetd is not running" with no hub and after a SIGKILL; and
`admin.sock` removed by SIGTERM. `backup_test.go` runs `fleetd backup` beside
a running hub (the copy is 0600 and holds the hub's CA; a second `run` still
gets `lock_held`) and checks `--encrypt` refuses a piped passphrase and has
no passphrase flag. `restore_test.go` is row 5's restore acceptance: refused
beside a running hub (which keeps serving); a restore with `--address`, then
`run` serving a certificate from the pinned CA that covers the address; a
second restore; and bad backups refused with `state.db` and its WAL byte for
byte unchanged. `setup_github_test.go` is row 13a's setup acceptance:
`setup github` prints a loopback URL, the one redirect is exchanged against
recorded GitHub responses, and `github-app.pem` lands mode 0600 with the
`github` keys in `fleetd.yaml`.
