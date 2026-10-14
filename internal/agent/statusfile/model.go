package statusfile

import (
	"sort"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// StatusFile is the name of the running agent's status file in the state
// folder.
const StatusFile = "status.json"

// layout is the version of the files' layout. A reader refuses another
// version and prints unknown rather than guess.
const layout = 1

// Bounds on what a file may hold. They keep the file small and keep a
// hostile or damaged one from filling a terminal.
const (
	maxFile     = 64 << 10 // the largest file Read opens
	maxFailures = 32       // hub addresses with a recorded failure
	maxAddr     = 270      // one hub address, as conn caps it
	maxToken    = 40       // one closed-enum word
	maxDropped  = 8        // reasons in Browse.DroppedBy
)

// StateUnknown stands for a state, end or browse result the file holds
// that is not one of the closed values.
const StateUnknown = "unknown"

// PolicyWatchOff is the one value of Status.PolicyWatch: the policy
// loader could not set its directory watch (03e).
const PolicyWatchOff = "off"

// TmuxWatchOff is the one value of Status.TmuxWatch: the agent could not
// set its watch on the tmux socket folder (row-7 ruling fork 4(b)).
const TmuxWatchOff = "off"

// maxFolderBytes bounds Status.TmuxFolder.
const maxFolderBytes = 256

// maxAmbiguous bounds Status.AmbiguousFolders: folders are checkouts, and
// dozens already mean a broken roots setting.
const maxAmbiguous = 32

// The three results of an mDNS browse (ADR 0028 decision 3). They are
// their own closed values, not err_kind classes.
const (
	BrowseFound                = "found"
	BrowseNone                 = "none"
	BrowseNoMulticastInterface = "no_multicast_interface"
)

var (
	states = setOf("discovering", "connecting", "hello_sent", "snapshotting", "connected", "backoff", "stopped", "ended")
	ends   = setOf("revoked", "unknown_machine", "fleet_mismatch", "cert_expired")
	finds  = setOf(BrowseFound, BrowseNone, BrowseNoMulticastInterface)
	watch  = setOf(PolicyWatchOff)
)

func setOf(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

// Status is the content of status.json: what the connection loop knows,
// as the loop keeps it in memory ([conn.Status]), stamped with the time it
// was written. A value not yet known is left out, never written as 0 or an
// empty word, so a reader can say "unknown". It holds closed words, times,
// numbers and hub addresses: no token and no error text. The one path is
// the tmux socket folder the agent watches, which doctor prints.
type Status struct {
	Version     int       `json:"version"`
	WrittenTime time.Time `json:"written_time"`
	State       string    `json:"state,omitempty"`
	// Peer is the hub address of the live stream.
	Peer string `json:"peer,omitempty"`
	// Failures is the last failure per hub address tried.
	Failures map[string]Failure `json:"failures,omitempty"`
	// PushOff is true when the network watcher's push socket is off and it
	// polls instead (ADR 0029).
	PushOff bool `json:"push_off"`
	// Clock is hub time minus the agent's clock at the last HelloAck.
	Clock *Clock `json:"clock,omitempty"`
	// Ended is the end the loop stopped on (written before the agent exits).
	Ended                string     `json:"ended,omitempty"`
	SnapshotOversizeTime *time.Time `json:"snapshot_oversize_time,omitempty"`
	Browse               *Browse    `json:"browse,omitempty"`
	// PolicyWatch is [PolicyWatchOff] when the policy loader could not set
	// its directory watch; left out while the watch works (or is not
	// known), so doctor can tell "working" from "never said".
	PolicyWatch string `json:"policy_watch,omitempty"`
	// TmuxWatch is [TmuxWatchOff] when the agent could not set its watch
	// on the tmux socket folder; left out while it works.
	TmuxWatch string `json:"tmux_watch,omitempty"`
	// TmuxFolder is the tmux socket folder the agent watches
	// ($TMUX_TMPDIR or /tmp, then tmux-<uid>): the launchd-versus-shell
	// TMUX_TMPDIR mismatch shows here.
	TmuxFolder string `json:"tmux_folder,omitempty"`
	// AmbiguousFolders are the checkout folders holding two harness panes
	// (02h "Observed goals"): no goal is attributed there, and doctor
	// lists them. Left out when empty.
	AmbiguousFolders []string `json:"ambiguous_folders,omitempty"`
}

// Failure is the last failure for one hub address.
type Failure struct {
	Step    string     `json:"step"`
	ErrKind string     `json:"err_kind"`
	Time    *time.Time `json:"time,omitempty"`
}

// Clock is a measured offset between the hub's clock and the agent's.
type Clock struct {
	OffsetMS int64     `json:"offset_ms"`
	Time     time.Time `json:"time"`
}

// Browse is the last mDNS browse.
type Browse struct {
	// Result is [BrowseFound], [BrowseNone] or [BrowseNoMulticastInterface].
	Result    string         `json:"result"`
	Time      time.Time      `json:"time"`
	ElapsedMS int64          `json:"elapsed_ms"`
	Kept      int            `json:"kept"`
	Dropped   int            `json:"dropped"`
	DroppedBy map[string]int `json:"dropped_by,omitempty"`
}

// FromConn turns the loop's status into the file's content, stamped with
// now. Every string is cleaned and bounded, so the file holds nothing the
// loop's status could carry beyond closed words and addresses.
func FromConn(s conn.Status, now time.Time) Status {
	out := Status{
		Version: layout, WrittenTime: now.UTC().Truncate(time.Millisecond),
		State: string(s.State), Peer: s.Peer, PushOff: s.PushOff, Ended: string(s.Ended),
	}
	if len(s.Failures) > 0 {
		out.Failures = make(map[string]Failure, len(s.Failures))
		for addr, f := range s.Failures {
			out.Failures[addr] = Failure{Step: f.Step, ErrKind: f.ErrKind, Time: timePtr(f.Time)}
		}
	}
	if !s.ClockOffsetTime.IsZero() {
		out.Clock = &Clock{OffsetMS: s.ClockOffset.Milliseconds(), Time: s.ClockOffsetTime.UTC()}
	}
	out.SnapshotOversizeTime = timePtr(s.SnapshotOversize)
	if b := s.Browse; !b.Time.IsZero() {
		res := BrowseNone
		switch {
		case b.ErrKind != "" && b.Kept == 0:
			res = BrowseNoMulticastInterface
		case b.Kept > 0:
			res = BrowseFound
		}
		out.Browse = &Browse{
			Result: res, Time: b.Time.UTC(), ElapsedMS: b.Elapsed.Milliseconds(),
			Kept: b.Kept, Dropped: b.Dropped, DroppedBy: b.DroppedBy,
		}
	}
	return out.cleaned()
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

// cleaned returns s with every string reduced to its closed form and the
// maps bounded. FromConn and Read both apply it, so a value that reaches a
// terminal has passed the same filter whoever wrote the file.
func (s Status) cleaned() Status {
	s.State = enum(s.State, states)
	s.Ended = enum(s.Ended, ends)
	s.PolicyWatch = enum(s.PolicyWatch, watch)
	s.TmuxWatch = enum(s.TmuxWatch, watch)
	s.TmuxFolder = folderPath(s.TmuxFolder)
	s.AmbiguousFolders = cleanFolders(s.AmbiguousFolders)
	s.Peer = addr(s.Peer)
	if len(s.Failures) > 0 {
		s.Failures = boundFailures(s.Failures)
	}
	if s.Browse != nil {
		b := *s.Browse
		b.Result = enum(b.Result, finds)
		b.Kept, b.Dropped = max(b.Kept, 0), max(b.Dropped, 0)
		b.DroppedBy = boundCounts(b.DroppedBy)
		s.Browse = &b
	}
	return s
}

// folderPath keeps an absolute path of printable ASCII up to
// maxFolderBytes; anything else reads as unknown ("").
func folderPath(p string) string {
	if len(p) > maxFolderBytes || !strings.HasPrefix(p, "/") {
		return ""
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return ""
		}
	}
	return p
}

// cleanFolders keeps the absolute, printable, bounded folders, sorted,
// at most maxAmbiguous: a hostile or damaged file reads clean.
func cleanFolders(in []string) []string {
	var out []string
	for _, f := range in {
		if p := folderPath(f); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out[:min(len(out), maxAmbiguous)]
}

// boundFailures cleans each entry and keeps the newest maxFailures.
func boundFailures(in map[string]Failure) map[string]Failure {
	type kv struct {
		addr string
		f    Failure
	}
	all := make([]kv, 0, len(in))
	for a, f := range in {
		f.Step, f.ErrKind = token(f.Step), token(f.ErrKind)
		all = append(all, kv{addr(a), f})
	}
	newest := func(i int) time.Time {
		if t := all[i].f.Time; t != nil {
			return *t
		}
		return time.Time{}
	}
	sort.Slice(all, func(i, j int) bool {
		if ti, tj := newest(i), newest(j); !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].addr < all[j].addr
	})
	all = all[:min(len(all), maxFailures)]
	out := make(map[string]Failure, len(all))
	for _, e := range all {
		out[e.addr] = e.f
	}
	return out
}

func boundCounts(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	names := make([]string, 0, len(in))
	for k := range in {
		names = append(names, k)
	}
	sort.Strings(names)
	out := map[string]int{}
	for _, k := range names {
		if len(out) == maxDropped {
			break
		}
		out[token(k)] += max(in[k], 0)
	}
	return out
}

// enum keeps v when it is one of the closed values, else [StateUnknown];
// empty stays empty (unset).
func enum(v string, set map[string]bool) string {
	switch {
	case v == "":
		return ""
	case set[v]:
		return v
	default:
		return StateUnknown
	}
}

// token reduces a word the loop chose (a step, an err_kind class) to
// lowercase letters, digits and underscore, or "other".
func token(s string) string {
	if s == "" || len(s) > maxToken || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return "other"
	}
	return s
}

// addr keeps the characters of a host:port (a DNS name, an IPv4 or IPv6
// address, a zone) and drops the rest, bounded to maxAddr.
func addr(s string) string {
	if s == "" {
		return ""
	}
	kept := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == ':', r == '-', r == '_', r == '[', r == ']', r == '%':
			return r
		}
		return -1
	}, s)
	if kept == "" {
		return "invalid"
	}
	return kept[:min(len(kept), maxAddr)]
}

// Age is how long before now the file was written; 0 when the file is
// stamped later than now (a clock set back).
func (s Status) Age(now time.Time) time.Duration {
	return max(now.Sub(s.WrittenTime), 0)
}
