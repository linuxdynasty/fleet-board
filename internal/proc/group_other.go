//go:build !darwin && !linux

package proc

import (
	"os"
	"syscall"
)

// fleet-agent runs on macOS and Linux only; elsewhere the package compiles
// and a cap kills the child alone, with no process group.

func groupAttr() *syscall.SysProcAttr { return nil }

func killGroupOf(p *os.Process) error { return p.Kill() }

func killGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
