package api

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	"github.com/linuxdynasty/fleet-board/web"
)

// NewLocalServer serves the local-trusted FleetService and metrics on the
// API listener, plus the embedded dashboard on every other GET path (02j
// "Listeners"). The Fleet instance is the same one served by admin.sock.
func NewLocalServer(tel *telemetry.Telemetry, port int, fleet *Fleet, quotas *Quotas) (*http.Server, error) {
	if tel == nil || fleet == nil || quotas == nil || !fleet.localTrusted {
		return nil, errors.New("api: local server needs telemetry, quotas and a local-trusted Fleet")
	}
	content, err := web.Dist()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", tel.MetricsHandler(nil))
	mux.Handle(fleet.Handler())
	// "/" also matches other methods; the dashboard refuses those itself.
	mux.Handle("/", web.Handler(content))
	return localServer(tel, "api", port, mux, quotas, true), nil
}

// NewMetricsServer keeps FleetService off the optional metrics listener. It
// shares the API listener's per-peer metrics allowance.
func NewMetricsServer(tel *telemetry.Telemetry, port int, quotas *Quotas) (*http.Server, error) {
	if tel == nil || quotas == nil {
		return nil, errors.New("api: metrics server needs telemetry and quotas")
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", tel.MetricsHandler(nil))
	return localServer(tel, "metrics", port, mux, quotas, false), nil
}

func localServer(tel *telemetry.Telemetry, listener string, port int, mux *http.ServeMux,
	quotas *Quotas, fleet bool,
) *http.Server {
	errs := connect.NewErrorWriter()
	admit := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := localPeer(r.RemoteAddr)
		if !ok {
			refuse(w, r, tel, errs, telemetry.ReasonHost, typev1.ReasonCode_REASON_CODE_HOST_NOT_ALLOWED,
				"local API needs a loopback peer")
			return
		}
		if suppliedCredential(r.Header) {
			tel.CountAuthFailure(r.Context(), telemetry.ReasonCredentials)
			_ = errs.Write(w, r, apiError(connect.CodeUnauthenticated,
				typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "credentials are not available in local mode", nil, nil))
			return
		}
		if r.URL.Path == "/metrics" {
			if err := quotas.take(r.Context(), peer, quotaMetrics); err != nil {
				_ = errs.Write(w, r, err)
				return
			}
		} else if fleet && strings.HasPrefix(r.URL.Path, "/fleet.hub.v1.FleetService/") {
			if r.Method == http.MethodPost && !connectProtocol(r.Header) {
				tel.CountAuthFailure(r.Context(), telemetry.ReasonProtocol)
				_ = errs.Write(w, r, apiError(connect.CodeInvalidArgument,
					typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "Connect protocol header is required", nil, nil))
				return
			}
			if localModeRPC(r.URL.Path) {
				_ = errs.Write(w, r, apiError(connect.CodeFailedPrecondition,
					typev1.ReasonCode_REASON_CODE_SIGN_IN_METHOD_DISABLED,
					"sign-in and devices are unavailable in local mode", nil, nil))
				return
			}
			r = r.WithContext(withQuota(withLocalOwner(r.Context()), quotas, peer))
		}
		mux.ServeHTTP(w, r)
	})
	h := &guard{
		next: admit, tel: tel, hosts: allowed(port, ""),
		origins: allowed(port, "http://"), errors: errs,
	}
	return &http.Server{
		Handler: h, ReadHeaderTimeout: ReadHeaderTimeout,
		MaxHeaderBytes: 1 << 20, // bounds raw Cookie scanning without treating unrelated cookies as credentials
		IdleTimeout:    IdleTimeout, ErrorLog: tel.HTTPErrorLog(listener),
	}
}

// localModeRPC is the auth.proto and device.proto surface. Token and
// notification RPCs have no v1 implementation and remain UNIMPLEMENTED.
func localModeRPC(path string) bool {
	const prefix = "/fleet.hub.v1.FleetService/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	switch strings.TrimPrefix(path, prefix) {
	case "RedeemInvite", "BeginPasskeyRegistration", "FinishPasskeyRegistration",
		"BeginPasskeyLogin", "FinishPasskeyLogin", "DeletePasskey", "SignOut",
		"ListLoginSessions", "RevokeLoginSession", "ListUsers", "CreateUserInvite", "UpdateUser",
		"RequestPairing", "ApprovePairing", "RevokeDevice", "ListDevices", "GetDevice":
		return true
	default:
		return false
	}
}

func localPeer(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(host)
	return ip.Unmap(), err == nil && ip.Unmap().IsLoopback()
}

// suppliedCredential checks presence, including empty and repeated carriers.
// Cookie names are examined as raw segments bounded by MaxHeaderBytes so
// malformed cookie pairs cannot be dropped by net/http's Cookie parser.
func suppliedCredential(h http.Header) bool {
	const session = "__Host-fleet_session"
	for name, values := range h {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
		if !strings.EqualFold(name, "Cookie") {
			continue
		}
		for _, value := range values {
			for part := range strings.SplitSeq(value, ";") {
				name, _, _ := strings.Cut(part, "=")
				if strings.TrimSpace(name) == session {
					return true
				}
			}
		}
	}
	return false
}

func connectProtocol(h http.Header) bool {
	if len(h.Values("Connect-Protocol-Version")) == 1 && h.Get("Connect-Protocol-Version") == "1" {
		return true
	}
	for _, value := range h.Values("Content-Type") {
		if strings.HasPrefix(strings.ToLower(value), "application/grpc") {
			return true
		}
	}
	return false
}
