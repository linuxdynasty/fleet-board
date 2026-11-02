// Package native_test proves capabilities N1 (native session control: the
// five typed commands through the journal to one qualified adapter) and N2
// (structured native events, unknown-event classification and scrubbing).
package native_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/native"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	_ "modernc.org/sqlite"
)

// countEffects reads the journal file itself: how many effects sit in a
// state ("" counts all). Independent of the host under test.
func countEffects(t *testing.T, f *fixture, state string) int {
	t.Helper()
	db, err := sql.Open("sqlite", ring.Path(f.dir)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	q := `SELECT count(*) FROM native_effects`
	if state != "" {
		q += ` WHERE state = '` + state + `'`
	}
	var n int
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// effectState reads one effect's journal state through the host's journal.
func effectState(t *testing.T, f *fixture, id string) string {
	t.Helper()
	e, err := f.db.ReadNativeEffect(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return string(e.State)
}

// fixture opens a file-backed agent.db (crash tests close and reopen it)
// and a host with a scripted adapter and a recording emitter.
type fixture struct {
	dir string
	db  *ring.DB
	em  *fakeEmitter
	fa  *fakeAdapter
	h   *native.Host
}

func newFixture(t *testing.T, adapters ...native.Adapter) *fixture {
	t.Helper()
	if len(adapters) == 0 {
		adapters = []native.Adapter{&fakeAdapter{backend: typev1.NativeBackend_NATIVE_BACKEND_CLAUDE_CODE}}
	}
	dir := t.TempDir()
	db, err := ring.Open(context.Background(), dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return buildHost(t, dir, db, adapters...)
}

// newBareFixture builds a host with no adapters at all: nothing qualified,
// nothing advertised.
func newBareFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := ring.Open(context.Background(), dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	em := &fakeEmitter{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "native-test", LogOutput: testLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	h, err := native.New(native.Options{
		DB: db, MachineID: "machine-1", Home: "/home/op", Emitter: em,
		Telemetry: tel, Now: time.Now, OrderWait: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{dir: dir, db: db, em: em, fa: &fakeAdapter{}, h: h}
}

func buildHost(t *testing.T, dir string, db *ring.DB, adapters ...native.Adapter) *fixture {
	t.Helper()
	em := &fakeEmitter{}
	var fa *fakeAdapter
	if len(adapters) > 0 {
		fa, _ = adapters[0].(*fakeAdapter)
	}
	if fa == nil {
		fa = &fakeAdapter{}
	}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "native-test", LogOutput: testLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	h, err := native.New(native.Options{
		DB: db, MachineID: "machine-1", Home: "/home/op", Emitter: em,
		Adapters: adapters, Telemetry: tel, Now: time.Now, OrderWait: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{dir: dir, db: db, em: em, fa: fa, h: h}
}

func testLog(t *testing.T) *testWriter {
	t.Helper()
	return &testWriter{}
}

// reopen closes the db and reopens the same folder: the crash failpoint.
// The rebuilt host keeps the same fake adapter (call counts accumulate).
func (f *fixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := ring.Open(context.Background(), f.dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	var adapters []native.Adapter
	if f.fa.Backend() != typev1.NativeBackend_NATIVE_BACKEND_UNSPECIFIED {
		adapters = []native.Adapter{f.fa}
	}
	nh := buildHost(t, f.dir, db, adapters...)
	f.h = nh.h
	f.em = nh.em
}

// loggedCommand saves cmd in the command log exactly like the executor
// does (message-name kind, deterministic body) and returns its digest.
func loggedCommand(t *testing.T, db *ring.DB, cmd *agentv1.Command) string {
	t.Helper()
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	kind := string(cmd.ProtoReflect().WhichOneof(
		cmd.ProtoReflect().Descriptor().Oneofs().ByName("kind")).Message().Name())
	if _, dup, err := db.LogCommand(context.Background(), ring.Command{
		ID: cmd.GetCommandId().GetValue(), Kind: kind,
		Epoch: 1, Seq: 1, Received: time.Now(), Body: body,
	}); err != nil || dup {
		t.Fatalf("log %s: duplicate=%v err=%v", cmd.GetCommandId().GetValue(), dup, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func envelope(id, requires string) *agentv1.Command {
	now := time.Now()
	return &agentv1.Command{
		CommandId:    &typev1.CommandId{Value: id},
		Epoch:        1,
		Seq:          1,
		IssuedTime:   timestamppb.New(now),
		NotAfterTime: timestamppb.New(now.Add(time.Hour)),
		RequestedBy: &typev1.Principal{
			Kind: typev1.PrincipalKind_PRINCIPAL_KIND_SCHEDULER,
			Role: typev1.Role_ROLE_SCHEDULER,
		},
		Requires: &typev1.Capability{Name: requires, Version: 1},
	}
}

func startCmd(id string) *agentv1.Command {
	cmd := envelope(id, "native.claude_code")
	cmd.Kind = &agentv1.Command_StartNativeSession{StartNativeSession: &agentv1.StartNativeSession{
		Backend: typev1.NativeBackend_NATIVE_BACKEND_CLAUDE_CODE,
		Session: &typev1.SessionId{Value: "session-1"},
		Account: &typev1.AccountId{Value: "account-1"},
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "org/repo"},
		Model:   "opus", Effort: "high", QualificationRef: "native.claude_code/1_cli-9_plan",
	}}
	return cmd
}

func sendCmd(id, session string, gen uint64, input string) *agentv1.Command {
	cmd := envelope(id, "native.claude_code")
	cmd.Kind = &agentv1.Command_SendNativeTurn{SendNativeTurn: &agentv1.SendNativeTurn{
		Session: &typev1.SessionId{Value: session}, OwnershipGeneration: gen, Input: input,
	}}
	return cmd
}

func resumeCmd(id, session string, gen uint64) *agentv1.Command {
	cmd := envelope(id, "native.claude_code")
	cmd.Kind = &agentv1.Command_ResumeNativeSession{ResumeNativeSession: &agentv1.ResumeNativeSession{
		Session: &typev1.SessionId{Value: session}, OwnershipGeneration: gen,
	}}
	return cmd
}

func interruptCmd(id, session string, gen uint64) *agentv1.Command {
	cmd := envelope(id, "native.claude_code")
	cmd.Kind = &agentv1.Command_InterruptNativeSession{InterruptNativeSession: &agentv1.InterruptNativeSession{
		Session: &typev1.SessionId{Value: session}, OwnershipGeneration: gen,
	}}
	return cmd
}

func stopCmd(id, session string, gen uint64) *agentv1.Command {
	cmd := envelope(id, "native.claude_code")
	cmd.Kind = &agentv1.Command_StopNativeSession{StopNativeSession: &agentv1.StopNativeSession{
		Session: &typev1.SessionId{Value: session}, OwnershipGeneration: gen,
	}}
	return cmd
}

// startSession runs a start through the host and fails the test otherwise.
func startSession(t *testing.T, f *fixture, cmd *agentv1.Command) *agentv1.CommandResult {
	t.Helper()
	loggedCommand(t, f.db, cmd)
	res := f.h.Run(context.Background(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("start: status=%v reason=%v message=%q", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	return res
}

// fakeAdapter scripts one backend's answers and records every call.
type fakeAdapter struct {
	mu        sync.Mutex
	backend   typev1.NativeBackend
	calls     []string
	blockSend chan struct{}
	sendErr   *native.CallError
	startErr  *native.CallError
	classify  func(native.RawEvent) native.Classified
	provider  string
}

func (a *fakeAdapter) Backend() typev1.NativeBackend { return a.backend }
func (a *fakeAdapter) Version() uint32               { return 1 }

func (a *fakeAdapter) record(call string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, call)
}

func (a *fakeAdapter) started() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *fakeAdapter) Start(_ context.Context, spec native.StartSpec) (native.StartReceipt, *native.CallError) {
	if a.startErr != nil {
		return native.StartReceipt{}, a.startErr
	}
	provider := a.provider
	if provider == "" {
		provider = "provider-" + spec.SessionID
	}
	a.record("start:" + spec.SessionID)
	return native.StartReceipt{ProviderSessionID: provider, Model: spec.Model, Effort: spec.Effort}, nil
}

func (a *fakeAdapter) SendTurn(ctx context.Context, turn native.TurnSpec) (native.TurnReceipt, *native.CallError) {
	a.record("send:" + turn.SessionID)
	if a.blockSend != nil {
		select {
		case <-a.blockSend:
		case <-ctx.Done():
			return native.TurnReceipt{}, &native.CallError{Uncertain: true, Msg: "send cancelled"}
		}
	}
	if a.sendErr != nil {
		return native.TurnReceipt{}, a.sendErr
	}
	return native.TurnReceipt{ProviderTurnID: "turn-" + turn.SessionID}, nil
}

func (a *fakeAdapter) Resume(_ context.Context, rs native.ResumeSpec) (native.ResumeReceipt, *native.CallError) {
	a.record("resume:" + rs.SessionID)
	return native.ResumeReceipt{ProviderSessionKept: true}, nil
}

func (a *fakeAdapter) Interrupt(_ context.Context, sessionID string, _ uint64) (native.InterruptReceipt, *native.CallError) {
	a.record("interrupt:" + sessionID)
	return native.InterruptReceipt{Accepted: true}, nil
}

func (a *fakeAdapter) Stop(_ context.Context, st native.StopSpec) (native.StopReceipt, *native.CallError) {
	a.record("stop:" + st.SessionID)
	return native.StopReceipt{ProcessExited: true, CleanupComplete: true}, nil
}

func (a *fakeAdapter) Classify(raw native.RawEvent) native.Classified {
	if a.classify != nil {
		return a.classify(raw)
	}
	return native.Classified{Class: native.ClassUnknown, Name: raw.Kind}
}

// fakeEmitter records published events; it stamps them like production so
// unknown arms and coalesce keys are proven stampable.
type fakeEmitter struct {
	mu     sync.Mutex
	events []*agentv1.Event
	err    error
}

func (e *fakeEmitter) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	if e.err != nil {
		return 0, e.err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return uint64(len(e.events)), nil
}

func (e *fakeEmitter) all() []*agentv1.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*agentv1.Event(nil), e.events...)
}

func (e *fakeEmitter) arms() []string {
	var out []string
	for _, ev := range e.all() {
		switch ev.GetBody().(type) {
		case *agentv1.Event_NativeOutput:
			out = append(out, "native_output")
		case *agentv1.Event_NativeTool:
			out = append(out, "native_tool")
		case *agentv1.Event_NativeApproval:
			out = append(out, "native_approval")
		case *agentv1.Event_NativeUsage:
			out = append(out, "native_usage")
		case *agentv1.Event_NativeTurnEnded:
			out = append(out, "native_turn_ended")
		case *agentv1.Event_NativeProcessExited:
			out = append(out, "native_process_exited")
		case *agentv1.Event_NativeUnknown:
			out = append(out, "native_unknown")
		default:
			out = append(out, "other")
		}
	}
	return out
}

type testWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *testWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	return len(p), nil
}
