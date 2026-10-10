// Package conntest runs the hub's real agent listener (internal/hub/gateway)
// on a real state.db with its own fleet CA, on 127.0.0.1, for agent tests.
// It mints tokens (also ones that name another CA), counts the requests the
// listener served, reads machines, issued certificates, certificate serials
// and renewals and token use from the store, takes a clock for the hub's
// tokens and certificates, and can drop the first reply of a method
// (Enroll, RenewCert) after the hub commits it. Test code only.
package conntest
