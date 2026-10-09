// Package conntest runs the hub's real agent listener (internal/hub/gateway)
// on a real state.db with its own fleet CA, on 127.0.0.1, for agent tests.
// It mints tokens (also ones that name another CA), counts the requests the
// listener served, reads machines, issued certificates and token use from
// the store, and can drop the first Enroll reply after the hub commits it.
// Test code only.
package conntest
