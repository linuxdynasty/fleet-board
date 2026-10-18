package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// A previous agent run may stop after marking F4 running but before storing
// its result. Restart recovery may send C-c again, but must still check the
// current server generation before touching any pane.
func TestUnfinishedInterruptResumesAgainstRealTmux(t *testing.T) {
	for _, binary := range tmuxtest.Binaries() {
		t.Run(binary.Name, func(t *testing.T) {
			for _, name := range []string{"current generation", "old generation", "tightened settings", "saved result without acknowledgement"} {
				t.Run(name, func(t *testing.T) {
					stale, tightened, saved := name == "old generation", name == "tightened settings", name == "saved result without acknowledgement"
					base := tmuxtest.Start(t, binary, "-s", "spare", "exec sleep 60")
					server := base.Sibling(t, "default", "-s", "work", "exec sleep 60")
					server.Run(t, "set-window-option", "-g", "remain-on-exit", "on")
					pane := strings.TrimSpace(server.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
					s := newStartup(t)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					runner, err := proc.NewRunner(s.tel)
					if err != nil {
						t.Fatal(err)
					}
					o, err := inventory.New(inventory.Options{
						Program: binary.Path, Runner: runner, Telemetry: s.tel,
						Folder: base.Dir, Servers: []string{"default"},
					})
					if err != nil {
						t.Fatal(err)
					}
					// The observer's server-start id is the key the earlier run
					// would have logged. Starting the observer here also keeps
					// the setup independent of the developer's own tmux server.
					observerCtx, stopObserver := context.WithCancel(ctx)
					observerDone := make(chan struct{})
					go func() { o.Run(observerCtx); close(observerDone) }()
					t.Cleanup(func() { stopObserver(); <-observerDone })
					if err := o.Client("default").WaitState(ctx, tmux.StateUp); err != nil {
						t.Fatal(err)
					}
					start := o.Client("default").ServerStartID()
					if stale {
						start = "older-generation"
					}
					who := &typev1.Principal{
						Id: &typev1.PrincipalId{Value: "local:test"}, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
						Role: typev1.Role_ROLE_OWNER,
					}
					cmd := &agentv1.Command{
						CommandId: &typev1.CommandId{Value: "unfinished-interrupt"},
						Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{
							Server: "default", ServerStartId: start, PaneId: pane,
						}}},
						Requires:    &typev1.Capability{Name: interruptCapability, Version: 1},
						RequestedBy: who, NotAfterTime: timestamppb.New(time.Now().Add(time.Minute)),
					}
					// The hub's durable row and the agent's unfinished log row
					// share one command id. The agent log is left RUNNING with no
					// result, as a crash during the earlier attempt would leave it.
					s.hub.Enqueue(t, s.machine, cmd)
					db, err := ring.Open(ctx, s.dir, ring.Options{Telemetry: s.tel})
					if err != nil {
						t.Fatal(err)
					}
					if err := db.StartEpoch(ctx, 1); err != nil {
						t.Fatal(err)
					}
					cmd.Epoch, cmd.Seq = 1, 1
					body, err := proto.Marshal(cmd)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := db.LogAndApply(ctx, ring.Command{
						ID: cmd.GetCommandId().GetValue(), Kind: "Interrupt", Epoch: 1, Seq: 1,
						Received: time.Now(), NotAfter: cmd.NotAfterTime.AsTime(), Body: body,
					}); err != nil {
						t.Fatal(err)
					}
					if err := db.SetRunning(ctx, cmd.GetCommandId().GetValue(), 0); err != nil {
						t.Fatal(err)
					}
					if saved {
						result, err := proto.Marshal(&agentv1.CommandResult{
							CommandId: cmd.CommandId, Status: typev1.Status_STATUS_OK,
							Message: "saved result from the earlier attempt",
						})
						if err != nil {
							t.Fatal(err)
						}
						if err := db.RecordResult(ctx, cmd.GetCommandId().GetValue(), result, time.Now()); err != nil {
							t.Fatal(err)
						}
					}
					if tightened {
						kept, err := proto.Marshal(&agentv1.Settings{
							SettingsEpoch: "epoch-1", Generation: 1,
							Values: &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE.Enum()},
						})
						if err != nil {
							t.Fatal(err)
						}
						if err := db.SaveSettings(ctx, ring.Held{Epoch: "epoch-1", Generation: 1, Settings: kept}); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					go func() {
						done <- runAgentWithTmux(ctx, s.dir, s.cfg, "test", "boot", s.tel, nil,
							func(context.Context, *sync.WaitGroup, *telemetry.Telemetry) (tmuxRun, error) {
								return tmuxRun{obs: o, program: binary.Path}, nil
							})
					}()
					t.Cleanup(func() { cancel(); <-done })
					waitInterruptCondition(t, 10*time.Second, "resumed durable result", func() bool {
						return len(s.hub.Results(t, s.machine)) == 1
					})
					row := s.hub.Commands(t, s.machine)[0]
					wantStatus, wantReason, wantDead := "OK", "", "1"
					if stale {
						wantStatus, wantReason, wantDead = "FAILED", "PANE_NOT_FOUND", "0"
					}
					if tightened {
						wantStatus, wantReason, wantDead = "REJECTED_BY_POLICY", "POLICY_DENIED", "0"
					}
					if saved {
						wantDead = "0"
						if got := s.hub.Results(t, s.machine)[0].Message; got != "saved result from the earlier attempt" {
							t.Fatalf("replayed result = %q, want the saved result", got)
						}
					}
					if row.Status != wantStatus || row.Reason != wantReason {
						t.Fatalf("resumed command = %+v, want status %s reason %s", row, wantStatus, wantReason)
					}
					waitInterruptCondition(t, 5*time.Second, "resumed pane state", func() bool {
						return strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}")) == wantDead
					})
				})
			}
		})
	}
}
