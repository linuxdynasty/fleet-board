package gateway

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/hub/auditcap"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// auditWait bounds how long a refusal waits for its audit row, so a hub
// whose store is stuck still answers the agent.
const auditWait = 5 * time.Second

// auditRefusal records a refused Enroll as machine.enroll_failed, within
// the caps of auditcap: the summary of an earlier minute that is due, then
// the refusal's own row when it gets one. The rows are one writer job of
// their own (a refusal changes nothing else) and the refusal waits for it.
// A write failure is counted and logged, never returned: the agent gets
// its refusal either way.
func (d *enrollDeps) auditRefusal(ctx context.Context, peer string, err *connect.Error) {
	now := d.now().UTC().Truncate(time.Millisecond)
	due, row := d.audit.Note(now, reasonOf(err), peerHost(peer))
	var entries []statedb.AuditEntry
	if due != nil {
		entries = append(entries, summaryEntry(*due, now))
	}
	if row != nil {
		detail, _ := json.Marshal(struct { //nolint:errchkjson // strings and an int cannot fail
			Peer       string `json:"peer"`
			Code       string `json:"code"`
			Suppressed int    `json:"suppressed,omitempty"`
		}{row.Peer, err.Code().String(), row.Suppressed})
		entries = append(entries, statedb.AuditEntry{
			Action: statedb.ActionEnrollFailed, Target: "machines", Outcome: "FAILED", Reason: row.Reason,
			Detail: string(detail), Now: now,
		})
	} else {
		d.auditRows.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "suppressed")))
	}
	d.writeAudit(ctx, due != nil, row != nil, entries)
}

// flushAudit writes the summary of the minute in progress ([Server.FlushAudit]).
func (d *enrollDeps) flushAudit(ctx context.Context) {
	if s := d.audit.Flush(); s != nil {
		now := d.now().UTC().Truncate(time.Millisecond)
		d.writeAudit(ctx, true, false, []statedb.AuditEntry{summaryEntry(*s, now)})
	}
}

func (d *enrollDeps) writeAudit(ctx context.Context, summary, row bool, entries []statedb.AuditEntry) {
	if len(entries) == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWait)
	defer cancel()
	if err := statedb.WriteAudit(wctx, d.store, entries...); err != nil {
		d.auditRows.Add(ctx, int64(len(entries)), metric.WithAttributes(attribute.String("result", "write_failed")))
		d.log.ErrorContext(ctx, "enroll.audit_failed", "rows", len(entries), "err_kind", errKind(err))
		return
	}
	if summary {
		d.auditRows.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "summary")))
	}
	if row {
		d.auditRows.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "written")))
	}
}

// summaryEntry is the machine.enroll_failed row for a minute's suppressed
// refusals; occurred_time is the minute's start.
func summaryEntry(s auditcap.Summary, now time.Time) statedb.AuditEntry {
	detail, _ := json.Marshal(struct { //nolint:errchkjson // strings, ints, a bool and a map of ints cannot fail
		WindowStart   string         `json:"window_start"`
		Suppressed    int            `json:"suppressed"`
		DistinctPeers int            `json:"distinct_peers"`
		PeersCapped   bool           `json:"peers_capped,omitempty"`
		Reasons       map[string]int `json:"reasons"`
	}{s.Window.UTC().Format(time.RFC3339), s.Suppressed, s.DistinctPeers, s.PeersCapped, s.Reasons})
	return statedb.AuditEntry{
		Action: statedb.ActionEnrollFailed, Target: "machines", Outcome: "FAILED",
		Detail: string(detail), Occurred: s.Window, Now: now,
	}
}

// peerHost is the peer address without its port: each connection has its
// own port, so the port would make every refusal a new pair.
func peerHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
