package tmux

import (
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The values of Pane.DeadSignal: a closed set, never tmux's own text. A
// signal outside the named eight is SignalOther and its number
// ("other:31"); text the client cannot read is SignalOther alone.
const (
	SignalHup   = "hup"
	SignalInt   = "int"
	SignalQuit  = "quit"
	SignalAbort = "abort"
	SignalKill  = "kill"
	SignalSegv  = "segv"
	SignalPipe  = "pipe"
	SignalTerm  = "term"
	SignalOther = "other"
)

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  SignalHup,
	syscall.SIGINT:  SignalInt,
	syscall.SIGQUIT: SignalQuit,
	syscall.SIGABRT: SignalAbort,
	syscall.SIGKILL: SignalKill,
	syscall.SIGSEGV: SignalSegv,
	syscall.SIGPIPE: SignalPipe,
	syscall.SIGTERM: SignalTerm,
}

// deadFields reads pane_dead_status and pane_dead_signal, the rule both
// pane reads share (ListPanes and ReadInventory). format.c fills the status
// only under WIFEXITED and the signal only under WIFSIGNALED: a program
// either exited or was killed, so both at once is a reply in the wrong
// shape (ok false), on any pane. A live pane has NoStatus and no signal;
// a dead one has its status when tmux sent one (a status that is not a
// number is ok false; none is a signal death, never 0) and its signal
// through deadSignal, which never fails.
func deadFields(dead bool, status, signal string) (int, string, bool) {
	if status != "" && signal != "" {
		return 0, "", false
	}
	if !dead {
		return NoStatus, "", true
	}
	if status == "" {
		return NoStatus, deadSignal(signal), true
	}
	st, err := strconv.Atoi(status)
	if err != nil {
		return 0, "", false
	}
	return st, "", true
}

// maxSignal is the highest number a wait status holds (WTERMSIG is 7 bits).
const maxSignal = 127

// deadSignal turns tmux's pane_dead_signal into the closed set. tmux 3.7c
// prints sys_signame[n] where the OS has it (macOS and the BSDs: "kill")
// and the number elsewhere (Linux: "9"; tmux.c sig2name); 3.2a has no such
// variable and prints nothing. A number is 1 to 127 with no sign or
// leading zero; a name is lower case and one this OS knows. The signal is
// a diagnostic, not structure, so anything else (a name this OS lacks,
// such as "iot" or "pwr", or a number out of range) is SignalOther with no
// number: the client never guesses one.
func deadSignal(f string) string {
	if f == "" {
		return ""
	}
	n, ok := signalNumber(f)
	if !ok {
		return SignalOther
	}
	if name, ok := signalNames[n]; ok {
		return name
	}
	return SignalOther + ":" + strconv.Itoa(int(n))
}

func signalNumber(f string) (syscall.Signal, bool) {
	if f[0] >= '1' && f[0] <= '9' {
		n, err := strconv.Atoi(f)
		if err != nil || n > maxSignal {
			return 0, false
		}
		return syscall.Signal(n), true
	}
	for i := range len(f) {
		c := f[i]
		if (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return 0, false
		}
	}
	n := unix.SignalNum("SIG" + strings.ToUpper(f))
	if n <= 0 || n > maxSignal {
		return 0, false
	}
	return n, true
}
