// Package secret is the local approval secret of ADR 0032: in
// local_trusted mode any process on the hub machine counts as the owner,
// so approving a request (DecideApproval) and changing settings (the
// SETTINGS and ADMIN calls) need this secret as well and fail without it,
// even on loopback.
//
// The secret is 256-bit random, created at fleetd init beside the state
// database (mode 0600), shown once at the terminal, and kept in the
// owner's dashboard session: never in the shell environment and never in a
// plain file. The hub keeps its copy in this file only to check presented
// values with a constant-time compare; it never logs the secret.
package secret
