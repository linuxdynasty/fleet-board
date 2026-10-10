//go:build darwin || linux

package proc

import (
	"errors"
	"os"
	"syscall"
)

// groupAttr puts the child in a new process group of its own, so the group id
// is the child's pid and one signal reaches every descendant that did not
// leave it.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// killGroupOf is killGroup for the child p, and does nothing once p was waited
// for: from then on its pid, and so its group id, may be free. It is not
// atomic with Wait (the check, then the kill), which is why a group kill is
// only safe while some member of the group is alive.
func killGroupOf(p *os.Process) error {
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return os.ErrProcessDone
	}
	return killGroup(p.Pid)
}

// killGroup sends SIGKILL to the whole group led by pid. It returns
// [os.ErrProcessDone] when no member is left.
func killGroup(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone // never -0 or a negative group: that would signal our own
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
