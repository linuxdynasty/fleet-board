package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/setupgithub"
)

// setupCommand is `fleetd setup github` (02j "fleetd setup github"):
// it binds a one-shot listener on 127.0.0.1, walks the owner through
// the GitHub App manifest flow, writes github-app.pem and
// github-client-secret next to fleetd.yaml (mode 0600, never logged)
// and adds the github keys. It prints "restart fleetd to apply": the
// file is read at start.
func setupCommand(options func() (daemon.Options, error), stdout, stderr io.Writer) *cobra.Command {
	var appName, apiBase, webBase, org string
	var installationID int64
	var timeout time.Duration
	setup := &cobra.Command{
		Use:   "setup",
		Short: "One-time setup commands for the hub",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	github := &cobra.Command{
		Use:   "github",
		Short: "Connect GitHub through the App manifest flow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options()
			if err != nil {
				return commandError{err}
			}
			req := setupRequest{
				ConfigPath:     opts.ConfigPath,
				AppName:        appName,
				APIBase:        apiBase,
				WebBase:        webBase,
				Org:            org,
				InstallationID: installationID,
				Timeout:        timeout,
			}
			if err := runSetupGithub(cmd.Context(), req, stdout, stderr); err != nil {
				return commandError{err}
			}
			return nil
		},
	}
	f := github.Flags()
	f.StringVar(&appName, "app-name", "fleet-board", "name for the GitHub App (the owner can rename it on GitHub)")
	f.StringVar(&apiBase, "api-base", "https://api.github.com", "GitHub API base (tests point it at a recorder)")
	f.StringVar(&webBase, "github-base", "https://github.com", "GitHub web base the manifest is posted to")
	f.Int64Var(&installationID, "installation-id", 0, "use this App installation instead of asking (re-runs)")
	f.StringVar(&org, "org", "", "create the GitHub App under this organization (default: your personal account)")
	f.DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the GitHub redirect")
	setup.AddCommand(github)
	return setup
}

type setupRequest struct {
	ConfigPath     string
	AppName        string
	APIBase        string
	WebBase        string
	Org            string
	InstallationID int64
	Timeout        time.Duration
}

func runSetupGithub(ctx context.Context, req setupRequest, stdout, stderr io.Writer) error {
	if req.InstallationID < 0 {
		return fmt.Errorf("github setup: --installation-id %d is not a valid installation (refused)", req.InstallationID)
	}
	if !validOrg(req.Org) {
		return fmt.Errorf("github setup: --org %q is not a valid organization (refused)", req.Org)
	}
	// A re-run finds the saved App from the first run and only lists,
	// selects and saves the installation: no browser, no new exchange.
	// The secret files already exist, so the first-run path below
	// (WriteSecrets with O_EXCL) cannot serve a re-run.
	if saved, err := setupgithub.LoadSavedApp(req.ConfigPath); err == nil {
		return runSetupGithubRerun(ctx, req, saved, stdout, stderr)
	}
	state, err := setupgithub.NewState()
	if err != nil {
		return err
	}
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("github setup: listen: %w", err)
	}
	local := "http://" + l.Addr().String()
	if _, err := fmt.Fprintf(stderr, "fleetd: open %s to connect GitHub (waiting %s)\n", local, req.Timeout.Round(time.Second)); err != nil {
		_ = l.Close()
		return err
	}
	manifest := setupgithub.Manifest(req.AppName, local, local+"/callback")
	page, err := setupPage(req.WebBase, state, manifest, req.Org)
	if err != nil {
		return err
	}
	code, gotState := serveOnce(ctx, l, page, req.Timeout)
	if ctx.Err() != nil {
		return fmt.Errorf("github setup: %w", ctx.Err())
	}
	if code == "" {
		return fmt.Errorf("github setup: no redirect arrived (refused)")
	}
	if err := setupgithub.CheckState(gotState, state); err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	res, err := setupgithub.Exchange(ctx, httpClient, req.APIBase, code)
	if err != nil {
		return err
	}
	dir := filepath.Dir(req.ConfigPath)
	pemPath, secretPath, err := setupgithub.WriteSecrets(dir, res.PrivateKey, res.ClientSecret)
	if err != nil {
		return err
	}
	id := req.InstallationID
	if id == 0 {
		appClient, err := appClient(res.AppID, []byte(res.PrivateKey))
		if err != nil {
			return err
		}
		installs, err := setupgithub.ListInstallations(ctx, appClient, req.APIBase)
		if err != nil {
			return err
		}
		switch len(installs) {
		case 0:
			id = 0
			_, _ = fmt.Fprintln(stderr, "fleetd: install the App on the watched repos, "+
				"then re-run: fleetd setup github --installation-id ID")
		case 1:
			id = installs[0].ID
		default:
			_, _ = fmt.Fprintln(stderr, "fleetd: several installations found; re-run with one of:")
			for _, in := range installs {
				_, _ = fmt.Fprintf(stderr, "fleetd:   --installation-id %d (%s)\n", in.ID, in.Account)
			}
		}
	}
	if err := setupgithub.UpdateConfig(req.ConfigPath, res.AppID, pemPath,
		res.ClientID, secretPath, id); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "restart fleetd to apply")
	return err
}

// runSetupGithubRerun is 02j step 5 on its own: the App already exists,
// so it signs an App token with the saved key, lists the installations
// and saves the choice. It never touches the browser flow or the secret
// files, which already exist.
func runSetupGithubRerun(ctx context.Context, req setupRequest, saved setupgithub.SavedApp, stdout, stderr io.Writer) error {
	appClient, err := appClient(saved.AppID, []byte(saved.PrivateKey))
	if err != nil {
		return err
	}
	installs, err := setupgithub.ListInstallations(ctx, appClient, req.APIBase)
	if err != nil {
		return err
	}
	id := req.InstallationID
	if id != 0 {
		found := false
		for _, in := range installs {
			if in.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("github setup: installation %d not found (refused)", id)
		}
	} else {
		switch len(installs) {
		case 0:
			_, _ = fmt.Fprintln(stderr, "fleetd: install the App on the watched repos, "+
				"then re-run: fleetd setup github --installation-id ID")
		case 1:
			id = installs[0].ID
		default:
			_, _ = fmt.Fprintln(stderr, "fleetd: several installations found; re-run with one of:")
			for _, in := range installs {
				_, _ = fmt.Fprintf(stderr, "fleetd:   --installation-id %d (%s)\n", in.ID, in.Account)
			}
		}
	}
	if err := setupgithub.UpdateConfig(req.ConfigPath, saved.AppID, saved.PEMPath,
		saved.ClientID, saved.SecretPath, id); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "restart fleetd to apply")
	return err
}

// appClient authenticates as the App (JWT over the private key) for the
// installation list (02j step 5). The key never leaves this transport.
func appClient(appID int64, privateKey []byte) (*http.Client, error) {
	tr, err := ghinstallation.NewAppsTransport(http.DefaultTransport, appID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("github setup: app transport: %w", err)
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, nil
}

// setupCallback is the one GitHub redirect the listener waits for.
type setupCallback struct{ code, state string }

// connectedPage answers the GitHub redirect: a 200 page, not a redirect,
// because the listener closes as soon as the one callback arrives, so a
// redirect target would race the teardown and the owner would see a
// failed page.
const connectedPage = "<!doctype html><html><body><h1>Connected</h1>" +
	"<p>Return to the terminal: fleetd is finishing the setup.</p></body></html>"

// callbackHandler answers the redirect and then tells the waiter it
// arrived. The page is written and flushed first: the waiter shuts the
// server down as soon as it hears, which would otherwise close the
// connection while this handler is still writing.
func callbackHandler(got chan<- setupCallback, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case got <- setupCallback{q.Get("code"), q.Get("state")}:
		default:
		}
	}
}

// serveOnce serves the setup page until the one GitHub redirect
// arrives, answers it with a confirmation page, then nothing: the
// listener shuts down gracefully, after the page is fully written.
func serveOnce(ctx context.Context, l net.Listener, page string, timeout time.Duration) (code, state string) {
	got := make(chan setupCallback, 1)
	mux := &http.ServeMux{}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
	// The answer is a 200 page, not a redirect: the listener closes as
	// soon as the one callback arrives, so a redirect target would race
	// the teardown and the owner would see a failed page.
	mux.HandleFunc("/callback", callbackHandler(got, connectedPage))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan struct{})
	go func() { _ = srv.Serve(l); close(serveDone) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		<-serveDone
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case cb := <-got:
		return cb.code, cb.state
	case <-deadline.C:
		return "", ""
	case <-ctx.Done():
		return "", ""
	}
}

// setupPage renders the one-shot page: it posts the manifest to GitHub,
// whose confirm screen redirects back to the listener (02j steps 1-2).
// An empty org posts to the personal-account endpoint; an organization
// name posts to that organization's endpoint, because a private App can
// only be installed on its owning account.
func setupPage(webBase, state string, manifest setupgithub.ManifestDoc, org string) (string, error) {
	if !validOrg(org) {
		return "", fmt.Errorf("github setup: --org %q is not a valid organization (refused)", org)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("github setup: render manifest: %w", err)
	}
	endpoint := "/settings/apps/new?state=" + state
	if org != "" {
		endpoint = "/organizations/" + org + "/settings/apps/new?state=" + state
	}
	action := html.EscapeString(strings.TrimSuffix(webBase, "/") + endpoint)
	return "<!doctype html><html><body>" +
		"<h1>Connect GitHub</h1>" +
		"<p>Send the manifest to GitHub, confirm the App there, and GitHub " +
		"redirects back here with a one-time code.</p>" +
		"<form action=\"" + action + "\" method=\"post\">" +
		"<input type=\"hidden\" name=\"manifest\" value=\"" + html.EscapeString(string(raw)) + "\">" +
		"<button type=\"submit\">Create GitHub App</button>" +
		"</form></body></html>", nil
}

// validOrg bounds the --org flag before it reaches the setup page: an
// empty org means the owner's personal account, anything else must look
// like a GitHub login so it cannot break out of the form action URL.
func validOrg(org string) bool {
	if org == "" {
		return true
	}
	if len(org) > 39 {
		return false
	}
	for _, r := range org {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
