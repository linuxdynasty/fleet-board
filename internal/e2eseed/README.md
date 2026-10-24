# internal/e2eseed

Writes the shared test fleet into a fresh state folder, for the dashboard's
Playwright smoke test (`npm run e2e` in `web/`).

```bash
go run ./internal/e2eseed /tmp/e2e-state
fleetd run --config /tmp/e2e-fleetd.yaml   # state_dir points at /tmp/e2e-state
```

One argument, the state folder; it prints the folder back. Seeding twice
fails: the shared fleet is never silently replaced. Deterministic like the
seed itself: no clocks, no randomness, fixed test vectors.
