package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// helloAt is the time of the first Hello in these tests.
var helloAt = beforeTTL.Add(time.Minute)

// hello is a Hello from the machine enrolledStore made (serial ab0001).
func hello(lastEpoch uint64, boot, peer string, at time.Time) statedb.HelloIn {
	return statedb.HelloIn{
		MachineID: renewMachineID, PeerSerial: "ab0001", LastEpoch: lastEpoch, BootID: boot,
		InstallID: "install-1", Peer: peer, Reason: statedb.ConnectStart, Now: at,
	}
}

func mustHello(t *testing.T, s *store.Store, in statedb.HelloIn) statedb.HelloOut {
	t.Helper()
	out, err := statedb.Hello(context.Background(), s, in)
	if err != nil {
		t.Fatalf("Hello(last_epoch=%d): %v", in.LastEpoch, err)
	}
	return out
}

// connRow is one machine_connections row read from the file.
type connRow struct {
	boot, install, peer, reason string
	started                     int64
	ended                       sql.NullInt64
	end                         sql.NullString
	displacedBy, age            sql.NullInt64
}

func readConn(t *testing.T, db *sql.DB, machine string, epoch int64) connRow {
	t.Helper()
	var r connRow
	err := db.QueryRowContext(t.Context(), `SELECT boot_id, install_id, peer, connect_reason, started_time, ended_time,
		end_reason, displaced_by, last_heartbeat_age_ms FROM machine_connections WHERE machine_id = ? AND epoch = ?`,
		machine, epoch).
		Scan(&r.boot, &r.install, &r.peer, &r.reason, &r.started, &r.ended, &r.end, &r.displacedBy, &r.age)
	if err != nil {
		t.Fatalf("machine_connections %s/%d: %v", machine, epoch, err)
	}
	return r
}

// machineMarks reads machines.state, last_epoch and last_seen_time.
func machineMarks(t *testing.T, db *sql.DB, machine string) (string, uint64, sql.NullInt64) {
	t.Helper()
	var (
		state string
		epoch uint64
		seen  sql.NullInt64
	)
	if err := db.QueryRowContext(t.Context(), `SELECT state, last_epoch, last_seen_time FROM machines WHERE machine_id = ?`,
		machine).Scan(&state, &epoch, &seen); err != nil {
		t.Fatal(err)
	}
	return state, epoch, seen
}

func TestTheFirstHelloIsEpochOneAndLeavesTheMachineStateAlone(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if out.Epoch != 1 || out.Displaced != nil || out.HubLastEventSeq != 0 || out.SettingsGeneration != 0 {
		t.Errorf("Hello = %+v, want epoch 1, nothing displaced, seq and generation 0", out)
	}
	if want := sampleIdentity().SettingsEpoch; out.SettingsEpoch != want {
		t.Errorf("SettingsEpoch = %q, want %q", out.SettingsEpoch, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	r := readConn(t, db, renewMachineID, 1)
	if r.boot != "boot-a" || r.install != "install-1" || r.peer != "192.0.2.7:51000" || r.reason != "start" ||
		r.started != helloAt.UnixMilli() || r.ended.Valid || r.end.Valid || r.displacedBy.Valid || r.age.Valid {
		t.Errorf("connection row = %+v", r)
	}
	// Fork 1 of the row 5 ruling: the Snapshot transaction marks ONLINE,
	// the Hello transaction leaves machines.state as it was.
	state, epoch, seen := machineMarks(t, db, renewMachineID)
	if state != statedb.MachineEnrolled || epoch != 1 || seen.Int64 != helloAt.UnixMilli() {
		t.Errorf("machine = %s, last_epoch %d, last_seen %v; want ENROLLED, 1, %d", state, epoch, seen, helloAt.UnixMilli())
	}
}

// 02i "Connection epoch": max(stored, Hello.last_epoch) + 1, so an agent
// ahead of a restored hub still gets a higher epoch.
func TestTheEpochIsTheLargerOfStoredAndLastEpochPlusOne(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		before    int // Hellos (last_epoch 0) before the one under test: stored = before
		lastEpoch uint64
		want      uint64
	}{
		{"agent behind the hub", 3, 1, 4},
		{"agent equal to the hub", 3, 3, 4},
		{"agent ahead of a restored hub", 1, 7, 8},
		{"fresh hub, agent with history", 0, 41, 42},
		{"last epoch at the edge", 0, statedb.MaxEpoch - 1, statedb.MaxEpoch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			for i := range tc.before {
				mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt.Add(time.Duration(i)*time.Second)))
			}
			out := mustHello(t, s, hello(tc.lastEpoch, "boot-a", "192.0.2.7:51000", helloAt.Add(time.Minute)))
			if out.Epoch != tc.want {
				t.Fatalf("epoch %d, want %d", out.Epoch, tc.want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, epoch, _ := machineMarks(t, rawDB(t, dir), renewMachineID); epoch != tc.want {
				t.Errorf("stored last_epoch %d, want %d", epoch, tc.want)
			}
		})
	}
}

// The epoch is stored in the commit: closing and reopening the store (a
// hub restart, or a crash after the commit) keeps counting from it.
func TestTheEpochSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51001", helloAt.Add(time.Second)))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	out := mustHello(t, s, hello(0, "boot-b", "192.0.2.7:51002", helloAt.Add(time.Minute)))
	if out.Epoch != 3 {
		t.Fatalf("epoch after reopen %d, want 3", out.Epoch)
	}
	if out.Displaced == nil || out.Displaced.Epoch != 2 {
		t.Errorf("Displaced = %+v, want the live row of epoch 2 from before the restart", out.Displaced)
	}
}

// 03n "Duplicate identity": the newer Hello wins and the old row is closed
// as displaced, with the facts 5a-ii logs and 5e's triage rule reads.
func TestANewerHelloDisplacesTheLiveConnection(t *testing.T) {
	t.Parallel()
	second := helloAt.Add(30 * time.Second)
	cases := []struct {
		name    string
		live    statedb.LiveStream
		wantAge time.Duration
		known   bool
	}{
		{"heartbeat known", statedb.LiveStream{Epoch: 1, LastHeartbeat: second.Add(-12 * time.Second)}, 12 * time.Second, true},
		{"no live stream in the registry", statedb.LiveStream{}, 0, false},
		{"registry names another epoch", statedb.LiveStream{Epoch: 7, LastHeartbeat: second}, 0, false},
		{"no heartbeat yet", statedb.LiveStream{Epoch: 1}, 0, false},
		{"heartbeat stamped after now", statedb.LiveStream{Epoch: 1, LastHeartbeat: second.Add(time.Second)}, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			in := hello(1, "boot-b", "198.51.100.4:40000", second)
			in.Live = tc.live
			out := mustHello(t, s, in)
			want := statedb.Displaced{
				Epoch: 1, BootID: "boot-a", InstallID: "install-1", Peer: "192.0.2.7:51000", Started: helloAt,
				HeartbeatAge: tc.wantAge, HeartbeatKnown: tc.known,
			}
			if out.Epoch != 2 || out.Displaced == nil || *out.Displaced != want {
				t.Fatalf("Hello = epoch %d, displaced %+v; want epoch 2, displaced %+v", out.Epoch, out.Displaced, want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			db := rawDB(t, dir)
			old := readConn(t, db, renewMachineID, 1)
			if old.ended.Int64 != second.UnixMilli() || old.end.String != "displaced" || old.displacedBy.Int64 != 2 ||
				old.age.Valid != tc.known || old.age.Int64 != tc.wantAge.Milliseconds() {
				t.Errorf("displaced row = %+v", old)
			}
			if cur := readConn(t, db, renewMachineID, 2); cur.ended.Valid || cur.boot != "boot-b" {
				t.Errorf("new row = %+v, want live boot-b", cur)
			}
		})
	}
}

// 02g: Connect needs a live machine row, a serial not revoked and not
// stored for another machine, and room for the next epoch under the agent's
// ceiling; the machine is checked before the epoch, and a refused Hello
// writes nothing.
func TestHelloRefusesWithoutALiveMachineAnUnrevokedSerialOrRoomForAnEpoch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup string
		edit  func(*statedb.HelloIn)
		want  error
	}{
		{"unknown machine", "", func(in *statedb.HelloIn) { in.MachineID = "01jmachine0000000000000099" }, statedb.ErrMachineUnknown},
		{"deleted machine", `DELETE FROM enroll_responses; DELETE FROM machines`, nil, statedb.ErrMachineUnknown},
		{"revoked machine", `UPDATE machines SET state = 'REVOKED'`, nil, statedb.ErrMachineRevoked},
		{"revoked serial", `UPDATE cert_serials SET revoked_time = 1`, nil, statedb.ErrSerialRevoked},
		{
			"serial stored for another machine",
			`INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES ('ab0099', 'other', 1, 1)`,
			func(in *statedb.HelloIn) { in.PeerSerial = "ab0099" }, statedb.ErrPeerSerialUnknown,
		},
		{"stored epoch at the ceiling", `UPDATE machines SET last_epoch = 9223372036854775806`, nil, statedb.ErrEpochOutOfRange},
		{
			"revoked machine with last_epoch past the ceiling", `UPDATE machines SET state = 'REVOKED'`,
			func(in *statedb.HelloIn) { in.LastEpoch = math.MaxUint64 }, statedb.ErrMachineRevoked,
		},
		{
			"unknown machine with last_epoch at the ceiling", "",
			func(in *statedb.HelloIn) { in.MachineID, in.LastEpoch = "01jmachine0000000000000099", statedb.MaxEpoch }, statedb.ErrMachineUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
			if tc.setup != "" {
				if _, err := rawDB(t, dir).ExecContext(t.Context(), tc.setup); err != nil {
					t.Fatal(err)
				}
			}
			in := hello(5, "boot-b", "192.0.2.7:51001", helloAt.Add(time.Minute))
			if tc.edit != nil {
				tc.edit(&in)
			}
			if out, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, tc.want) {
				t.Fatalf("Hello = %+v, %v; want %v", out, err, tc.want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if n := rawCount(t, statedb.Path(dir), `SELECT count(*) FROM machine_connections`); n != 1 {
				t.Errorf("%d connection rows, want only the first Hello's", n)
			}
			if r := readConn(t, rawDB(t, dir), renewMachineID, 1); r.ended.Valid {
				t.Errorf("a refused Hello closed the live row: %+v", r)
			}
		})
	}
}

func TestHelloRefusesMalformedInputAndEpochsOutOfRange(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 65)
	cases := []struct {
		name string
		edit func(*statedb.HelloIn)
		want error
	}{
		{"last epoch at the ceiling", func(in *statedb.HelloIn) { in.LastEpoch = statedb.MaxEpoch }, statedb.ErrEpochOutOfRange},
		{"last epoch past int64", func(in *statedb.HelloIn) { in.LastEpoch = math.MaxUint64 }, statedb.ErrEpochOutOfRange},
		{"empty machine id", func(in *statedb.HelloIn) { in.MachineID = "" }, statedb.ErrHelloInvalid},
		{"empty serial", func(in *statedb.HelloIn) { in.PeerSerial = "" }, statedb.ErrHelloInvalid},
		{"empty boot id", func(in *statedb.HelloIn) { in.BootID = "" }, statedb.ErrHelloInvalid},
		{"boot id of 65 bytes", func(in *statedb.HelloIn) { in.BootID = long }, statedb.ErrHelloInvalid},
		{"boot id with a space", func(in *statedb.HelloIn) { in.BootID = "boot a" }, statedb.ErrHelloInvalid},
		{"boot id with a control character", func(in *statedb.HelloIn) { in.BootID = "boot\x1b[2J" }, statedb.ErrHelloInvalid},
		{"install id not ASCII", func(in *statedb.HelloIn) { in.InstallID = "inställa" }, statedb.ErrHelloInvalid},
		{"empty install id", func(in *statedb.HelloIn) { in.InstallID = "" }, statedb.ErrHelloInvalid},
		{"empty peer", func(in *statedb.HelloIn) { in.Peer = "" }, statedb.ErrHelloInvalid},
		{"peer of 129 bytes", func(in *statedb.HelloIn) { in.Peer = strings.Repeat("1", 129) }, statedb.ErrHelloInvalid},
		{"unknown reason", func(in *statedb.HelloIn) { in.Reason = "boot" }, statedb.ErrHelloInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			in := hello(0, "boot-a", "192.0.2.7:51000", helloAt)
			tc.edit(&in)
			if out, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, tc.want) {
				t.Fatalf("Hello = %+v, %v; want %v", out, err, tc.want)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if n := rawCount(t, statedb.Path(dir), `SELECT count(*) FROM machine_connections`); n != 0 {
				t.Errorf("%d connection rows after a refused Hello", n)
			}
		})
	}
}

// The agent's own ids pass: boot_id is 16 hex, install_id 32 hex, and an
// IPv6 peer with a zone fits.
func TestHelloAcceptsTheAgentsRealIDs(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	in := hello(0, "0123456789abcdef", "[fe80::1ff:fe23:4567:890a%en0]:51000", helloAt)
	in.InstallID = "0123456789abcdef0123456789abcdef"
	for _, r := range []statedb.ConnectReason{
		statedb.ConnectUnspecified, statedb.ConnectStart, statedb.ConnectReconnect, statedb.ConnectWake, statedb.ConnectNetworkChange,
	} {
		in.Reason = r
		mustHello(t, s, in)
	}
}

func TestEndConnectionClosesTheLiveRowOnce(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	ctx := context.Background()
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	end := helloAt.Add(time.Minute)
	if live, err := statedb.EndConnection(ctx, s, renewMachineID, 1, statedb.EndStreamClosed, end); err != nil || !live {
		t.Fatalf("EndConnection = %v, %v; want the live row closed", live, err)
	}
	// defaults.md: last_seen is written on disconnect.
	if _, _, seen := machineMarks(t, rawDB(t, dir), renewMachineID); seen.Int64 != end.UnixMilli() {
		t.Errorf("last_seen_time after the close %v, want %d", seen, end.UnixMilli())
	}
	if live, err := statedb.EndConnection(ctx, s, renewMachineID, 1, statedb.EndAgentBye, end.Add(time.Minute)); err != nil || live {
		t.Errorf("second EndConnection = %v, %v; want a no-op", live, err)
	}
	if live, err := statedb.EndConnection(ctx, s, renewMachineID, 9, statedb.EndStreamClosed, end); err != nil || live {
		t.Errorf("EndConnection of an epoch never seen = %v, %v; want a no-op", live, err)
	}
	if _, err := statedb.EndConnection(ctx, s, renewMachineID, 1, statedb.EndDisplaced, end); !errors.Is(err, statedb.ErrEndReasonInvalid) {
		t.Errorf("EndConnection(displaced) = %v, want ErrEndReasonInvalid", err)
	}
	// The next Hello finds no live row, so it displaces nothing.
	if out := mustHello(t, s, hello(1, "boot-a", "192.0.2.7:51001", end.Add(2*time.Minute))); out.Epoch != 2 || out.Displaced != nil {
		t.Errorf("Hello after the close = %+v, want epoch 2 displacing nothing", out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if r := readConn(t, db, renewMachineID, 1); r.ended.Int64 != end.UnixMilli() || r.end.String != "stream_closed" || r.displacedBy.Valid {
		t.Errorf("closed row = %+v, want ended at the first close with stream_closed", r)
	}
	if _, _, seen := machineMarks(t, db, renewMachineID); seen.Int64 != end.Add(2*time.Minute).UnixMilli() {
		t.Errorf("last_seen_time %v, want the latest Hello", seen)
	}
}

// 03f race table: the displaced stream's close is not the live row's close,
// so it reports false and the row keeps its displacement.
func TestEndConnectionOfADisplacedStreamIsANoOp(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	mustHello(t, s, hello(1, "boot-b", "192.0.2.7:51001", helloAt.Add(time.Second)))
	live, err := statedb.EndConnection(context.Background(), s, renewMachineID, 1, statedb.EndStreamClosed, helloAt.Add(time.Minute))
	if err != nil || live {
		t.Fatalf("EndConnection(displaced epoch 1) = %v, %v; want false, nil", live, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if r := readConn(t, db, renewMachineID, 1); r.end.String != "displaced" || r.ended.Int64 != helloAt.Add(time.Second).UnixMilli() {
		t.Errorf("displaced row = %+v", r)
	}
	if r := readConn(t, db, renewMachineID, 2); r.ended.Valid {
		t.Errorf("the live row was closed by the old stream: %+v", r)
	}
	if _, _, seen := machineMarks(t, db, renewMachineID); seen.Int64 != helloAt.Add(time.Second).UnixMilli() {
		t.Errorf("last_seen_time %v moved on a displaced stream's close", seen)
	}
}

// Every reason EndConnection may write is one the table's CHECK accepts, so
// the Go list and the SQL list cannot drift apart.
func TestEveryEndReasonIsStored(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	reasons := []statedb.EndReason{
		statedb.EndStreamClosed, statedb.EndAgentBye, statedb.EndHubRestart,
		statedb.EndIngestStalled, statedb.EndOffline, statedb.EndRevoked,
	}
	for i, r := range reasons {
		at := helloAt.Add(time.Duration(i) * time.Minute)
		out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", at))
		if live, err := statedb.EndConnection(context.Background(), s, renewMachineID, out.Epoch, r, at.Add(time.Second)); err != nil || !live {
			t.Errorf("EndConnection(%s) = %v, %v", r, live, err)
		}
	}
}
