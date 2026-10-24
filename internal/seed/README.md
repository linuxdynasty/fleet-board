# internal/seed

The shared test fleet: one state folder every contract test, golden CLI
output and Playwright smoke starts from
(docs/specs/02k-v1-surface-and-cli.md "The test seed").

- `Seed(ctx, dir)` builds the folder: the `seed-fleet` identity, a
  connected laptop (`MachineLaptop`, ONLINE with a snapshot) and an
  enrolled server (`MachineServer`, ENROLLED, never connected).
- Every stamp comes from one fixed clock, so two seeds hold the same rows.
- `Version` follows the agent wire (`internal/protoversion`): a wire change
  breaks the build until the seed is rebuilt.
- Seeding twice fails; the shared fleet is never silently replaced.
- `SeedRows` writes the same rows into a `fleetd init` store, whose real CA
  identity a running hub needs; the fake seed identity is for contract
  tests only.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/seed/`.
