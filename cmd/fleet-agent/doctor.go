package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// doctorHandshakeTimeout bounds the TLS handshake of one hub address
// (defaults.md "TLS handshake per hub address").
const doctorHandshakeTimeout = 10 * time.Second

// doctorDeps are the parts of `doctor` a test replaces.
type doctorDeps struct {
	// probeDir is the usage probe's folder (01 C3).
	probeDir func() (string, error)
	// tmux runs `tmux -V` through the agent's measured proc caller.
	tmux func(ctx context.Context) (string, error)
	// handshake runs a TLS handshake against the pinned CA, never Connect.
	handshake func(ctx context.Context, addr, caPEM string) error
	// linger runs `loginctl show-user --property=Linger` on Linux.
	linger func(ctx context.Context) (string, error)
	// probeTrust starts one harness in the probe folder, reads its first
	// screen, sends no key and tears the session down (01 C3).
	probeTrust func(ctx context.Context, harness, probeDir string) (screen string, installed bool, err error)
	// lingerUser is named in the linger fix line.
	lingerUser string
	// checks is the list to run; nil is doctor.Local.
	checks []doctor.Check
}

// doctorCommand is `fleet-agent doctor`: every local check with a reason, one
// line each (or one JSON object with --json). It reads status.json and
// enroll.json, so it works while the agent is down; the only processes it
// starts are `tmux -V` and the probe-trust look, which starts each installed
// harness once in the probe folder on a throwaway tmux server, reads the
// first screen, sends no key and tears the session down (01 C3). Exit 0 when
// nothing failed, 1 when a check failed.
func doctorCommand(dir, cfg func() (string, error), stdout io.Writer) *cobra.Command {
	u, _ := user.Current()
	name := ""
	if u != nil {
		name = u.Username
	}
	return newDoctorCommand(dir, cfg, doctorDeps{
		probeDir: defaultProbeDir, tmux: tmuxVersion,
		handshake: doctorHandshake, linger: lingerOutput, lingerUser: name,
		probeTrust: probeTrustCapture,
	}, stdout)
}

func newDoctorCommand(dir, cfg func() (string, error), deps doctorDeps, stdout io.Writer) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and say what is wrong, with a fix for each problem",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return commandError{err}
			}
			cd, err := cfg()
			if err != nil {
				return commandError{err}
			}
			probe, err := deps.probeDir()
			if err != nil {
				return commandError{err}
			}
			checks := deps.checks
			if checks == nil {
				checks = doctor.Local()
			}
			rep := doctor.Run(cmd.Context(), doctor.Options{
				StateDir: d, ConfigDir: cd, ProbeDir: probe, TmuxVersion: deps.tmux,
				HubHandshake: deps.handshake, Linger: deps.linger, LingerUser: deps.lingerUser,
				ProbeTrustCapture: deps.probeTrust,
			}, checks...)
			write := doctor.WriteText
			if asJSON {
				write = doctor.WriteJSON
			}
			if err := write(stdout, rep); err != nil {
				return commandError{err}
			}
			if rep.Failed() {
				return commandError{fmt.Errorf("%d check(s) failed", rep.Summary.Fail)}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as one JSON object")
	return cmd
}

// defaultProbeDir is ~/.local/share/fleet-agent/probe, the same on macOS and
// Linux (01 C3).
func defaultProbeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home folder: %w", err)
	}
	return filepath.Join(home, ".local", "share", "fleet-agent", "probe"), nil
}

// tmuxVersion asks tmux for its version the way the machine facts do. The
// runner takes no telemetry: doctor prints to the person who ran it.
func tmuxVersion(ctx context.Context) (string, error) {
	r, err := proc.NewRunner(nil)
	if err != nil {
		return "", err
	}
	return machine.TmuxVersion(ctx, r, nil)
}

// doctorHandshake runs a TLS handshake against the pinned CA at addr and
// nothing more: no client certificate, no Connect call. A hub the peer fails
// (the pin, the name, the key proof, a non-TLS answer) is ErrHubUntrusted;
// a refused, dropped or timed-out connection is returned as is.
func doctorHandshake(ctx context.Context, addr, caPEM string) error {
	roots, host, err := doctorHandshakeRoots(caPEM, addr)
	if err != nil {
		return err
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: host,
		NextProtos: []string{"h2"},
		// The system roots never apply: the chain is verified against the
		// enrolled CA alone, with no TOFU.
		InsecureSkipVerify: true, //nolint:gosec // G402: VerifyConnection verifies the chain against the pinned CA
		VerifyConnection: func(cs tls.ConnectionState) error {
			return doctorVerifyPinned(cs, roots, host)
		},
	}
	d := &net.Dialer{Timeout: doctorHandshakeTimeout}
	hctx, cancel := context.WithTimeout(ctx, doctorHandshakeTimeout)
	defer cancel()
	raw, err := d.DialContext(hctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = raw.Close() }()
	c := tls.Client(raw, cfg)
	if err := c.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		var rh tls.RecordHeaderError
		var cv *tls.CertificateVerificationError
		if errors.As(err, &rh) || errors.As(err, &cv) {
			return fmt.Errorf("%w: %w", doctor.ErrHubUntrusted, err)
		}
		return err
	}
	_ = c.Close()
	return nil
}

func doctorHandshakeRoots(caPEM, addr string) (*x509.CertPool, string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", doctor.ErrHubUntrusted, err)
	}
	b, _ := pem.Decode([]byte(caPEM))
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, "", fmt.Errorf("%w: the pinned CA does not parse", doctor.ErrHubUntrusted)
	}
	ca, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("%w: the pinned CA does not parse", doctor.ErrHubUntrusted)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return roots, host, nil
}

func doctorVerifyPinned(cs tls.ConnectionState, roots *x509.CertPool, host string) error {
	if len(cs.PeerCertificates) == 0 {
		return fmt.Errorf("%w: the hub sent no certificate", doctor.ErrHubUntrusted)
	}
	if _, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots: roots, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("%w: %w", doctor.ErrHubUntrusted, err)
	}
	return nil
}

// lingerOutput runs `loginctl show-user <user> --property=Linger` through
// the measured proc caller and returns its output.
func lingerOutput(ctx context.Context) (string, error) {
	u, err := user.Current()
	if err != nil || u == nil || u.Username == "" {
		return "", errNoLingerUser
	}
	r, err := proc.NewRunner(nil)
	if err != nil {
		return "", err
	}
	caller, err := proc.For("loginctl")
	if err != nil {
		return "", err
	}
	res, err := r.Run(ctx, proc.Spec{
		Caller: caller, Path: "loginctl",
		Args: []string{"show-user", u.Username, "--property=Linger"},
	})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

var errNoLingerUser = errors.New("cannot find the current user")
