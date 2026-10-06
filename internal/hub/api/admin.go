package api

import (
	"context"
	"net"
	"net/http"
	"os/user"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// PeerUID reads the kernel user id of the process at the other end of a
// Unix socket connection.
type PeerUID func(*net.UnixConn) (uint32, error)

// AdminOptions build [NewAdmin]. Fleet and Telemetry are required.
type AdminOptions struct {
	Fleet     *Fleet
	Telemetry *telemetry.Telemetry
	// UID is the only peer user id served: fleetd's own.
	UID uint32
	// PeerUID reads a connection's peer; nil means [KernelPeerUID].
	PeerUID PeerUID
	// Lookup resolves the peer's user id to a name; nil means os/user.
	Lookup func(string) (*user.User, error)
}

// NewAdmin wraps the admin socket's listener ln and builds its server
// (02f, 02j): Connect unary over HTTP/1.1 with the FleetService handler.
// The listener closes, logs (admin.peer_refused) and counts
// (auth_failures_total{reason="peer_uid"}) every connection whose peer is
// not opts.UID, before any byte of it is read. A served connection carries
// Principal{LOCAL_USER, local:<os user>} of its peer. Any request with an
// Origin header is refused (403 ORIGIN_NOT_ALLOWED). Serve the returned
// server on the returned listener.
func NewAdmin(ln *net.UnixListener, opts AdminOptions) (net.Listener, *http.Server) {
	peer := opts.PeerUID
	if peer == nil {
		peer = KernelPeerUID
	}
	mux := http.NewServeMux()
	mux.Handle(opts.Fleet.Handler())
	errs := connect.NewErrorWriter()
	tel := opts.Telemetry
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, sent := r.Header["Origin"]; sent {
			refuse(w, r, tel, errs, telemetry.ReasonOrigin, typev1.ReasonCode_REASON_CODE_ORIGIN_NOT_ALLOWED,
				"the admin socket takes no browser requests")
			return
		}
		mux.ServeHTTP(w, r)
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		ErrorLog:          tel.HTTPErrorLog("admin"),
		Protocols:         protocols,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if ac, ok := c.(*adminConn); ok {
				return WithPrincipal(ctx, ac.who)
			}
			return ctx
		},
	}
	return &peerListener{UnixListener: ln, uid: opts.UID, peer: peer, lookup: opts.Lookup, tel: tel}, srv
}

// peerListener accepts only connections from one user id.
type peerListener struct {
	*net.UnixListener
	uid    uint32
	peer   PeerUID
	lookup func(string) (*user.User, error)
	tel    *telemetry.Telemetry
}

type adminConn struct {
	*net.UnixConn
	who *typev1.Principal
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.peer(c)
		if err == nil && uid == l.uid {
			return &adminConn{UnixConn: c, who: LocalUser(uid, l.lookup)}, nil
		}
		ctx := context.Background()
		l.tel.CountAuthFailure(ctx, telemetry.ReasonPeerUID)
		if err != nil {
			l.tel.Logger.WarnContext(ctx, "admin.peer_refused", "cause", "unreadable")
		} else {
			l.tel.Logger.WarnContext(ctx, "admin.peer_refused", "cause", "uid_mismatch", "peer_uid", uid, "uid", l.uid)
		}
		_ = c.Close() // refused: nothing was read, and nothing is owed to the peer
	}
}
