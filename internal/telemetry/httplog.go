package telemetry

import (
	"bytes"
	"log"
)

// HTTPErrorKind is the closed set of kinds in an http.server_error line.
type HTTPErrorKind string

const (
	HTTPErrorTLSHandshake HTTPErrorKind = "tls_handshake"
	HTTPErrorPanic        HTTPErrorKind = "panic"
	HTTPErrorOther        HTTPErrorKind = "other"
)

// HTTPErrorLog is an http.Server ErrorLog. Each line net/http writes (a
// failed TLS handshake, a broken connection, a handler panic) becomes one
// JSON event, http.server_error, with the listener name and a closed kind:
// a recovered handler panic at error level, because it is a fault in fleetd,
// everything else at debug level, because a peer causes it. The line itself
// is dropped: it names the peer and can echo bytes the peer sent. Without it
// net/http prints plain text to stderr.
func (t *Telemetry) HTTPErrorLog(listener string) *log.Logger {
	return log.New(httpErrorWriter{t: t, listener: listener}, "", 0)
}

type httpErrorWriter struct {
	t        *Telemetry
	listener string
}

func (w httpErrorWriter) Write(p []byte) (int, error) {
	// Match the start only: later in a line net/http may quote bytes the peer
	// sent, so a peer could otherwise pick the kind and the level.
	switch {
	case bytes.HasPrefix(p, []byte("http: panic serving ")):
		w.t.Logger.Error("http.server_error", "listener", w.listener, "kind", string(HTTPErrorPanic))
	case bytes.HasPrefix(p, []byte("http: TLS handshake error ")):
		w.t.Logger.Debug("http.server_error", "listener", w.listener, "kind", string(HTTPErrorTLSHandshake))
	default:
		w.t.Logger.Debug("http.server_error", "listener", w.listener, "kind", string(HTTPErrorOther))
	}
	return len(p), nil
}
