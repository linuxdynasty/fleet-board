// Package probe decides which machine probes a usage account (03b, "Probe
// assignment"). It is pure: candidate machines plus the account in, the
// assignment decision out. No database, no clock beyond the now argument,
// no telemetry here; the scheduler and store slices own those.
package probe
