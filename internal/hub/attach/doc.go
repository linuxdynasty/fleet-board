// Package attach is the hub side of terminal attach (J1): single-use
// attach tickets and the bridge that pairs one AttachTerminal client
// with one agent Terminal stream by attach_id.
//
// Tickets live only in memory: a hub restart drops them, and every attach
// dies with the restart (HUB_CLOSED), so a ticket never needs the store.
// One Registry is shared by the FleetService handler (CreateAttachTicket,
// AttachTerminal) and the agent gateway (Terminal); both are served by
// the same fleetd process. Flow control follows 02f and ADR 0016: output
// is never dropped, and past 256 KiB unacknowledged the gateway stops
// reading the agent's Terminal stream, so HTTP/2 flow control blocks the
// agent's PTY read while Connect commands keep flowing.
package attach
