# e2eseed agent rules

One-shot seeder, no server: `go run ./internal/e2eseed <fleetd.yaml>`.

- Argument is the fleetd.yaml path, not the state folder: `daemon.Init`
  needs the config to find (and refuse to replace) the store.
- Keep it dependency-light: init plus `seed.SeedRows`, no scheduler, no
  network, no telemetry (the binary passes a no-op).
- Seed rows stay fixed-order; the CA identity stays fresh-random per run.
  Do not "stabilize" the identity: reseeding a live folder is refused.
