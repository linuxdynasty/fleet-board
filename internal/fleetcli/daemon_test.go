package fleetcli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/linuxdynasty/fleet-board/internal/fleetcli"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/seed"
)

type localHub struct {
	url, secret string
}

func startHub(t *testing.T) localHub {
	t.Helper()
	return startHubWithSeed(t, true)
}

func startHubWithSeed(t *testing.T, withSeed bool) localHub {
	t.Helper()
	return startHubWithExtra(t, withSeed, nil)
}

func startHubWithExtra(t *testing.T, withSeed bool, extra func(*testing.T, string)) localHub {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fleet-cli-") //nolint:usetesting // macOS t.TempDir paths can exceed the hub admin Unix socket limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	state := filepath.Join(dir, "state")
	config := filepath.Join(dir, "fleetd.yaml")
	if err := os.WriteFile(config, []byte("state_dir: "+state+"\nlisten: {agent: '127.0.0.1:0', api: '127.0.0.1:0'}\ndiscovery: {mdns: false}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var shown bytes.Buffer
	opts := daemon.Options{
		ConfigPath: config, Hostname: func() (string, error) { return "fleet-cli-test.local", nil },
		Now:          func() time.Time { return time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) },
		SecretOutput: &shown, LogOutput: io.Discard, Version: "test",
	}
	if err := daemon.Init(t.Context(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	secret := strings.TrimSpace(strings.Split(shown.String(), "\n")[1])
	if withSeed {
		seedMachine(t, state)
	}
	if extra != nil {
		extra(t, state)
	}
	ctx, stop := context.WithCancel(context.Background())
	ready := make(chan daemon.Addrs, 1)
	opts.Ready = func(a daemon.Addrs) { ready <- a }
	opts.SecretOutput = io.Discard
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, opts) }()
	var addr daemon.Addrs
	select {
	case addr = <-ready:
	case err := <-done:
		stop()
		t.Fatalf("Run: %v", err)
	case <-time.After(20 * time.Second):
		stop()
		t.Fatal("hub did not start")
	}
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run stop: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("Run did not stop")
		}
	})
	// Run checks for a nightly backup at startup. Wait for its published copy
	// before reading the store, so the golden response has a stable shape.
	backupPath := filepath.Join(state, "backups", "nightly-20261019T000000Z.db")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(backupPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat nightly backup: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("hub did not publish its nightly backup")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return localHub{url: "http://" + addr.API.String(), secret: secret}
}

func TestEmptyListsThroughTheRealDaemonTCPListener(t *testing.T) {
	h := startHubWithSeed(t, false)
	for _, name := range []string{"machines", "accounts", "goals", "work"} {
		code, plain, diagnostic := invoke(t, h, name)
		if code != 0 || !strings.Contains(plain, "No results.\n") || diagnostic != "" {
			t.Fatalf("empty %s plain = %d, %q, %q", name, code, plain, diagnostic)
		}
		code, jsonText, diagnostic := invoke(t, h, name, "--json")
		if code != 0 || diagnostic != "" {
			t.Fatalf("empty %s JSON = %d, %q", name, code, diagnostic)
		}
		golden(t, name+"-empty", jsonText)
	}
}

func seedMachine(t *testing.T, dir string) {
	t.Helper()
	st, err := statedb.Open(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("cli-seed-machine"))
	if err := statedb.InsertEnrollToken(t.Context(), st, statedb.EnrollToken{
		SecretHash: hash, MachineName: "laptop", ExpectedHostname: "laptop.lan", NotAfter: now.Add(time.Hour),
		Created: now, Principal: "local:seed", RequestID: "cli-seed-enroll", RequestHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = statedb.Enroll(t.Context(), st, statedb.NewMachine{
		Key:       statedb.EnrollKey{SecretHash: hash, InstallID: "cli-seed-install", CSRKeyHash: hash},
		MachineID: seed.MachineLaptop, CertDER: []byte("cli-seed-cert"), Serial: "cli-seed-serial",
		CertNotAfter: now.Add(time.Hour), ReportedHostname: "laptop.lan", Facts: seed.LaptopFacts, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverHash := sha256.Sum256([]byte("cli-seed-server"))
	if err := statedb.InsertEnrollToken(t.Context(), st, statedb.EnrollToken{
		SecretHash: serverHash, MachineName: "server", ExpectedHostname: "server.lan",
		NotAfter: now.Add(time.Hour), Created: now, Principal: "local:seed",
		RequestID: "cli-seed-server", RequestHash: serverHash,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = statedb.Enroll(t.Context(), st, statedb.NewMachine{
		Key:       statedb.EnrollKey{SecretHash: serverHash, InstallID: "cli-seed-server-install", CSRKeyHash: serverHash},
		MachineID: seed.MachineServer, CertDER: []byte("cli-seed-server-cert"), Serial: "cli-seed-server-serial",
		CertNotAfter: now.Add(time.Hour), ReportedHostname: "server.lan",
		Facts: `{"hostname":"server.lan"}`, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = statedb.Hello(t.Context(), st, statedb.HelloIn{
		MachineID:  seed.MachineLaptop,
		PeerSerial: "cli-seed-serial", LastEpoch: 0, BootID: "cli-seed-boot", InstallID: "cli-seed-install",
		Peer: "127.0.0.1:1234", Reason: statedb.ConnectStart, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(t.Context(), func(tx *sql.Tx) error {
		if _, err := statedb.ApplySnapshot(t.Context(), tx, statedb.SnapshotIn{
			MachineID: seed.MachineLaptop, Epoch: 1, Seq: 1, InstallID: "cli-seed-install",
			Facts: seed.LaptopFacts, Resources: `{"load1":0.5}`,
			Policy: statedb.Policy{Hash: "cli-seed", SchemaVersion: "1"}, Now: now,
		}); err != nil {
			return err
		}
		_, _, err := statedb.MarkOnline(t.Context(), tx, seed.MachineLaptop, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = statedb.CreateOwnerAccount(t.Context(), st, statedb.CreateAccount{
		ConfigWrite: statedb.ConfigWrite{
			Principal: "local:seed", RequestID: "cli-seed-account",
			RequestHash: hash, Actor: statedb.Actor{ID: "local:seed", Kind: "LOCAL_USER", Name: "seed"}, Now: now,
		},
		ID: "cli-account", Harness: "codex", Provider: "openai", DisplayName: "test account",
		Origin: statedb.AccountOriginOwner,
		Spec:   []byte(`{"displayName":"test account","mode":"ACCOUNT_MODE_INTERACTIVE_ONLY"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(t.Context(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(t.Context(), tx, statedb.ObservedRow{
			ID: "cli-goal", MachineID: seed.MachineLaptop,
			Spec:   `{"machine":{"value":"` + seed.MachineLaptop + `"},"harness":{"name":"codex"}}`,
			Status: `{"phase":"GOAL_PHASE_RUNNING"}`, Phase: "GOAL_PHASE_RUNNING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
			StartMs: now.UnixMilli(), ObservedMs: now.UnixMilli(), HubSeq: 10,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.UpsertWorkItemPage(t.Context(), st, "github-1", []statedb.WorkItemMirror{{
		Integration: "github-1", Kind: "WORK_ITEM_KIND_ISSUE", Project: "acme/web", ItemKey: "7398",
		ItemJSON: []byte(`{"title":"Fix\n\u001b[31m the parser"}`), UpdateMs: now.UnixMilli(), Stage: 2,
		TrustJSON: []byte(`{"trusted":true}`),
	}}, nil, nil, now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE work_items SET version = ? WHERE item_key = '7398'`, int64(9007199254740993))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func invoke(t *testing.T, h localHub, args ...string) (int, string, string) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	code := fleetcli.Run(t.Context(), args, fleetcli.Input{
		Stdout: &out, Stderr: &diagnostics,
		LookupEnv: func(name string) (string, bool) {
			if name == "FLEET_URL" {
				return h.url, true
			}
			return "", false
		},
	})
	return code, out.String(), diagnostics.String()
}

func TestReadsThroughTheRealDaemonTCPListener(t *testing.T) {
	h := startHub(t)
	for _, name := range []string{"status", "machines", "accounts", "goals", "work"} {
		code, out, diagnostic := invoke(t, h, name, "--json")
		if code != 0 {
			t.Fatalf("%s = %d: %s", name, code, diagnostic)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("%s JSON: %v", name, err)
		}
		if len(decoded) == 0 {
			t.Fatalf("%s empty response", name)
		}
		if name == "machines" && !strings.Contains(out, seed.MachineLaptop) {
			t.Fatal("seeded machine missing")
		}
		if name == "work" && !strings.Contains(out, `"9007199254740993"`) {
			t.Fatalf("uint64 was not a JSON string: %s", out)
		}
		golden(t, name, out)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"machine-show", []string{"machines", "show", seed.MachineLaptop, "--json"}},
		{"account-show", []string{"accounts", "show", "cli-account", "--json"}},
		{"goal-show", []string{"goals", "show", "cli-goal", "--json"}},
		{"work-show", []string{"work", "show", "--kind", "ISSUE", "--project", "acme/web", "--key", "7398", "--integration", "github-1", "--json"}},
	} {
		code, out, diagnostic := invoke(t, h, tc.args...)
		if code != 0 || !strings.Contains(out, "{") {
			t.Fatalf("show %v = %d, %q, %q", tc.args, code, out, diagnostic)
		}
		golden(t, tc.name, out)
	}
	code, out, diagnostic := invoke(t, h, "doctor", "--json")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "terminal") {
		t.Fatalf("nonterminal doctor = %d, %q, %q", code, out, diagnostic)
	}
	code, out, diagnostic = invoke(t, h, "work")
	if code != 0 || strings.Contains(out, "\x1b") || strings.Contains(out, "Fix\n") || !strings.Contains(out, `\n\x1b`) {
		t.Fatalf("plain work escaped cells = %d, %q, %q", code, out, diagnostic)
	}
}

func TestMachinePaginationOnTheRealDaemon(t *testing.T) {
	h := startHub(t)
	code, first, diagnostic := invoke(t, h, "machines", "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("page 1: %s", diagnostic)
	}
	var page struct {
		Machines      []json.RawMessage `json:"machines"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal([]byte(first), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Machines) != 1 || page.NextPageToken == "" {
		t.Fatalf("page 1 = %s", first)
	}
	code, second, diagnostic := invoke(t, h, "machines", "--page-size", "1", "--page-token", page.NextPageToken, "--json")
	if code != 0 {
		t.Fatalf("page 2: %s", diagnostic)
	}
	var last struct {
		Machines      []json.RawMessage `json:"machines"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal([]byte(second), &last); err != nil {
		t.Fatal(err)
	}
	if len(last.Machines) != 1 || last.NextPageToken != "" || bytes.Equal(last.Machines[0], page.Machines[0]) {
		t.Fatalf("page 2 = %s", second)
	}
	code, plain, diagnostic := invoke(t, h, "machines", "--page-size", "1")
	if code != 0 || !strings.Contains(plain, "Next page token: \"") {
		t.Fatalf("plain page = %d, %q, %q", code, plain, diagnostic)
	}
}

func TestServerFilterAndTokenReasonsStayVisible(t *testing.T) {
	h := startHub(t)
	for _, tc := range []struct {
		args   []string
		reason string
	}{
		{[]string{"machines", "--filter", "not_a_field = x"}, "REASON_CODE_FILTER_INVALID"},
		{[]string{"machines", "--order-by", "not_a_field"}, "REASON_CODE_ORDER_BY_INVALID"},
		{[]string{"machines", "--page-token", "wrong"}, "REASON_CODE_PAGE_TOKEN_INVALID"},
	} {
		code, out, diagnostic := invoke(t, h, tc.args...)
		if code != 1 || out != "" || !strings.Contains(diagnostic, tc.reason) {
			t.Fatalf("%v = %d, %q, %q", tc.args, code, out, diagnostic)
		}
	}
}

type watchedPrompt struct {
	mu    sync.Mutex
	b     bytes.Buffer
	ready chan struct{}
}

func (w *watchedPrompt) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.b.Write(p)
	if strings.Contains(w.b.String(), "Local approval secret:") {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (w *watchedPrompt) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }

func TestDoctorUsesTerminalSecretOnTheRealAdminRead(t *testing.T) {
	h := startHub(t)
	for _, entered := range []string{h.secret, strings.Repeat("0", 64)} {
		master, slave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		prompt := &watchedPrompt{ready: make(chan struct{}, 1)}
		var out bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- fleetcli.Run(t.Context(), []string{"doctor", "--json"}, fleetcli.Input{
				Stdin: slave, Stdout: &out, Stderr: prompt,
				LookupEnv: func(name string) (string, bool) {
					if name == "FLEET_URL" {
						return h.url, true
					}
					return "", false
				},
			})
		}()
		select {
		case <-prompt.ready:
		case <-time.After(4 * time.Second):
			t.Fatal("doctor did not prompt")
		}
		if _, err := master.Write([]byte(entered + "\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-done:
			if entered == h.secret && (code != 0 || !strings.Contains(out.String(), `"store"`)) {
				t.Fatalf("valid doctor = %d, output %q, diagnostic %q", code, out.String(), prompt.String())
			}
			if entered != h.secret && (code != 1 || out.Len() != 0) {
				t.Fatalf("wrong secret doctor = %d, output %q", code, out.String())
			}
			if entered != h.secret && !strings.Contains(prompt.String(), "REASON_CODE_CREDENTIALS_INVALID") {
				t.Fatalf("wrong secret reason = %q", prompt.String())
			}
			if entered == h.secret {
				golden(t, "doctor", out.String())
			}
		case <-time.After(4 * time.Second):
			t.Fatal("doctor did not finish")
		}
		if strings.Contains(prompt.String(), entered) {
			t.Fatal("secret leaked to stderr")
		}
		_ = master.Close()
		_ = slave.Close()
	}
}

// Golden files preserve the complete public response shape. Only runtime
// timestamps, random hub IDs, host disk byte counts and temporary paths are normalized.
func golden(t *testing.T, name, raw string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		t.Fatal(err)
	}
	normalize(value)
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pretty = append(pretty, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if os.Getenv("FLEET_CLI_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: committed golden fixture path from literal test names, no external input
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty, want) {
		t.Fatalf("%s golden changed; got:\n%s", name, pretty)
	}
}

func normalize(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			switch {
			case key == "fleetId" || key == "hubInstanceId":
				v[key] = "<hub-id>"
			case key == "token" || key == "caFingerprint" || key == "enrollCommand":
				v[key] = "<secret>"
			case strings.HasSuffix(key, "Time"):
				v[key] = "<time>"
			case strings.HasSuffix(key, "Bytes"):
				v[key] = "<bytes>"
			case key == "lastBackupPath":
				if path, ok := item.(string); ok {
					v[key] = "<state>/backups/" + filepath.Base(path)
				}
			default:
				normalize(item)
			}
		}
	case []any:
		for _, item := range v {
			normalize(item)
		}
	}
}
