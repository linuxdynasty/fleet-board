# e2eseed

One-shot seeder for the web smoke test: `go run ./internal/e2eseed <fleetd.yaml>`.

Two steps, in order:

1. `daemon.Init` on the config path (a real CA identity, fresh random per
   run, which a booting hub needs). The state folder must not hold a
   `state.db` yet; init refuses to replace one.
2. `seed.SeedRows`, which writes the shared seed rows (machines only) in a
   fixed order.

Determinism holds for the seed rows only. The CA identity is
fresh-random every run by design, and `daemon.Init` decides what exists
already, so this binary never reseeds a live folder. Telemetry is a
no-op here: no run of the seeder phones anything home.
