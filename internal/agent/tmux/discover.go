package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// ErrTooOld is a tmux older than 3.2, or one whose control client does not
// list ignore-size and pause-after in client_flags. It is durable until the
// machine's facts change: REASON_CODE_TMUX_TOO_OLD (02l, 03h).
var ErrTooOld = errors.New("tmux: older than 3.2 or without the control-mode flags (TMUX_TOO_OLD)")

// errAbsent is no server on the socket, or one with no session to attach.
var errAbsent = errors.New("tmux: no server on the socket")

// versionRE reads the release from `tmux -V`: "tmux 3.2a", "tmux 3.7c",
// "tmux next-3.8", "tmux openbsd-7.5". Only a plain major.minor is compared.
var versionRE = regexp.MustCompile(`^tmux (?:next-)?([0-9]+)\.([0-9]+)[a-z]?$`)

// checkVersion runs `program -V` (no server is contacted) and refuses a
// release below 3.2. A version it cannot read (openbsd-7.5, master) is not
// refused here: the client_flags gate after the attach decides.
func checkVersion(ctx context.Context, r *proc.Runner, caller proc.Caller, program string) error {
	res, err := r.Run(ctx, proc.Spec{Caller: caller, Path: program, Args: []string{"-V"}})
	if err != nil {
		return fmt.Errorf("tmux -V: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\n")
	m := versionRE.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 3 || major == 3 && minor < 2 {
		return ErrTooOld
	}
	return nil
}

// sessionsFormat lists each session's id and the creator's internal mark
// (the @fleet_internal user option, row-7 ruling fork 6): ids and a mark
// only, never a session name.
const sessionsFormat = "#{session_id} #{@fleet_internal}"

// lowestSession lists the sessions with one `tmux list-sessions` through
// internal/proc and returns the lowest session id, skipping sessions the
// fleet marked internal when skipInternal is set (the developer's server;
// on the probe server every session is internal, row-7 ruling fork 5(d)).
// A failed list (no server, a stale socket) or no session is errAbsent.
func lowestSession(ctx context.Context, r *proc.Runner, caller proc.Caller, program, socket string, skipInternal bool) (SessionID, error) {
	res, err := r.Run(ctx, proc.Spec{
		Caller: caller, Path: program,
		Args: []string{"-S", socket, "list-sessions", "-F", sessionsFormat},
		Env:  withoutTMUX(os.Environ()),
	})
	var exit *proc.ExitError
	if errors.As(err, &exit) {
		return 0, errAbsent
	}
	if err != nil {
		return 0, fmt.Errorf("tmux list-sessions: %w", err)
	}
	best := SessionID(NoID)
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		idText, mark, ok := strings.Cut(line, " ")
		id, idOK := parseID([]byte(idText), '$')
		if !ok || !idOK {
			return 0, errors.New("tmux list-sessions: a line is not \"$N mark\"")
		}
		if skipInternal && mark != "" {
			continue
		}
		if best == NoID || SessionID(id) < best {
			best = SessionID(id)
		}
	}
	if best == NoID {
		return 0, errAbsent
	}
	return best, nil
}
