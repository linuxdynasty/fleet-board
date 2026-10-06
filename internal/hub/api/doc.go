// Package api is the hub's HTTP API (docs/specs/02b-hub-api.md, 02f).
//
// For now [Handler] is the whole surface of the local_trusted API listener
// (and of the optional metrics listener): the 02f Host and Origin checks
// before any handler, then GET /metrics, and 404 for every other path. A
// refused Host or Origin is a Connect permission_denied error (HTTP 403) whose
// ErrorDetail reason is HOST_NOT_ALLOWED or ORIGIN_NOT_ALLOWED, counted in
// fleet_hub_auth_failures_total{reason="host"|"origin"}. No CORS header is
// ever sent. FleetService, Watch and the admin socket server arrive here with
// their plan rows.
package api
