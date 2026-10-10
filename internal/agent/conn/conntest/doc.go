// Package conntest runs the hub's real agent listener (internal/hub/gateway)
// on a real state.db with its own fleet CA, on 127.0.0.1, for agent tests.
// It mints tokens (also ones that name another CA), counts the requests the
// listener served, reads machines, issued certificates, certificate serials
// and renewals and token use from the store, takes a clock for the hub's
// tokens, certificates, hub_time and Hello rate limit, and can drop the
// first reply of a method (Enroll, RenewCert) after the hub commits it. It
// serves the hub's own Connect, restarts the listener on the same state.db
// and port (on a socket it holds open, so the port is never free), holds
// state.db's writer on demand, reads machines.last_epoch and
// machine_connections, seeds and reads machines.state, and runs the hub's
// machine-state housekeeping job once. Test code only.
package conntest
