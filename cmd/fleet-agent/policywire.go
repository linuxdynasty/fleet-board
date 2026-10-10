package main

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// configDirFlag adds the persistent --config-dir flag and returns the
// resolver every command shares: the flag, else $XDG_CONFIG_HOME/fleet-agent
// when that is an absolute path, else ~/.config/fleet-agent (03e, ADR 0009).
func configDirFlag(root *cobra.Command) func() (string, error) {
	var dir string
	root.PersistentFlags().StringVar(&dir, "config-dir", "",
		"agent config folder, where policy.yaml lives (default $XDG_CONFIG_HOME/fleet-agent, else ~/.config/fleet-agent)")
	return func() (string, error) {
		if dir != "" {
			return filepath.Abs(dir)
		}
		return policy.DefaultDir()
	}
}

// chainSnapshot runs the Snapshot hooks in order, so the machine facts and the
// policy each fill their own part of one Snapshot. The first error stops the
// chain.
func chainSnapshot(hooks ...func(context.Context, *agentv1.Snapshot) error) func(context.Context, *agentv1.Snapshot) error {
	return func(ctx context.Context, s *agentv1.Snapshot) error {
		for _, h := range hooks {
			if err := h(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}
}

// policyRun is the local policy (L1) and the audit log (M1) of one run.
type policyRun struct {
	mgr   *policy.Manager
	audit *audit.Log
	watch sync.WaitGroup
	// The three steps of close, in the order it runs them. They are fields
	// so a test can record the order.
	stopWatch, closeManager, closeAudit func()
}

// startPolicy makes the config folder when it is missing (so there is
// something to watch; it never writes policy.yaml), loads the policy, opens
// audit.log in the state folder and starts the directory watch. A config
// folder that cannot be made is logged and the agent runs on: a missing policy
// file is a normal first run, and the watch goes off. The caller ends it with
// close.
func startPolicy(ctx context.Context, cfgDir, stateDir, machineID string, tel *telemetry.Telemetry) (*policyRun, error) {
	log := tel.Logger.With("machine_id", machineID)
	if err := policy.EnsureDir(cfgDir); err != nil {
		log.With("component", "policy").WarnContext(ctx, "policy.config_dir_failed", "err_kind", "io")
	}
	al, err := audit.New(stateDir, audit.Options{Telemetry: tel, Logger: log.With("component", "audit")})
	if err != nil {
		return nil, err
	}
	mgr, err := policy.Load(policy.Options{Dir: cfgDir, Telemetry: tel, Logger: log.With("component", "policy"), Audit: al})
	if err != nil {
		_ = al.Close()
		return nil, err
	}
	wctx, cancel := context.WithCancel(ctx)
	p := &policyRun{mgr: mgr, audit: al, closeManager: mgr.Close, closeAudit: func() { _ = al.Close() }}
	p.stopWatch = func() { cancel(); p.watch.Wait() }
	p.watch.Go(func() { _ = mgr.Watch(wctx) })
	return p, nil
}

// close stops the watch, lets the manager report the changes it already
// found (they write audit rows), and only then closes audit.log.
func (p *policyRun) close() {
	p.stopWatch()    // the watch ends and looks at the file one last time
	p.closeManager() // the loader reports what it found, writing audit rows
	p.closeAudit()   // only now may audit.log close
}
