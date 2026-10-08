// Package ulid mints the hub's ids: lowercase Crockford ULIDs of 26
// characters, the format api/fleet/type/v1/ids.proto gives every
// hub-assigned id (MachineId first, from plan row 3).
//
// [New] takes the time and the random source from the caller, so tests set
// both: 48 bits of Unix milliseconds, then 80 random bits. Ids from later
// milliseconds sort after earlier ones; there is no monotonic counter within
// one millisecond, because nothing orders by id inside a millisecond.
package ulid
