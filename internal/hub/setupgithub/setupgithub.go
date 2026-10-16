// Package setupgithub is `fleetd setup github` without the terminal:
// the GitHub App manifest, the one-time code exchange, the secret files
// and the fleetd.yaml update (02j "fleetd setup github", ADR 0022
// decision 4). The cobra wiring in cmd/fleetd owns the listener and the
// prompts; this package owns everything a test can observe without a
// browser: no listener, no stdout, no secret in any error or log.
package setupgithub

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// ManifestDoc is the GitHub App manifest the setup page posts. The
// permission set is ADR 0022 decision 4: read-only repository
// permissions, no webhook, no account permissions; user authorization
// (client id/secret) only arrives when sign-in is asked for, never in v1.
type ManifestDoc struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	RedirectURL        string            `json:"redirect_url"`
	Public             bool              `json:"public"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// Manifest builds the manifest for the one-shot listener: url and
// redirectURL both point at it (02j steps 1-2).
func Manifest(name, url, redirectURL string) ManifestDoc {
	return ManifestDoc{
		Name:          name,
		URL:           url,
		RedirectURL:   redirectURL,
		Public:        false,
		DefaultEvents: []string{},
		DefaultPermissions: map[string]string{
			"metadata":      "read",
			"issues":        "read",
			"pull_requests": "read",
			"checks":        "read",
			"statuses":      "read",
			"contents":      "read",
		},
	}
}

// NewState makes the one-time callback secret (02j step 2). A wrong
// state is refused by [CheckState].
func NewState() (string, error) {
	var raw [24]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fmt.Errorf("github setup: new state: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// CheckState refuses a callback whose state is not the one the command
// generated (02j step 2). The comparison runs in constant time.
func CheckState(got, want string) error {
	if want == "" {
		return fmt.Errorf("github setup: no state was generated")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("github setup: state mismatch (refused)")
	}
	return nil
}

// CodeError is a refused conversion code: used, unknown or expired (02j
// step 3). It carries the HTTP status only, never the code or the body.
type CodeError struct {
	Status int
}

func (e *CodeError) Error() string {
	if e.Status == 0 {
		return "github setup: empty conversion code (refused)"
	}
	return fmt.Sprintf("github setup: conversion refused (HTTP %d: used or unknown code)", e.Status)
}

// Result is the exchanged App identity (02j step 3). ClientSecret is
// empty unless sign-in was asked for.
type Result struct {
	AppID        int64
	PrivateKey   string
	ClientID     string
	ClientSecret string
}

// Exchange swaps the one-time code once
// (POST /app-manifests/{code}/conversions, 02j step 3). A non-2xx
// answer is a *[CodeError]; the caller writes files only on success,
// so a used code writes nothing.
func Exchange(ctx context.Context, client *http.Client, apiBase, code string) (Result, error) {
	if !validCode(code) {
		return Result{}, &CodeError{}
	}
	base := strings.TrimSuffix(apiBase, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/app-manifests/"+code+"/conversions", nil)
	if err != nil {
		// The URL carries the one-time code, so the raw error (which
		// echoes the URL) must never leave this function.
		return Result{}, fmt.Errorf("github setup: exchange request (refused)")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		// client.Do returns a url.Error holding the full request URL,
		// whose path carries the still-valid one-time code: name the
		// step only, never the wrapped error.
		return Result{}, fmt.Errorf("github setup: exchange (refused)")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("github setup: exchange body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, &CodeError{Status: resp.StatusCode}
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		return Result{}, fmt.Errorf("github setup: exchange answer: %w", err)
	}
	id, _ := wire["id"].(float64)
	pem, _ := wire["pem"].(string)
	clientID, _ := wire["client_id"].(string)
	secret, _ := wire["client_secret"].(string)
	if id <= 0 || pem == "" || !utf8.ValidString(pem) || !utf8.ValidString(secret) {
		return Result{}, fmt.Errorf("github setup: exchange answer lacks the app id or key (refused)")
	}
	return Result{AppID: int64(id), PrivateKey: pem, ClientID: clientID, ClientSecret: secret}, nil
}

// validCode bounds the redirect's code before it reaches a URL: the
// value is untrusted redirect input, not a secret.
func validCode(code string) bool {
	if len(code) == 0 || len(code) > 256 {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// Installation is one row of GET /app/installations (02j step 5).
type Installation struct {
	ID      int64
	Account string
}

// ListInstallations returns the installs the owner confirmed on GitHub
// (02j step 5): none means install the App first, several means ask
// which. The caller authenticates as the App; tests pass a fixture
// server's client. Account logins are untrusted text, capped here.
func ListInstallations(ctx context.Context, client *http.Client, apiBase string) ([]Installation, error) {
	base := strings.TrimSuffix(apiBase, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/app/installations", nil)
	if err != nil {
		return nil, fmt.Errorf("github setup: list installations request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github setup: list installations: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("github setup: list installations: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("github setup: installations body: %w", err)
	}
	var wire []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("github setup: installations answer: %w", err)
	}
	out := make([]Installation, 0, len(wire))
	for _, row := range wire {
		if row.ID <= 0 {
			continue
		}
		out = append(out, Installation{ID: row.ID, Account: cleanLogin(row.Account.Login)})
	}
	return out, nil
}

// cleanLogin caps an account login at the boundary: at most 255
// characters, no control characters.
func cleanLogin(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 255 {
		s = s[:255]
	}
	if !utf8.ValidString(s) {
		return ""
	}
	return s
}

// SavedApp is the App identity a first run stored: the re-run path
// ([LoadSavedApp]) lists installations with it instead of exchanging a
// new code.
type SavedApp struct {
	AppID      int64
	PrivateKey string
	ClientID   string
	PEMPath    string
	SecretPath string
}

// LoadSavedApp returns the App identity a first run wrote to the config
// and the secret files. Errors name the step only, never the path: the
// caller prints them to stderr.
func LoadSavedApp(configPath string) (SavedApp, error) {
	raw, err := os.ReadFile(configPath) //nolint:gosec // G304: path is the operator's own config path; reading it is the job
	if err != nil {
		return SavedApp{}, fmt.Errorf("github setup: no saved app (refused)")
	}
	cfg, err := config.Parse(raw)
	if err != nil || cfg.GitHub == nil || cfg.GitHub.AppID <= 0 ||
		cfg.GitHub.PrivateKeyFile == "" || cfg.GitHub.ClientSecretFile == "" {
		return SavedApp{}, fmt.Errorf("github setup: no saved app (refused)")
	}
	key, err := os.ReadFile(cfg.GitHub.PrivateKeyFile) //nolint:gosec // G304: path comes from the operator's own config file
	if err != nil || len(key) == 0 {
		return SavedApp{}, fmt.Errorf("github setup: saved key (refused)")
	}
	return SavedApp{
		AppID:      cfg.GitHub.AppID,
		PrivateKey: string(key),
		ClientID:   cfg.GitHub.ClientID,
		PEMPath:    cfg.GitHub.PrivateKeyFile,
		SecretPath: cfg.GitHub.ClientSecretFile,
	}, nil
}

// WriteSecrets stores the exchanged key material next to fleetd.yaml
// (02j step 4): github-app.pem and github-client-secret, each mode
// 0600, created exclusively, never logged. An empty client secret (no
// sign-in) still gets its file, so the config shape never depends on
// which flow the owner picked.
func WriteSecrets(dir, privateKey, clientSecret string) (pemPath, secretPath string, err error) {
	if privateKey == "" {
		return "", "", fmt.Errorf("github setup: no private key to write")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("github setup: config dir (refused)")
	}
	pemPath = filepath.Join(dir, "github-app.pem")
	if err := writeExclusive(pemPath, []byte(privateKey)); err != nil {
		return "", "", err
	}
	secretPath = filepath.Join(dir, "github-client-secret")
	if err := writeExclusive(secretPath, []byte(clientSecret)); err != nil {
		_ = os.Remove(pemPath)
		return "", "", err
	}
	return pemPath, secretPath, nil
}

func writeExclusive(path string, body []byte) error {
	// The os error keeps the full file path, which the command prints
	// to stderr: failures name the step only, never the wrapped error.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is under the operator's own config dir; the name is fixed
	if err != nil {
		return fmt.Errorf("github setup: write file (refused)")
	}
	_, werr := f.Write(body)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("github setup: write file (refused)")
	}
	return nil
}

// UpdateConfig adds the exchanged identity to the github section of
// fleetd.yaml (02j step 4): app_id, private_key_file, client_id,
// client_secret_file and installation_id. Nothing secret goes into
// state.db. The file keeps mode 0600; a failed write leaves the old
// file in place.
func UpdateConfig(configPath string, appID int64, pemPath, clientID, secretPath string, installationID int64) error {
	raw, err := os.ReadFile(configPath) //nolint:gosec // G304: path is the operator's own config path (--config, $FLEETD_CONFIG or the default)
	if err != nil {
		return fmt.Errorf("github setup: read config (refused)")
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return fmt.Errorf("github setup: %w", err)
	}
	cfg.GitHub = &config.GitHub{
		AppID:            appID,
		InstallationID:   installationID,
		PrivateKeyFile:   pemPath,
		ClientID:         clientID,
		ClientSecretFile: secretPath,
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("github setup: render config: %w", err)
	}
	if err := writeAtomic0600(configPath, body); err != nil {
		return err
	}
	return nil
}

func writeAtomic0600(path string, body []byte) error {
	// The os errors keep the full file path, which the command prints
	// to stderr: failures name the step only, never the wrapped error.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fleetd.yaml.tmp.*")
	if err != nil {
		return fmt.Errorf("github setup: stage config (refused)")
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(body)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("github setup: stage config (refused)")
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("github setup: stage config (refused)")
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("github setup: replace config (refused)")
	}
	return nil
}
