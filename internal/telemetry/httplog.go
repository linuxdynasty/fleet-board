package telemetry

import (
	"bytes"
	"log"
)

// HTTPErrorKind is the closed set of kinds in an http.server_error line.
type HTTPErrorKind string

const (
	HTTPErrorTLSHandshake HTTPErrorKind = "tls_handshake"
	HTTPErrorOther        HTTPErrorKind = "other"
)

// HTTPErrorLog is an http.Server ErrorLog. Each line net/http writes (a
// failed TLS handshake, a broken connection, a handler panic) becomes one
// debug-level JSON event, http.server_error, with the listener name and a
// closed kind. The line itself is dropped: it names the peer and can echo
// bytes the peer sent. Without it net/http prints plain text to stderr.
func (t *Telemetry) HTTPErrorLog(listener string) *log.Logger {
	return log.New(httpErrorWriter{t: t, listener: listener}, "", 0)
}

type httpErrorWriter struct {
	t        *Telemetry
	listener string
}

func (w httpErrorWriter) Write(p []byte) (int, error) {
	kind := HTTPErrorOther
	if bytes.Contains(p, []byte("TLS handshake error")) {
		kind = HTTPErrorTLSHandshake
	}
	w.t.Logger.Debug("http.server_error", "listener", w.listener, "kind", string(kind))
	return len(p), nil
}
