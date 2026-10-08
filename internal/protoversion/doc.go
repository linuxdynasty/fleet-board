// Package protoversion is the agent wire's proto version range, shared by
// fleetd and fleet-agent so the two never disagree on it.
//
// The v1 wire is version 1 and 0 means unset
// (docs/specs/02l-versioning-errors-and-decisions.md). [Negotiate] checks a
// peer's EnrollRequest or Hello proto_min and proto_max against [Min] and
// [Max] and picks the highest common version.
package protoversion
