package machine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// maxFactBytes caps every text fact read from the machine. Facts are shown in
// the UI, so none may carry control characters or grow without bound.
const maxFactBytes = 64

// system is everything the facts read from the machine, so a test can supply
// another one.
type system struct {
	readlink   func(path string) (string, error)
	readFile   func(path string) ([]byte, error)
	exists     func(path string) bool
	osVersion  func() (string, error)
	memTotal   func(ctx context.Context) (uint64, error)
	username   func() (string, error)
	binaryHash func() (string, error)
	tmux       func(ctx context.Context) (string, error)
}

// CollectorOptions configure a [Collector].
type CollectorOptions struct {
	// Runner runs `tmux -V` (the proc caller "tmux"). Nil leaves
	// tmux_version unknown.
	Runner *proc.Runner
	// Metrics receives failed readings; nil reports nowhere.
	Metrics *Metrics
	// TmuxDirs are the directories searched for tmux when it is not on the
	// agent's PATH (launchd and systemd start an agent with a minimal one).
	// Nil is [DefaultTmuxDirs]; an empty non-nil slice searches none.
	TmuxDirs []string
	// Mem reads total memory; nil is the real reading ([DefaultReaders]).
	Mem func(ctx context.Context) (total, available uint64, err error)
}

// DefaultTmuxDirs are searched, in order, for tmux when PATH lacks it.
var DefaultTmuxDirs = []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin"}

// Collector fills the B1 machine facts that the connection loop's basic
// facts (conn.BasicFacts) do not have: os_version, ram_bytes, timezone,
// agent_binary_hash, tmux_version, container and username. A fact that
// cannot be read stays empty; it is never guessed.
//
// Static facts (OS version, RAM, user, binary hash, container) are read once
// and kept; one that failed is tried again at the next [Collector.Fill]. The
// time zone and tmux version are read at every Fill: the zone can change with
// the laptop, and tmux can be installed after the agent starts.
type Collector struct {
	sys system
	m   *Metrics

	mu        sync.Mutex
	osVersion string
	ram       uint64
	username  string
	hash      string
	container bool
}

// NewCollector returns a Collector and reads the static facts once now, so
// the binary hash describes the file the agent started from even if an
// upgrade replaces it later.
func NewCollector(ctx context.Context, o CollectorOptions) (*Collector, error) {
	m := o.Metrics
	if m == nil {
		var err error
		if m, err = NewMetrics(nil); err != nil {
			return nil, err
		}
	}
	memFn := o.Mem
	if memFn == nil {
		memFn = DefaultReaders().Mem
	}
	dirs := o.TmuxDirs
	if dirs == nil {
		dirs = DefaultTmuxDirs
	}
	s := system{
		readlink:  os.Readlink,
		readFile:  readSmall,
		exists:    exists,
		osVersion: platformOSVersion,
		memTotal: func(ctx context.Context) (uint64, error) {
			total, _, err := memFn(ctx)
			return total, err
		},
		username:   currentUsername,
		binaryHash: executableHash,
		tmux:       func(ctx context.Context) (string, error) { return tmuxVersion(ctx, o.Runner, dirs) },
	}
	return newCollector(ctx, s, m), nil
}

func newCollector(ctx context.Context, s system, m *Metrics) *Collector {
	c := &Collector{sys: s, m: m}
	c.container = s.exists("/.dockerenv") || s.exists("/run/.containerenv")
	t := newTally(m)
	c.mu.Lock()
	c.readStatic(ctx, t)
	c.mu.Unlock()
	t.settle(ctx)
	return c
}

// readStatic reads each static fact that is still missing. c.mu is held.
func (c *Collector) readStatic(ctx context.Context, t *tally) {
	try := func(source string, have bool, read func() error) {
		if have {
			return
		}
		if err := read(); err != nil {
			t.fail(ctx, source, err)
			return
		}
		t.ok(source)
	}
	try(sourceOSVersion, c.osVersion != "", func() (err error) { c.osVersion, err = c.sys.osVersion(); return })
	try(sourceRAM, c.ram != 0, func() (err error) { c.ram, err = c.sys.memTotal(ctx); return })
	try(sourceUsername, c.username != "", func() (err error) { c.username, err = c.sys.username(); return })
	try(sourceBinaryHash, c.hash != "", func() (err error) { c.hash, err = c.sys.binaryHash(); return })
}

// Fill sets the facts it can read on f. It sets nothing it cannot read, and
// leaves every other field of f as it found it.
func (c *Collector) Fill(ctx context.Context, f *agentv1.MachineFacts) {
	t := newTally(c.m)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readStatic(ctx, t)
	tz, err := readTimezone(c.sys.readlink, c.sys.readFile)
	if err != nil {
		t.fail(ctx, sourceTimezone, err)
		tz = ""
	} else {
		t.ok(sourceTimezone)
	}
	tv, err := c.sys.tmux(ctx)
	if err != nil {
		t.fail(ctx, sourceTmux, err)
		tv = ""
	} else {
		t.ok(sourceTmux)
	}
	t.settle(ctx)

	f.OsVersion, f.RamBytes, f.Timezone = c.osVersion, c.ram, tz
	f.AgentBinaryHash, f.TmuxVersion, f.Username = c.hash, tv, c.username
	f.Container = c.container
}

// tmuxVersionRE is what `tmux -V` prints: the program name and a version such
// as 3.7c, next-3.7 or openbsd-7.5.
var tmuxVersionRE = regexp.MustCompile(`^tmux [A-Za-z0-9][A-Za-z0-9._+-]{0,31}$`)

// parseTmuxVersion returns the first line of `tmux -V` output when it has the
// expected shape. The output is the child's text: nothing else is passed on.
func parseTmuxVersion(out []byte) (string, error) {
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if !tmuxVersionRE.MatchString(line) {
		return "", fmt.Errorf("%w: tmux -V output", errParse)
	}
	return line, nil
}

// tmuxVersion runs `tmux -V` through internal/proc under the measured caller
// "tmux": the tmux on the agent's PATH, else the first executable tmux in dirs.
// tmux not installed is an answer, not a failure: the version is empty, as 02e
// says. -V never contacts a server. The tmux client that rows 7 and 8 start
// must find tmux the same way, or facts and behaviour can name different
// programs.
func tmuxVersion(ctx context.Context, r *proc.Runner, dirs []string) (string, error) {
	if r == nil {
		return "", nil
	}
	caller, err := proc.For("tmux")
	if err != nil {
		return "", err
	}
	run := func(program string) (proc.Result, error) {
		return r.Run(ctx, proc.Spec{Caller: caller, Path: program, Args: []string{"-V"}})
	}
	res, err := run("tmux")
	if errors.Is(err, proc.ErrNotFound) {
		found := false
		for _, d := range dirs {
			if p := filepath.Join(d, "tmux"); isExecutableFile(p) {
				res, err = run(p)
				found = true
				break
			}
		}
		if !found {
			return "", nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("tmux -V: %w", err)
	}
	return parseTmuxVersion(res.Stdout)
}

// isExecutableFile reports a regular file with an execute bit.
func isExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// zoneFromLink takes the IANA name from the target of /etc/localtime: what
// follows the last "zoneinfo/".
func zoneFromLink(target string) (string, error) {
	i := strings.LastIndex(target, "zoneinfo/")
	if i < 0 {
		return "", fmt.Errorf("%w: localtime link", errParse)
	}
	return validZone(target[i+len("zoneinfo/"):])
}

// validZone accepts an IANA zone name: letters, digits, "_", "+", "-" and "/"
// separators, no empty or dot segments, at most [maxFactBytes] bytes.
func validZone(name string) (string, error) {
	if name == "" || len(name) > maxFactBytes {
		return "", fmt.Errorf("%w: zone name", errParse)
	}
	for seg := range strings.SplitSeq(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: zone name", errParse)
		}
		for _, r := range seg {
			if !isNameRune(r) && r != '+' {
				return "", fmt.Errorf("%w: zone name", errParse)
			}
		}
	}
	return name, nil
}

// isNameRune is a letter, digit, "_", "-" or ".".
func isNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
}

// readTimezone is the machine's IANA zone: the /etc/localtime link on macOS
// and most Linux, else the first line of /etc/timezone (Debian). Neither is
// an error, not "UTC".
func readTimezone(readlink func(string) (string, error), readFile func(string) ([]byte, error)) (string, error) {
	if target, err := readlink("/etc/localtime"); err == nil {
		if z, err := zoneFromLink(target); err == nil {
			return z, nil
		}
	}
	b, err := readFile("/etc/timezone")
	if err != nil {
		return "", fmt.Errorf("no /etc/localtime link and no /etc/timezone: %w", err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return validZone(strings.TrimSpace(line))
}

// parseOSRelease turns /etc/os-release into "<ID> <VERSION_ID>" ("ubuntu
// 24.04"), or just the ID on a rolling release.
func parseOSRelease(text string) (string, error) {
	var id, version string
	for line := range strings.SplitSeq(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	for _, v := range []string{id, version} {
		if len(v) > maxFactBytes {
			return "", fmt.Errorf("%w: os-release value", errParse)
		}
		for _, r := range v {
			if !isNameRune(r) {
				return "", fmt.Errorf("%w: os-release value", errParse)
			}
		}
	}
	if id == "" {
		return "", fmt.Errorf("%w: os-release has no ID", errParse)
	}
	if version == "" {
		return id, nil
	}
	return id + " " + version, nil
}

// validUsername accepts what a login name can be and nothing a shell or an ssh
// line would read differently: letters, digits, "_", "-", ".", and "$" last.
func validUsername(u string) bool {
	if u == "" || len(u) > maxFactBytes || u[0] == '-' {
		return false
	}
	for i, r := range u {
		if !isNameRune(r) && (r != '$' || i != len(u)-1) {
			return false
		}
	}
	return true
}

// currentUsername is the login user the agent runs as. os/user needs no cgo
// here: it reads /etc/passwd on Linux and the environment on macOS.
func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	if !validUsername(u.Username) {
		return "", fmt.Errorf("%w: user name", errParse)
	}
	return u.Username, nil
}

// executableHash is the hex SHA-256 of the file the agent was started from.
func executableHash() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable path: %w", err)
	}
	return hashFile(p)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the agent's own executable, from os.Executable.
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readSmall reads a small system file (at most 64 KiB).
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: only the fixed system paths in this package.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}
