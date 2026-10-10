// Package housekeeping runs the hub's timed jobs (03f "housekeeping"):
// machine STALE and OFFLINE marking now, retention and expiry jobs from
// later plan rows. [New] takes named [Job]s, each with its own interval;
// [Runner.Run] checks them on every tick of a channel fleetd run feeds
// every [TickEvery], from one goroutine, until its context ends, and
// [Runner.Check] is one such pass. A job reads on the read pool and writes
// only through the store's single writer. A job that fails or panics is
// logged (hub.housekeeping_failed; a panic with its type and a bounded
// stack) and counted (fleet_hub_housekeeping_failures_total{job}); the
// others still run. An error after the hub began stopping is not a failure.
package housekeeping
