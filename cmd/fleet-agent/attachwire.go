package main

import (
	"context"
	"path/filepath"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/attach"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// attachServers names the tmux servers an attach can target by
// SessionTarget kind. The probe server is internal (usage probes only),
// so it is not served.
var attachServers = map[typev1.TmuxServerKind]string{
	typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT: "default",
	typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET:   "fleet",
}

// attachRun is the terminal-attach wiring of one run (01f J1): the
// manager behind the OpenAttach and CloseAttach handlers. Nil without
// tmux, or without a served server.
type attachRun struct {
	mgr *attach.Manager
}

// startAttach builds the manager against the observer's sockets, or nil
// when there is no tmux server an attach could target. loop is the
// connection loop the Terminal streams open on; Dial only calls it once
// commands arrive, by which time the loop exists. runCtx is the agent run
// context, the pumps' ancestor.
func startAttach(runCtx context.Context, tmx tmuxRun, loop *conn.Loop, tel *telemetry.Telemetry) (*attachRun, error) {
	if tmx.obs == nil {
		return nil, nil
	}
	served := false
	for _, name := range attachServers {
		if tmx.obs.Client(name) != nil {
			served = true
		}
	}
	if !served {
		return nil, nil
	}
	caller, err := proc.For("tmux")
	if err != nil {
		return nil, err
	}
	runner, err := proc.NewRunner(tel)
	if err != nil {
		return nil, err
	}
	mgr, err := attach.New(runCtx, attach.Options{
		Program: tmx.program, Runner: runner, Caller: caller,
		ServerStart: func(server string) (string, bool) {
			c := tmx.obs.Client(server)
			if c == nil {
				return "", false
			}
			if id := c.ServerStartID(); id != "" {
				return id, true
			}
			return "", false
		},
		Sockets: func(kind typev1.TmuxServerKind) (string, string, bool) {
			name, ok := attachServers[kind]
			if !ok || tmx.obs.Client(name) == nil {
				return "", "", false
			}
			return filepath.Join(tmx.obs.Folder(), name), name, true
		},
		Dial: func(ctx context.Context) (attach.Stream, error) {
			return loop.OpenTerminal(ctx)
		},
		Log: tel.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &attachRun{mgr: mgr}, nil
}

// handlers returns the OpenAttach and CloseAttach handlers, nil without a
// manager.
func (a *attachRun) handlers() []exec.Handler {
	if a == nil || a.mgr == nil {
		return nil
	}
	return []exec.Handler{a.mgr.OpenHandler(), a.mgr.CloseHandler()}
}

// openCount is the heartbeat's open_attaches: the live attach ids. Nil
// safe, so the seam can close over the run's attach wiring before it
// exists.
func (a *attachRun) openCount() int {
	if a == nil || a.mgr == nil {
		return 0
	}
	return len(a.mgr.OpenIDs())
}

// dropAll ends every attach for agent shutdown: the hub hears
// AGENT_SHUTDOWN where the stream still takes it, then every PTY and view
// dies while the panes keep running. Nil safe.
func (a *attachRun) dropAll() {
	if a == nil || a.mgr == nil {
		return
	}
	a.mgr.DropAll()
}
