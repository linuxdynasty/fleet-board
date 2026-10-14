package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func syncTestStore(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := statedb.Create(context.Background(), dir, statedb.Identity{
		FleetID: "fleet-test", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("skey"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "epoch",
		CreatedTime: time.Now(),
	}, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func syncTestKey(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "github-app.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSyncStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := statedb.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testTelemetry(t *testing.T) *telemetry.Telemetry {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel
}

func TestGitHubSyncerBuildsFromKeys(t *testing.T) {
	t.Parallel()
	dir := syncTestStore(t)
	path := syncTestKey(t, dir, 0o600)
	st := openSyncStore(t, dir)

	sy, err := githubSyncer(context.Background(), &config.Config{
		GitHub: &config.GitHub{AppID: 1, InstallationID: 2, PrivateKeyFile: path},
	}, st, testTelemetry(t), time.Now())
	if err != nil {
		t.Fatalf("githubSyncer: %v", err)
	}
	if sy == nil {
		t.Fatal("configured github keys built no syncer")
	}
	row, err := statedb.GetIntegration(context.Background(), st, GitHubIntegrationID)
	if err != nil {
		t.Fatalf("integration row: %v", err)
	}
	if row.BaseURL != "https://github.com" {
		t.Errorf("base url = %q, want the API host", row.BaseURL)
	}
}

func TestGitHubSyncerNeedsKeys(t *testing.T) {
	t.Parallel()
	sy, err := githubSyncer(context.Background(), &config.Config{}, nil, testTelemetry(t), time.Now())
	if err != nil || sy != nil {
		t.Errorf("unconfigured = %v, %v; want nil, nil", sy, err)
	}
}

func TestGitHubSyncerRefusesALooseKey(t *testing.T) {
	t.Parallel()
	dir := syncTestStore(t)
	path := syncTestKey(t, dir, 0o644)
	st := openSyncStore(t, dir)

	if _, err := githubSyncer(context.Background(), &config.Config{
		GitHub: &config.GitHub{AppID: 1, InstallationID: 2, PrivateKeyFile: path},
	}, st, testTelemetry(t), time.Now()); err == nil {
		t.Error("loose key succeeded, want a refusal")
	}
	if _, err := githubSyncer(context.Background(), &config.Config{
		GitHub: &config.GitHub{AppID: 1, InstallationID: 2, PrivateKeyFile: filepath.Join(dir, "missing.pem")},
	}, st, testTelemetry(t), time.Now()); err == nil {
		t.Error("missing key succeeded, want a refusal")
	}
}
