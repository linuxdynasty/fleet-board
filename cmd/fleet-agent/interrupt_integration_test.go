package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// This is the public route, including the hub's stored outbox and the agent's
// real connection and command log. The tmux server holds only a sleep process.
func TestInterruptPaneTravelsFromHubToRealTmux(t *testing.T) {
	for _, binary := range tmuxtest.Binaries() {
		t.Run(binary.Name, func(t *testing.T) {
			base := tmuxtest.Start(t, binary, "-s", "spare", "exec sleep 60")
			server := base.Sibling(t, "default", "-s", "work", "exec sleep 60")
			server.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			pane := strings.TrimSpace(server.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
			hellos := make(chan helloObservation, 4)
			s := newStartup(t, conntest.Options{Wrap: captureHello(hellos)})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			observer := make(chan *inventory.Observer, 1)
			done := make(chan error, 1)
			go func() {
				done <- runAgentWithTmux(ctx, s.dir, s.cfg, "test", "boot", s.tel, nil,
					func(ctx context.Context, wg *sync.WaitGroup, tel *telemetry.Telemetry) (tmuxRun, error) {
						runner, err := proc.NewRunner(tel)
						if err != nil {
							return tmuxRun{}, err
						}
						o, err := inventory.New(inventory.Options{
							Program: binary.Path, Runner: runner, Telemetry: tel,
							Folder: base.Dir, Servers: []string{"default"},
						})
						if err != nil {
							return tmuxRun{}, err
						}
						wg.Go(func() { o.Run(ctx) })
						observer <- o
						return tmuxRun{obs: o, program: binary.Path}, nil
					})
			}()
			t.Cleanup(func() { cancel(); <-done })
			var o *inventory.Observer
			select {
			case o = <-observer:
			case <-time.After(10 * time.Second):
				t.Fatal("the agent did not start its tmux observer")
			}
			if err := o.Client("default").WaitState(ctx, tmux.StateUp); err != nil {
				t.Fatal(err)
			}
			start := o.Client("default").ServerStartID()
			waitInterruptCondition(t, 10*time.Second, "agent connected", func() bool {
				if s.hub.LastEpoch(t, s.machine) == 0 {
					return false
				}
				return s.hub.Machine(t, s.machine).SnapshotEpoch > 0
			})
			select {
			case seen := <-hellos:
				if seen.err != nil {
					t.Fatal(seen.err)
				}
				caps := seen.hello.GetCapabilities()
				if len(caps) != 1 || caps[0].GetName() != interruptCapability || caps[0].GetVersion() != 1 {
					t.Fatalf("production Hello capabilities = %v, want only %s v1", caps, interruptCapability)
				}
			case <-time.After(time.Second):
				t.Fatal("no production Hello captured")
			}
			if err := s.hub.CurrentStore().Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO hub_sessions(session_id, machine_id, create_time)
					VALUES ('sess-work', ?, ?)`, s.machine, time.Now().UnixMilli())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var wakes atomic.Int32
			fleet, err := api.NewFleet(api.FleetOptions{
				Store: s.hub.CurrentStore(), CACert: s.hub.CACert,
				Telemetry: s.hub.Telemetry(), HubInstanceID: "interrupt-integration",
				OnCommandQueued: func(op outbox.Op) {
					wakes.Add(1)
					s.hub.NotifyCommand(op)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			who := &typev1.Principal{
				Id:   &typev1.PrincipalId{Value: "local:test"},
				Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
				Role: typev1.Role_ROLE_OWNER,
			}
			request := &hubv1.InterruptPaneRequest{
				RequestId: "req-interrupt-work", Session: &typev1.SessionId{Value: "sess-work"},
				Pane: &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane},
			}
			res, err := fleet.InterruptPane(api.WithPrincipal(ctx, who), connect.NewRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if res.Msg.GetOperation().GetId().GetValue() == "" {
				t.Fatal("InterruptPane returned no operation")
			}
			waitInterruptCondition(t, 8*time.Second, "Interrupt result at the hub", func() bool {
				return len(s.hub.Results(t, s.machine)) == 1
			})
			if got := s.hub.Results(t, s.machine)[0].Status; got != "OK" {
				t.Fatalf("result status %q, want OK", got)
			}
			commandID := s.hub.Commands(t, s.machine)[0].CommandID
			audits, err := statedb.ListStoredAuditEntries(ctx, s.hub.CurrentStore())
			if err != nil {
				t.Fatal(err)
			}
			var interrupts int
			for _, row := range audits {
				if row.Action == "session.interrupt" {
					interrupts++
					if row.CommandID != commandID || row.RequestID != request.GetRequestId() {
						t.Fatalf("hub command audit = %+v", row)
					}
				}
			}
			if interrupts != 1 {
				t.Fatalf("hub command audit count = %d, want one: %+v", interrupts, audits)
			}
			if lines := conntest.Lines(t, s.logs.String(), "command.received"); len(lines) != 1 || lines[0]["command_id"] != commandID {
				t.Fatalf("command.received = %v", lines)
			}
			if lines := conntest.Lines(t, s.logs.String(), "command.finished"); len(lines) != 1 || lines[0]["command_id"] != commandID {
				t.Fatalf("command.finished = %v", lines)
			}
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			rec := httptest.NewRecorder()
			s.tel.MetricsHandler(nil).ServeHTTP(rec, req)
			if !strings.Contains(rec.Body.String(), "fleet_agent_commands_total") || !strings.Contains(rec.Body.String(), `status="OK"`) {
				t.Fatal("the real executor did not report its command result metric")
			}
			waitInterruptCondition(t, 5*time.Second, "the pane to exit after C-c", func() bool {
				return strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}")) == "1"
			})
			replayed, err := fleet.InterruptPane(api.WithPrincipal(ctx, who), connect.NewRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if replayed.Msg.GetOperation().GetId().GetValue() != res.Msg.GetOperation().GetId().GetValue() {
				t.Fatal("request replay changed the durable operation id")
			}
			if rows := s.hub.Commands(t, s.machine); len(rows) != 1 || rows[0].Status != "OK" {
				t.Fatalf("replay changed the command rows: %+v", rows)
			}
			if got := wakes.Load(); got != 1 {
				t.Fatalf("after-commit wakes = %d, want one for the new row only", got)
			}
			_, err = fleet.InterruptPane(api.WithPrincipal(ctx, who), connect.NewRequest(&hubv1.InterruptPaneRequest{
				RequestId: "req-wrong-server", Session: &typev1.SessionId{Value: "sess-work"},
				Pane: &typev1.PaneKey{Server: "fleet", ServerStartId: start, PaneId: pane},
			}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || len(s.hub.Commands(t, s.machine)) != 1 || wakes.Load() != 1 {
				t.Fatalf("hub accepted another server: error %v, rows %v, wakes %d", err, s.hub.Commands(t, s.machine), wakes.Load())
			}
			guard := strings.TrimSpace(server.Run(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "work", "exec sleep 60"))
			if guard == "" {
				t.Fatal("guard pane was not created")
			}
			for _, tc := range []struct {
				name, requirement string
				version           uint32
				key               *typev1.PaneKey
				otherKind         bool
				status, reason    string
			}{
				{"empty requirement", "", 0, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: guard}, false, "UNSUPPORTED", ""},
				{"unknown capability", "tmux.control", 1, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: guard}, false, "UNSUPPORTED", ""},
				{"zero version", interruptCapability, 0, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: guard}, false, "UNSUPPORTED", ""},
				{"higher version", interruptCapability, 2, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: guard}, false, "UNSUPPORTED", ""},
				{"other kind", interruptCapability, 1, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: guard}, true, "UNSUPPORTED", ""},
				{"other server", interruptCapability, 1, &typev1.PaneKey{Server: "fleet", ServerStartId: start, PaneId: guard}, false, "FAILED", "INVALID_ARGUMENT"},
				{"probe server", interruptCapability, 1, &typev1.PaneKey{Server: "fleet-probe", ServerStartId: start, PaneId: guard}, false, "FAILED", "INVALID_ARGUMENT"},
				{"old generation", interruptCapability, 1, &typev1.PaneKey{Server: "default", ServerStartId: "older-server", PaneId: guard}, false, "FAILED", "PANE_NOT_FOUND"},
				{"missing pane", interruptCapability, 1, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: "%9999"}, false, "FAILED", "PANE_NOT_FOUND"},
				{"dead pane", interruptCapability, 1, &typev1.PaneKey{Server: "default", ServerStartId: start, PaneId: pane}, false, "FAILED", "PANE_BUSY"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cmd := &agentv1.Command{RequestedBy: who}
					if tc.otherKind {
						cmd.Kind = &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{Pane: tc.key}}
					} else {
						cmd.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: tc.key}}
					}
					if tc.requirement != "" {
						cmd.Requires = &typev1.Capability{Name: tc.requirement, Version: tc.version}
					}
					ops := s.hub.Enqueue(t, s.machine, cmd)
					waitInterruptCondition(t, 5*time.Second, "refusal result", func() bool {
						for _, row := range s.hub.Commands(t, s.machine) {
							if row.CommandID == ops[0].CommandID {
								return row.Status != ""
							}
						}
						return false
					})
					for _, row := range s.hub.Commands(t, s.machine) {
						if row.CommandID == ops[0].CommandID && (row.Status != tc.status || (tc.reason != "" && row.Reason != tc.reason)) {
							t.Fatalf("command = %+v, want status %s reason %s", row, tc.status, tc.reason)
						}
					}
					if dead := strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", guard, "-F", "#{pane_dead}")); dead != "0" {
						t.Fatalf("guard pane was touched: pane_dead = %q", dead)
					}
				})
			}
			priorEpoch := s.hub.LastEpoch(t, s.machine)
			s.hub.Restart(t)
			waitInterruptCondition(t, 10*time.Second, "agent reconnect and snapshot", func() bool {
				return s.hub.Machine(t, s.machine).SnapshotEpoch > priorEpoch
			})
			restarted, err := api.NewFleet(api.FleetOptions{
				Store: s.hub.CurrentStore(), CACert: s.hub.CACert,
				Telemetry: s.hub.Telemetry(), HubInstanceID: "interrupt-restart",
				OnCommandQueued: s.hub.NotifyCommand,
			})
			if err != nil {
				t.Fatal(err)
			}
			again, err := restarted.InterruptPane(api.WithPrincipal(ctx, who), connect.NewRequest(request))
			if err != nil {
				t.Fatal(err)
			}
			if again.Msg.GetOperation().GetId().GetValue() != res.Msg.GetOperation().GetId().GetValue() ||
				len(s.hub.Commands(t, s.machine)) != 11 {
				t.Fatal("hub reconnect changed the saved operation or replayed a completed command")
			}
			operator := &typev1.Principal{
				Id:   &typev1.PrincipalId{Value: "local:operator"},
				Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER, Role: typev1.Role_ROLE_OPERATOR,
			}
			manual := s.hub.Enqueue(t, s.machine, &agentv1.Command{
				Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{
					Server: "default", ServerStartId: start, PaneId: guard,
				}}},
				Requires: &typev1.Capability{Name: interruptCapability, Version: 1}, RequestedBy: operator,
			})
			waitInterruptCondition(t, 5*time.Second, "manual operator interrupt", func() bool {
				for _, row := range s.hub.Commands(t, s.machine) {
					if row.CommandID == manual[0].CommandID {
						return row.Status == "OK"
					}
				}
				return false
			})
			waitInterruptCondition(t, 5*time.Second, "operator C-c to exit pane", func() bool {
				return strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", guard, "-F", "#{pane_dead}")) == "1"
			})
			server.Kill(t)
			if err := o.Client("default").WaitState(ctx, tmux.StateAbsent); err != nil {
				t.Fatal(err)
			}
			if !base.Alive() {
				t.Fatal("the unrelated tmux server stopped with default")
			}
			down := s.hub.Enqueue(t, s.machine, &agentv1.Command{
				Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{
					Server: "default", ServerStartId: start, PaneId: guard,
				}}},
				Requires: &typev1.Capability{Name: interruptCapability, Version: 1}, RequestedBy: who,
			})
			waitInterruptCondition(t, 5*time.Second, "detached client refusal", func() bool {
				for _, row := range s.hub.Commands(t, s.machine) {
					if row.CommandID == down[0].CommandID {
						return row.Status != ""
					}
				}
				return false
			})
			for _, row := range s.hub.Commands(t, s.machine) {
				if row.CommandID == down[0].CommandID && (row.Status != "FAILED" || row.Reason != "TMUX_SERVER_DOWN") {
					t.Fatalf("detached client result = %+v", row)
				}
			}
			server.NewSession(t, "-s", "again", "exec sleep 60")
			server.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
			if err := o.Client("default").WaitState(ctx, tmux.StateUp); err != nil {
				t.Fatal(err)
			}
			newPane := strings.TrimSpace(server.Run(t, "list-panes", "-t", "again", "-F", "#{pane_id}"))
			if newPane != pane || o.Client("default").ServerStartID() == start {
				t.Fatalf("restart did not reuse %s with a new generation: pane %s, start %s", pane, newPane, o.Client("default").ServerStartID())
			}
			stale := s.hub.Enqueue(t, s.machine, &agentv1.Command{
				Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{
					Server: "default", ServerStartId: start, PaneId: pane,
				}}},
				Requires: &typev1.Capability{Name: interruptCapability, Version: 1}, RequestedBy: who,
			})
			waitInterruptCondition(t, 5*time.Second, "reused pane refusal", func() bool {
				for _, row := range s.hub.Commands(t, s.machine) {
					if row.CommandID == stale[0].CommandID {
						return row.Status != ""
					}
				}
				return false
			})
			for _, row := range s.hub.Commands(t, s.machine) {
				if row.CommandID == stale[0].CommandID && (row.Status != "FAILED" || row.Reason != "PANE_NOT_FOUND") {
					t.Fatalf("reused pane result = %+v", row)
				}
			}
			if dead := strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", newPane, "-F", "#{pane_dead}")); dead != "0" {
				t.Fatalf("interrupt reached a reused pane: pane_dead = %q", dead)
			}
		})
	}
}

func waitInterruptCondition(t *testing.T, within time.Duration, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
