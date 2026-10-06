package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Handler is the whole HTTP surface of a local_trusted API (or metrics)
// listener bound to port: the 02f Host and Origin checks before anything
// else, then GET /metrics from tel, and 404 for every other path. It sends no
// CORS header.
func Handler(tel *telemetry.Telemetry, port int) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", tel.MetricsHandler(nil))
	return &guard{
		next:    mux,
		tel:     tel,
		hosts:   allowed(port, ""),
		origins: allowed(port, "http://"),
		errors:  connect.NewErrorWriter(),
	}
}

// NewServer is the server for an API or metrics listener named listener and
// bound to port: [Handler], header and idle timeouts, and net/http's own
// error lines sent to tel as http.server_error events; tel must not be nil.
func NewServer(tel *telemetry.Telemetry, listener string, port int) *http.Server {
	return &http.Server{
		Handler:           Handler(tel, port),
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		ErrorLog:          tel.HTTPErrorLog(listener),
	}
}

// ReadHeaderTimeout bounds how long a client may take to send request
// headers. There is no ReadTimeout: Watch and attach are long streams.
const ReadHeaderTimeout = 10 * time.Second

// IdleTimeout closes a keep-alive connection with no request for this long.
const IdleTimeout = 2 * time.Minute

// allowed lists the loopback names with port, behind prefix. Port 80 also
// allows the bare name, since clients leave the default port out.
func allowed(port int, prefix string) map[string]bool {
	p := ":" + strconv.Itoa(port)
	set := map[string]bool{}
	for _, name := range []string{"localhost", "127.0.0.1", "[::1]"} {
		set[prefix+name+p] = true
		if port == 80 {
			set[prefix+name] = true
		}
	}
	return set
}

type guard struct {
	next    http.Handler
	tel     *telemetry.Telemetry
	hosts   map[string]bool
	origins map[string]bool
	errors  *connect.ErrorWriter
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.hosts[strings.ToLower(r.Host)] {
		refuse(w, r, g.tel, g.errors, telemetry.ReasonHost, typev1.ReasonCode_REASON_CODE_HOST_NOT_ALLOWED, "Host not allowed")
		return
	}
	if values, sent := r.Header["Origin"]; sent && (len(values) != 1 || !g.origins[strings.ToLower(values[0])]) {
		refuse(w, r, g.tel, g.errors, telemetry.ReasonOrigin, typev1.ReasonCode_REASON_CODE_ORIGIN_NOT_ALLOWED, "Origin not allowed")
		return
	}
	g.next.ServeHTTP(w, r)
}

// refuse writes the 02f refusal: a Connect permission_denied error (HTTP 403)
// whose ErrorDetail names the reason, and counts it.
func refuse(w http.ResponseWriter, r *http.Request, tel *telemetry.Telemetry, errs *connect.ErrorWriter,
	why telemetry.AuthReason, code typev1.ReasonCode, msg string,
) {
	tel.CountAuthFailure(r.Context(), why)
	_ = errs.Write(w, r, apiError(connect.CodePermissionDenied, code, msg, nil, nil)) // the client hung up; nothing more to tell it
}
