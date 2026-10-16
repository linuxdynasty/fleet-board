# internal/hub/setupgithub

`fleetd setup github` without the terminal: the GitHub App manifest, the
one-time code exchange, the secret files and the `fleetd.yaml` update
(`docs/specs/02j-hub-runtime.md` "fleetd setup github", ADR 0022
decision 4).

- `Manifest` builds the App manifest the setup page posts: `public:
  false`, no webhook events, read-only repository permissions only
  (metadata, issues, pull requests, checks, statuses, contents).
- `NewState` / `CheckState` make and refuse the one-time callback
  secret; a wrong `state` is refused before anything else.
- `Exchange` swaps the code once
  (`POST /app-manifests/{code}/conversions`). A used or unknown code is
  a `CodeError`, and the caller writes files only after success, so a
  refused code writes nothing.
- `ListInstallations` lists `GET /app/installations`: none means
  install the App first, several means ask which.
- `WriteSecrets` stores `github-app.pem` and `github-client-secret`
  next to `fleetd.yaml`, each mode 0600, created exclusively, never
  logged. `UpdateConfig` adds the `github` keys; nothing secret goes
  into `state.db`.

Tests: `CGO_ENABLED=0 go test ./internal/hub/setupgithub/` runs the
flow against recorded (`httptest`) GitHub responses: a wrong `state`,
a used code, and the file modes.
