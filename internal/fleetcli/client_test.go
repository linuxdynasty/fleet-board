package fleetcli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/creack/pty"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func TestClientAcceptsOnlyLocalPlainHTTP(t *testing.T) {
	for _, raw := range []string{
		"", "http://example.com", "https://127.0.0.1:7080", "http://127.0.0.1:0",
		"http://localhost:65536", "http://user@127.0.0.1:7080", "http://127.0.0.1:7080/x",
		"http://127.0.0.1:7080/%2e", "http://127.0.0.1:7080?x", "http://127.0.0.1:7080#x",
		"http://localhost.evil:7080", "http://[fe80::1%25en0]:7080",
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := newClient(raw)
			if err != errUsage {
				t.Fatalf("newClient(%q) = %v, want usage refusal", raw, err)
			}
		})
	}
	for _, raw := range []string{"http://127.0.0.1:7080", "http://localhost:7080/", "http://[::1]:7080"} {
		c, closeClient, err := newClient(raw)
		if err != nil || c == nil {
			t.Fatalf("newClient(%q): %v", raw, err)
		}
		closeClient()
	}
}

func TestCancellationStopsAWaitingRead(t *testing.T) {
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, diag bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"machines", "--json"}, Input{
			Stdout: &out, Stderr: &diag,
			LookupEnv: func(name string) (string, bool) {
				if name == "FLEET_URL" {
					return srv.URL, true
				}
				return "", false
			},
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 1 || out.Len() != 0 {
			t.Fatalf("cancel = %d, stdout %q, stderr %q", code, out.String(), diag.String())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request did not stop")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "GetFleetSummary") && !strings.Contains(r.URL.Path, "GetStoreStatus") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	client, closeClient, err := newClient(first.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	_, err = client.GetFleetSummary(context.Background(), connect.NewRequest(&hubv1.GetFleetSummaryRequest{}))
	if err == nil {
		t.Fatal("redirect was accepted")
	}
	if reached.Load() != 0 {
		t.Fatal("second server received the redirected call")
	}
	store := connect.NewRequest(&hubv1.GetStoreStatusRequest{})
	store.Header().Set("X-Fleet-Approval-Secret", strings.Repeat("a", 64))
	_, err = client.GetStoreStatus(context.Background(), store)
	if err == nil || reached.Load() != 0 {
		t.Fatal("approval secret followed a redirect")
	}
}

func TestDoctorCountsPreflightButPausesBudgetForHumanEntry(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		preflight, system               time.Duration
		wantCode, wantSystem, wantStore int
		wantPrompt                      bool
	}{
		{"remaining budget succeeds after a long human wait", 100 * time.Millisecond, 5 * time.Millisecond, 0, 1, 1, true},
		{"slow preflight leaves too little time", 100 * time.Millisecond, 380 * time.Millisecond, 1, 1, 0, true},
		{"expired preflight never prompts", 460 * time.Millisecond, 0, 1, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var systemCalls, storeCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message proto.Message
				switch {
				case strings.HasSuffix(r.URL.Path, "/GetServerInfo"):
					if r.Header.Get("X-Fleet-Approval-Secret") != "" {
						t.Error("preflight carried approval secret")
					}
					time.Sleep(tc.preflight)
					message = &hubv1.GetServerInfoResponse{AuthMode: hubv1.AuthMode_AUTH_MODE_LOCAL_TRUSTED}
				case strings.HasSuffix(r.URL.Path, "/GetSystemStatus"):
					if r.Header.Get("X-Fleet-Approval-Secret") != "" {
						t.Error("system read carried approval secret")
					}
					systemCalls.Add(1)
					time.Sleep(tc.system)
					message = &hubv1.GetSystemStatusResponse{Status: &hubv1.SystemStatus{}}
				case strings.HasSuffix(r.URL.Path, "/GetStoreStatus"):
					if r.Header.Get("X-Fleet-Approval-Secret") != strings.Repeat("a", 64) {
						t.Error("store read lacked terminal approval secret")
					}
					storeCalls.Add(1)
					message = &hubv1.GetStoreStatusResponse{Status: &hubv1.StoreStatus{}}
				default:
					t.Errorf("unexpected call %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				payload, err := proto.Marshal(message)
				if err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "application/proto")
				_, _ = w.Write(payload)
			}))
			defer srv.Close()
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			prompt := &signalWriter{ready: make(chan struct{}, 1)}
			var output bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- Run(t.Context(), []string{"doctor", "--json"}, Input{
					Stdin: slave, Stdout: &output, Stderr: prompt, NetworkBudget: 400 * time.Millisecond,
					LookupEnv: func(name string) (string, bool) {
						if name == "FLEET_URL" {
							return srv.URL, true
						}
						return "", false
					},
				})
			}()
			if tc.wantPrompt {
				select {
				case <-prompt.ready:
				case <-time.After(time.Second):
					t.Fatal("no prompt")
				}
				time.Sleep(450 * time.Millisecond)
				if _, err := master.Write([]byte(strings.Repeat("a", 64) + "\n")); err != nil {
					t.Fatal(err)
				}
			} else {
				select {
				case <-prompt.ready:
					t.Fatal("prompt after expired preflight")
				case <-time.After(480 * time.Millisecond):
				}
			}
			select {
			case code := <-done:
				if code != tc.wantCode {
					t.Fatalf("exit = %d, want %d; stderr %q", code, tc.wantCode, prompt.String())
				}
			case <-time.After(time.Second):
				t.Fatal("doctor did not finish")
			}
			if int(systemCalls.Load()) != tc.wantSystem || int(storeCalls.Load()) != tc.wantStore {
				t.Fatalf("calls system/store = %d/%d, want %d/%d", systemCalls.Load(), storeCalls.Load(), tc.wantSystem, tc.wantStore)
			}
			if tc.wantCode != 0 && output.Len() != 0 {
				t.Fatalf("failed doctor wrote %q", output.String())
			}
			if tc.wantCode == 0 && !strings.Contains(output.String(), `"store"`) {
				t.Fatalf("success lost store: %q", output.String())
			}
		})
	}
}
