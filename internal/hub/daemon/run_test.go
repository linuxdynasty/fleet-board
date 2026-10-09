package daemon_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func port(a net.Addr) string {
	_, p, _ := net.SplitHostPort(a.String())
	return p
}

// plainClient keeps no idle connection, so none is left open (and unused)
// when the hub shuts down.
var plainClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func TestRunRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		setup  func(t *testing.T, h *hub)
		reason daemon.Reason
	}{
		{"no state folder", func(*testing.T, *hub) {}, daemon.ReasonStoreMissing},
		{"no state.db", func(t *testing.T, h *hub) {
			t.Helper()
			if err := statedb.EnsureDir(h.state); err != nil {
				t.Fatal(err)
			}
		}, daemon.ReasonStoreMissing},
		{"store without hub_identity", func(t *testing.T, h *hub) {
			t.Helper()
			if err := statedb.EnsureDir(h.state); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.state, "state.db")
			s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}, daemon.ReasonIdentityMissing},
		{"loose state folder", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			if err := os.Chmod(h.state, 0o755); err != nil { //nolint:gosec // G302: loosened on purpose; Run must refuse it
				t.Fatal(err)
			}
		}, daemon.ReasonLooseMode},
		{"loose state.db", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			if err := os.Chmod(filepath.Join(h.state, "state.db"), 0o644); err != nil { //nolint:gosec // G302: loosened on purpose; Run must refuse it
				t.Fatal(err)
			}
		}, daemon.ReasonLooseMode},
		{"non-loopback API address", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			body := "state_dir: " + h.state + "\nlisten: {api: \"0.0.0.0:7080\"}\n"
			if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, daemon.ReasonConfig},
		{"pprof", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			body := "state_dir: " + h.state + "\nlisten: {agent: \"0.0.0.0:0\", api: \"127.0.0.1:0\", pprof: \"127.0.0.1:6060\"}\n"
			if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, daemon.ReasonConfig},
		{"missing config file", func(t *testing.T, h *hub) {
			t.Helper()
			if err := os.Remove(h.config); err != nil {
				t.Fatal(err)
			}
		}, daemon.ReasonConfig},
		{"bad line in revoked-serials", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			h.writeRevoked(t, "# fleet-board revoked-serials v1\nNOT-HEX 2030-01-01T00:00:00Z\n", 0o600)
		}, daemon.ReasonRevocations},
		{"loose revoked-serials", func(t *testing.T, h *hub) {
			t.Helper()
			h.init(t)
			h.writeRevoked(t, "# fleet-board revoked-serials v1\n", 0o644)
		}, daemon.ReasonLooseMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHub(t, "")
			tc.setup(t, h)
			err := daemon.Run(context.Background(), h.options())
			if err == nil {
				t.Fatal("Run started")
			}
			if got := reasonOf(t, err); got != tc.reason {
				t.Errorf("reason %q, want %q (%v)", got, tc.reason, err)
			}
			if tc.reason == daemon.ReasonStoreMissing {
				if _, err := os.Stat(filepath.Join(h.state, "state.db")); !os.IsNotExist(err) {
					t.Errorf("Run created state.db: %v", err)
				}
			}
			body := h.scrape(t)
			if got := sample(body, "fleet_hub_start_failures_total", `command="run"`, `reason="`+string(tc.reason)+`"`); got != 1 {
				t.Errorf("fleet_hub_start_failures_total{run,%s} = %v, want 1", tc.reason, got)
			}
			if len(h.logs.events(t, "hub.start_failed")) != 1 {
				t.Error("want one hub.start_failed log line")
			}
		})
	}
}

func TestRunServesTheAgentListenerAndMetrics(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	id := h.storedIdentity(t)
	pool := caPool(t, id)
	r := h.start(t, h.options())
	agent := "127.0.0.1:" + port(r.addrs.Agent)

	t.Run("TLS 1.3 for every default name", func(t *testing.T) {
		for _, tc := range []struct{ addr, serverName string }{
			{agent, "127.0.0.1"},
			{agent, "localhost"},
			{agent, "fleet-test-host"},
			{agent, "fleet-test-host.local"},
			{"[::1]:" + port(r.addrs.Agent), "::1"},
		} {
			st, err := dialAgent(t, pool, tc.addr, tc.serverName, 0)
			if err != nil {
				t.Errorf("%s as %s: %v", tc.addr, tc.serverName, err)
				continue
			}
			if st.Version != tls.VersionTLS13 || !bytes.Equal(st.PeerCertificates[0].Raw, id.ServerCert) {
				t.Errorf("%s: version %s, stored certificate served %v", tc.serverName, tls.VersionName(st.Version),
					bytes.Equal(st.PeerCertificates[0].Raw, id.ServerCert))
			}
		}
	})
	t.Run("a TLS 1.2 client fails", func(t *testing.T) {
		if _, err := dialAgent(t, pool, agent, "127.0.0.1", tls.VersionTLS12); err == nil {
			t.Error("a TLS 1.2-capped client connected")
		}
	})
	t.Run("AgentService serves Enroll and RenewCert and leaves the rest Unimplemented", func(t *testing.T) {
		hc := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true,
		}}
		defer hc.CloseIdleConnections()
		client := agentv1connect.NewAgentServiceClient(hc, "https://"+agent, connect.WithGRPC())
		_, err := client.Enroll(context.Background(), connect.NewRequest(&agentv1.EnrollRequest{}))
		if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
			t.Errorf("Enroll with no install_id: code %v, want invalid_argument", code)
		}
		_, err = client.RenewCert(context.Background(), connect.NewRequest(&agentv1.RenewCertRequest{}))
		if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
			t.Errorf("RenewCert without a client certificate: code %v, want unauthenticated", code)
		}
		stream := client.SessionLog(context.Background())
		_ = stream.CloseRequest()
		_, err = stream.Receive()
		_ = stream.CloseResponse()
		if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
			t.Errorf("SessionLog: code %v, want unimplemented", code)
		}
	})
	t.Run("metrics behind the Host and Origin checks", func(t *testing.T) {
		api := "127.0.0.1:" + port(r.addrs.API)
		for _, tc := range []struct {
			name, host, origin string
			want               int
		}{
			{"right Host", api, "", 200},
			{"localhost Host", "localhost:" + port(r.addrs.API), "", 200},
			{"foreign Host", "evil.example:" + port(r.addrs.API), "", 403},
			{"foreign Origin", api, "http://evil.example", 403},
		} {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+api+"/metrics", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := plainClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
			}
		}
	})
	t.Run("a second run on the same folder is refused while the first serves (#92)", func(t *testing.T) {
		err := daemon.Run(context.Background(), h.options())
		if got := reasonOf(t, err); got != daemon.ReasonLockHeld {
			t.Errorf("second Run reason %q, want %q", got, daemon.ReasonLockHeld)
		}
		if _, err := dialAgent(t, pool, agent, "127.0.0.1", 0); err != nil {
			t.Errorf("the first hub stopped serving after the refused second run: %v", err)
		}
	})
	r.stop(t)
	if len(h.logs.events(t, "hub.stopped")) != 1 {
		t.Error("no hub.stopped log line after a clean stop")
	}
}

func TestTheOptionalMetricsListenerHasItsOwnHostCheck(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	body := "state_dir: " + h.state + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\", metrics: \"127.0.0.1:0\"}\n"
	if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.start(t, h.options())
	if r.addrs.Metrics == nil {
		t.Fatal("no metrics listener")
	}
	m := "127.0.0.1:" + port(r.addrs.Metrics)
	for _, tc := range []struct {
		host string
		want int
	}{{m, 200}, {"127.0.0.1:" + port(r.addrs.API), 403}} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+m+"/metrics", nil)
		req.Host = tc.host
		resp, err := plainClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("Host %s: status %d, want %d", tc.host, resp.StatusCode, tc.want)
		}
	}
}

func TestChangedAgentNamesAreServedAfterARestartWithTheSameCAAndFleet(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	before := h.storedIdentity(t)
	h.writeConfig(t, "tls: {agent_names: [\"hub.example\", \"127.0.0.1\"]}\n")
	r := h.start(t, h.options())
	agent := "127.0.0.1:" + port(r.addrs.Agent)
	pool := caPool(t, before)
	if _, err := dialAgent(t, pool, agent, "hub.example", 0); err != nil {
		t.Errorf("hub.example after the names changed: %v", err)
	}
	if _, err := dialAgent(t, pool, agent, "fleet-test-host", 0); err == nil {
		t.Error("the old name is still served")
	}
	r.stop(t)
	after := h.storedIdentity(t)
	if !bytes.Equal(after.CACert, before.CACert) || !bytes.Equal(after.CAKey, before.CAKey) || after.FleetID != before.FleetID {
		t.Error("the restart changed the CA or the fleet id")
	}
	if bytes.Equal(after.ServerCert, before.ServerCert) {
		t.Error("state.db still holds the old server certificate")
	}
}

// The fake clock starts 61 days back, so the certificate init issues is fresh
// to the hub yet valid to a client on real time; moving the clock to now puts
// it past day 60 of 90.
func TestPastTwoThirdsTheServedCertificateChangesWithoutARestart(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	var now atomicTime
	now.set(time.Now().Add(-61 * 24 * time.Hour))
	opts := h.options()
	opts.Now = now.get
	if err := daemon.Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pool := caPool(t, h.storedIdentity(t))
	ticks := make(chan time.Time)
	opts.RenewTicks = ticks
	r := h.start(t, opts)
	agent := "127.0.0.1:" + port(r.addrs.Agent)
	first, err := dialAgent(t, pool, agent, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	now.set(time.Now())
	ticks <- now.get()
	ticks <- now.get() // the first tick has been handled once the second is taken
	second, err := dialAgent(t, pool, agent, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.PeerCertificates[0].Raw, second.PeerCertificates[0].Raw) {
		t.Error("the served certificate did not change past two thirds")
	}
}

type atomicTime struct{ ns atomic.Int64 }

func (a *atomicTime) set(at time.Time) { a.ns.Store(at.UnixNano()) }
func (a *atomicTime) get() time.Time   { return time.Unix(0, a.ns.Load()) }
