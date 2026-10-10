package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// revokedLater answers "not revoked" to the first question about each
// serial (the TLS handshake) and "revoked" after: a serial revoked while
// its connection was already open.
type revokedLater struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *revokedLater) Revoked(serial string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	was := r.seen[serial]
	r.seen[serial] = true
	return was
}

// machineMarks are the machine row's last_epoch and last_seen_time, which
// a refused Hello must not move.
type machineMarks struct {
	epoch int64
	seen  sql.NullInt64
}

func (h *hub) marks(t *testing.T, machineID string) machineMarks {
	t.Helper()
	var m machineMarks
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRowContext(context.Background(), `SELECT last_epoch, last_seen_time FROM machines WHERE machine_id = ?`,
			machineID).Scan(&m.epoch, &m.seen)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// helloRefusalCase is one row of TestConnectRefusals, built for the enrolled
// machine m.
type helloRefusalCase struct {
	name   string
	revoke bool // revoked-serials learns the serial after the handshake
	setup  string
	msg    func(m machine) *agentv1.ConnectRequest
	bye    agentv1.ByeReason
	code   connect.Code
	reason typev1.ReasonCode
	cause  string
}

func withHello(set func(*agentv1.Hello)) func(machine) *agentv1.ConnectRequest {
	return func(m machine) *agentv1.ConnectRequest {
		r := hello(m, 0)
		set(r.GetHello())
		return r
	}
}

var helloRefusalCases = []helloRefusalCase{
	{
		name: "first message not Hello", cause: "not_hello", code: connect.CodeInvalidArgument,
		reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: func(machine) *agentv1.ConnectRequest { return heartbeat() },
	},
	{
		name: "machine id not the certificate's", cause: "machine_id_mismatch", code: connect.CodeUnauthenticated,
		reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		msg:    withHello(func(h *agentv1.Hello) { h.GetMachineId().Value = "01OTHER" }),
	},
	{
		name: "machine id empty", cause: "machine_id_mismatch", code: connect.CodeUnauthenticated,
		reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, msg: withHello(func(h *agentv1.Hello) { h.MachineId = nil }),
	},
	{
		name: "boot id empty", cause: "field_invalid", code: connect.CodeInvalidArgument,
		reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: withHello(func(h *agentv1.Hello) { h.BootId = "" }),
	},
	{
		name: "install id with a space", cause: "field_invalid", code: connect.CodeInvalidArgument,
		reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, msg: withHello(func(h *agentv1.Hello) { h.InstallId = "install 1" }),
	},
	{
		name: "boot id over 64 bytes", cause: "field_invalid", code: connect.CodeInvalidArgument,
		reason: typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
		msg:    withHello(func(h *agentv1.Hello) { h.BootId = strings.Repeat("b", 65) }),
	},
	{
		name: "no common proto version", cause: "proto_unsupported", bye: agentv1.ByeReason_BYE_REASON_PROTO_UNSUPPORTED,
		msg: withHello(func(h *agentv1.Hello) { h.ProtoMin, h.ProtoMax = 2, 3 }),
	},
	{name: "serial revoked after the handshake", cause: "revoked", revoke: true, bye: agentv1.ByeReason_BYE_REASON_REVOKED},
	{
		name: "serial revoked in state.db", cause: "revoked", bye: agentv1.ByeReason_BYE_REASON_REVOKED,
		setup: `UPDATE cert_serials SET revoked_time = 1 WHERE serial = :serial`,
	},
	{
		name: "machine revoked", cause: "revoked", bye: agentv1.ByeReason_BYE_REASON_REVOKED,
		setup: `UPDATE machines SET state = 'REVOKED' WHERE machine_id = :machine`,
	},
	{
		name: "machine unknown", cause: "unknown_machine", bye: agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE,
		setup: `DELETE FROM enroll_responses WHERE machine_id = :machine; DELETE FROM machines WHERE machine_id = :machine`,
	},
	{
		name: "serial stored for another machine", cause: "serial_other_machine", code: connect.CodeUnauthenticated,
		reason: typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		setup:  `UPDATE cert_serials SET machine_id = 'someone-else' WHERE serial = :serial`,
	},
	{
		name: "stored epoch at the ceiling", cause: "epoch_out_of_range", code: connect.CodeFailedPrecondition,
		reason: typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH,
		setup:  `UPDATE machines SET last_epoch = 9223372036854775806 WHERE machine_id = :machine`,
	},
	{
		name: "Hello.last_epoch at the ceiling", cause: "epoch_out_of_range", code: connect.CodeFailedPrecondition,
		reason: typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH,
		msg:    func(m machine) *agentv1.ConnectRequest { return hello(m, statedb.MaxEpoch) },
	},
}

// Each refusal of 02g "Hello refusals" and "Connect-level failures": a
// Bye where 02g names one, else the Connect error; nothing is stored (no
// connection row, last_epoch and last_seen_time unchanged), and the
// refusal is logged with its cause and counted.
func TestConnectRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range helloRefusalCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var adjust []func(*gateway.ServerOptions)
			if tc.revoke {
				adjust = append(adjust, func(o *gateway.ServerOptions) { o.Revoked = &revokedLater{} })
			}
			h := startHub(t, adjust...)
			m := h.enrollMachine(t)
			for q := range strings.SplitSeq(tc.setup, ";") {
				if q = strings.TrimSpace(q); q != "" {
					h.exec(t, q, sql.Named("machine", m.id), sql.Named("serial", serialOf(m.leaf)))
				}
			}
			before := h.marks(t, m.id)
			msg := hello(m, 0)
			if tc.msg != nil {
				msg = tc.msg(m)
			}
			s := h.open(t, m)
			s.send(t, msg)
			if tc.bye != agentv1.ByeReason_BYE_REASON_UNSPECIFIED {
				if got := s.bye(t); got != tc.bye {
					t.Errorf("Bye %v, want %v", got, tc.bye)
				}
			} else if got := refusalOf(t, s.ended(t)); got.code != tc.code || got.reason != tc.reason || got.retryable {
				t.Errorf("refusal %+v, want %v %v not retryable", got, tc.code, tc.reason)
			}
			if rows := h.conns(t, m.id); len(rows) != 0 {
				t.Errorf("a refused Hello stored %+v", rows)
			}
			if after := h.marks(t, m.id); after != before {
				t.Errorf("machine marks moved from %+v to %+v", before, after)
			}
			lines := h.events(t, "connect.refused")
			if len(lines) != 1 || lines[0]["cause"] != tc.cause || lines[0]["machine_id"] != m.id {
				t.Errorf("connect.refused lines %v, want one with cause %s", lines, tc.cause)
			}
			if v := sample(scrape(t, h.fixture), "fleet_hub_connect_refused_total", `cause="`+tc.cause+`"`); v != 1 {
				t.Errorf("fleet_hub_connect_refused_total{cause=%s} = %v, want 1", tc.cause, v)
			}
		})
	}
}

// A store that cannot answer is the hub's failure: UNAVAILABLE, retryable,
// logged as connect.failed{step=store}.
func TestConnectAnswersUnavailableWhenTheStoreFails(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	got := refusalOf(t, s.ended(t))
	if got.code != connect.CodeUnavailable || !got.retryable {
		t.Errorf("refusal %+v, want unavailable and retryable", got)
	}
	if lines := h.events(t, "connect.failed"); len(lines) != 1 || lines[0]["step"] != "store" {
		t.Errorf("connect.failed lines %v, want one with step store", lines)
	}
}
