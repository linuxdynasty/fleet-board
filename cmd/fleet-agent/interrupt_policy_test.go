package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Each case uses the real outbox, connection, executor and observer client.
// The policy is loaded before run starts; the live pane must survive.
func TestInterruptPolicyRefusesRealTmux(t *testing.T) {
	binary := tmuxtest.Binaries()[1]
	for _, tc := range []struct {
		name, policy, reason                                            string
		hubObserve, approval, deniedRole, scheduler, expired, rateLimit bool
	}{
		{name: "local none", policy: "default_server: none\n", reason: "POLICY_DENIED"},
		{name: "local observe", policy: "default_server: observe\n", reason: "POLICY_DENIED"},
		{name: "tighter held hub setting", reason: "POLICY_DENIED", hubObserve: true},
		{name: "local safe mode", policy: "unexpected_key: true\n", reason: "POLICY_SAFE_MODE"},
		{name: "local approval timeout", policy: "local_approval:\n  - command_kind: Interrupt\n    required: true\n", reason: "LOCAL_CONFIRM_TIMEOUT", approval: true},
		{name: "denied role", reason: "ROLE_INSUFFICIENT", deniedRole: true},
		{name: "scheduler without local ownership proof", reason: "ROLE_INSUFFICIENT", scheduler: true},
		{name: "expired", reason: "EXPIRED", expired: true},
		{name: "rate limit", policy: "rate_limits:\n  - command_kind: Interrupt\n    per_minute: 1\n", reason: "RATE_LIMITED", rateLimit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := tmuxtest.Start(t, binary, "-s", "spare", "exec sleep 60")
			server := base.Sibling(t, "default", "-s", "work", "exec sleep 60")
			pane := strings.TrimSpace(server.Run(t, "list-panes", "-t", "work", "-F", "#{pane_id}"))
			if tc.rateLimit {
				server.Run(t, "new-window", "-d", "-t", "work", "exec sleep 60")
			}
			s := newStartup(t)
			if tc.policy != "" {
				if err := os.MkdirAll(s.cfg, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(s.cfg, "policy.yaml"), []byte(tc.policy), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hubObserve {
				db, err := ring.Open(t.Context(), s.dir, ring.Options{Telemetry: s.tel})
				if err != nil {
					t.Fatal(err)
				}
				msg, err := proto.Marshal(&agentv1.Settings{
					SettingsEpoch: "epoch-1", Generation: 1,
					Values: &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE.Enum()},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.SaveSettings(t.Context(), ring.Held{Epoch: "epoch-1", Generation: 1, Settings: msg}); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			observer := make(chan *inventory.Observer, 1)
			wired := make(chan *settings.Manager, 1)
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
					}, func(m *settings.Manager) { wired <- m })
			}()
			t.Cleanup(func() { cancel(); <-done })
			var o *inventory.Observer
			select {
			case o = <-observer:
			case <-time.After(10 * time.Second):
				t.Fatal("agent observer did not start")
			}
			if err := o.Client("default").WaitState(ctx, tmux.StateUp); err != nil {
				t.Fatal(err)
			}
			waitInterruptCondition(t, 10*time.Second, "agent snapshot", func() bool {
				if s.hub.LastEpoch(t, s.machine) == 0 {
					return false
				}
				return s.hub.Machine(t, s.machine).SnapshotEpoch > 0
			})
			who := &typev1.Principal{
				Id:   &typev1.PrincipalId{Value: "local:test"},
				Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
				Role: typev1.Role_ROLE_OWNER,
			}
			if tc.deniedRole {
				who.Role = typev1.Role_ROLE_VIEWER
			}
			if tc.scheduler {
				who.Role = typev1.Role_ROLE_SCHEDULER
			}
			cmd := &agentv1.Command{
				Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{
					Server: "default", ServerStartId: o.Client("default").ServerStartID(), PaneId: pane,
				}}},
				Requires: &typev1.Capability{Name: interruptCapability, Version: 1}, RequestedBy: who,
			}
			if tc.approval {
				cmd.NotAfterTime = timestamppb.New(time.Now().Add(5 * time.Second))
			}
			if tc.expired {
				cmd.NotAfterTime = timestamppb.New(time.Now().Add(time.Second))
			}
			if tc.rateLimit {
				first := s.hub.Enqueue(t, s.machine, cmd)
				waitInterruptCondition(t, 5*time.Second, "first rate-limited command to finish", func() bool {
					for _, row := range s.hub.Commands(t, s.machine) {
						if row.CommandID == first[0].CommandID {
							return row.Status == "OK"
						}
					}
					return false
				})
				pane = strings.TrimSpace(server.Run(t, "list-panes", "-a", "-F", "#{pane_id}:#{pane_dead}"))
				for _, line := range strings.Split(pane, "\n") {
					if strings.HasSuffix(line, ":0") {
						pane = strings.TrimSuffix(line, ":0")
						break
					}
				}
				cmd.GetInterrupt().Pane.PaneId = pane
			}
			var ops []outbox.Op
			if tc.expired {
				if err := s.hub.CurrentStore().Write(ctx, func(tx *sql.Tx) error {
					op, err := outbox.Enqueue(ctx, tx, outbox.In{MachineID: s.machine, Command: cmd, Now: time.Now()})
					ops = []outbox.Op{op}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Until(cmd.GetNotAfterTime().AsTime()) + 100*time.Millisecond)
				if rows := s.hub.Commands(t, s.machine); len(rows) != 1 || rows[0].SentEpoch != 0 || len(s.hub.Results(t, s.machine)) != 0 {
					t.Fatalf("connected command advanced before after-commit wake: %+v", rows)
				}
				// The hub's expiry job closes a pending row before delivery.
				s.hub.ExpireCommands(t, time.Now())
			} else {
				ops = s.hub.Enqueue(t, s.machine, cmd)
			}
			if tc.approval {
				var sm *settings.Manager
				select {
				case sm = <-wired:
				case <-time.After(time.Second):
					t.Fatal("settings manager was not wired")
				}
				waitInterruptCondition(t, 2*time.Second, "local approval wait", func() bool {
					for _, row := range s.hub.Commands(t, s.machine) {
						if row.CommandID == ops[0].CommandID {
							return row.OpState == "RUNNING"
						}
					}
					return false
				})
				if sm.Effective().GetValues().GetDefaultServer() != typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL {
					t.Fatal("approval wait did not start under CONTROL")
				}
				if !sm.Hook(ctx, s.hub.LastEpoch(t, s.machine), &agentv1.Settings{
					SettingsEpoch: "epoch-1", Generation: 1,
					Values: &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE.Enum()},
				}) {
					t.Fatal("settings intake refused a tighter hub setting")
				}
				waitInterruptCondition(t, 2*time.Second, "settings to tighten during approval", func() bool {
					return sm.Effective().GetValues().GetDefaultServer() == typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE
				})
				var snapshot agentv1.Snapshot
				if err := sm.Snapshot(ctx, &snapshot); err != nil || snapshot.GetSettings().GetGeneration() != 1 ||
					snapshot.GetSettings().GetValues().GetDefaultServer() != typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE {
					t.Fatalf("tightened settings missing from Snapshot: %v, %v", snapshot.GetSettings(), err)
				}
			}
			waitInterruptCondition(t, 6*time.Second, "policy refusal", func() bool {
				for _, row := range s.hub.Commands(t, s.machine) {
					if row.CommandID == ops[0].CommandID {
						return row.Status != ""
					}
				}
				return false
			})
			for _, row := range s.hub.Commands(t, s.machine) {
				if row.CommandID == ops[0].CommandID && row.Reason != tc.reason {
					t.Fatalf("command = %+v, want reason %s", row, tc.reason)
				}
			}
			if !tc.expired {
				body, err := os.ReadFile(filepath.Join(s.dir, "audit.log"))
				if err != nil || !strings.Contains(string(body), "command.refused") ||
					!strings.Contains(string(body), ops[0].CommandID) {
					t.Fatalf("missing durable local refusal audit for %s: %v", ops[0].CommandID, err)
				}
			}
			if dead := strings.TrimSpace(server.Run(t, "display-message", "-p", "-t", pane, "-F", "#{pane_dead}")); dead != "0" {
				t.Fatalf("policy refusal touched the pane: pane_dead = %q", dead)
			}
		})
	}
}
