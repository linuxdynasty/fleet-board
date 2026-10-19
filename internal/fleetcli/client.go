package fleetcli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// newClient refuses every address the local_trusted hub cannot admit. The
// transport dials loopback literals even for localhost, so local DNS and proxy
// settings cannot redirect a secret-bearing doctor request.
func newClient(raw string) (hubv1connect.FleetServiceClient, func(), error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		u.RawPath != "" || strings.Contains(raw, "#") || strings.Contains(raw, "?") ||
		strings.Contains(u.Host, "%") {
		return nil, nil, errUsage
	}
	host := u.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip, parseErr := netip.ParseAddr(host)
		if parseErr != nil || !ip.IsLoopback() || ip.Zone() != "" {
			return nil, nil, errUsage
		}
	}
	port := u.Port()
	if port != "" {
		n, parseErr := strconv.Atoi(port)
		if parseErr != nil || n < 1 || n > 65535 {
			return nil, nil, errUsage
		}
	} else {
		port = "80"
	}
	if u.Host == "" || strings.Contains(u.Host, "@") || strings.Contains(u.Host, "\\") ||
		strings.Contains(raw, "%") || strings.Contains(raw, "//localhost.") {
		return nil, nil, errUsage
	}
	u.Path = ""
	u.RawPath = ""
	tr := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 1 << 20,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var targets []string
		if strings.EqualFold(host, "localhost") {
			targets = []string{"127.0.0.1", "::1"}
		} else {
			targets = []string{host}
		}
		var last error
		for _, target := range targets {
			var d net.Dialer
			conn, dialErr := d.DialContext(ctx, "tcp", net.JoinHostPort(target, port))
			if dialErr == nil {
				return conn, nil
			}
			last = dialErr
		}
		return nil, last
	}
	httpClient := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirect refused")
	}}
	client := hubv1connect.NewFleetServiceClient(httpClient, u.String(), connect.WithReadMaxBytes(1<<20))
	return client, tr.CloseIdleConnections, nil
}

func safeError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		answer := "hub returned " + ce.Code().String()
		for _, detail := range ce.Details() {
			value, valueErr := detail.Value()
			if valueErr != nil {
				continue
			}
			d, ok := value.(*typev1.ErrorDetail)
			if !ok {
				continue
			}
			if _, known := typev1.ReasonCode_name[int32(d.GetReason())]; known {
				answer += " (" + d.GetReason().String() + ")"
			}
			break
		}
		return answer
	}
	if errors.Is(err, errNotTerminal) {
		return "doctor needs a terminal for its approval secret"
	}
	if errors.Is(err, errUnsupportedMode) {
		return "this build needs a local_trusted hub"
	}
	if errors.Is(err, errInvalidSecret) {
		return "enter the 64-character approval secret shown by fleetd init"
	}
	if errors.Is(err, errMalformed) {
		return "hub returned an incomplete response"
	}
	if errors.Is(err, errOutput) {
		return "could not write output"
	}
	return "request failed"
}
