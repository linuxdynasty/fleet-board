// Command e2eseed seeds a fresh hub for the dashboard's Playwright smoke
// (`npm run e2e` in web/).
//
// It takes one argument, the fleetd.yaml path: it runs daemon.Init on it
// (a real, fresh-random CA identity, which a booting hub needs; init
// refuses to replace an existing store) and then writes the shared test
// fleet (package
// github.com/linuxdynasty/fleet-board/internal/seed) with seed.SeedRows.
// Only the seed rows are deterministic.
package main
