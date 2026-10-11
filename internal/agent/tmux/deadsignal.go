package tmux

import (
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The values of Pane.DeadSignal: a closed set, never tmux's own text. Any
// other signal is SignalOther and its number ("other:31").
const (
	SignalHup   = "hup"
	SignalInt   = "int"
	SignalQuit  = "quit"
	SignalAbort = "abort"
	SignalKill  = "kill"
	SignalSegv  = "segv"
	SignalPipe  = "pipe"
	SignalTerm  = "term"
	SignalOther = "other:"
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

// maxSignal is the highest number a wait status holds (WTERMSIG is 7 bits).
const maxSignal = 127

// deadSignal turns tmux's pane_dead_signal into the closed set. tmux 3.7c
// prints sys_signame[n] where the OS has it (macOS: "kill") and the number
// elsewhere (Linux: "9"; tmux.c sig2name); 3.2a has no such variable and
// prints nothing. A number is 1 to 127 with no sign or leading zero; a
// name is lower case and one this OS knows. Anything else is not ok.
func deadSignal(f string) (string, bool) {
	if f == "" {
		return "", true
	}
	n, ok := signalNumber(f)
	if !ok {
		return "", false
	}
	if name, ok := signalNames[n]; ok {
		return name, true
	}
	return SignalOther + strconv.Itoa(int(n)), true
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
