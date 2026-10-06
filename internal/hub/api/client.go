package api

import (
	"context"
	"net"
	"net/http"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
)

// adminBaseURL is the URL the admin client sends; the host is a placeholder,
// since every request goes to the socket.
const adminBaseURL = "http://fleetd"

// AdminHTTPClient is an HTTP/1.1 client whose every request goes to the Unix
// socket at path.
func AdminHTTPClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		DisableKeepAlives: true,
	}}
}

// AdminClient is a FleetService client on the admin socket at path.
func AdminClient(path string) hubv1connect.FleetServiceClient {
	return hubv1connect.NewFleetServiceClient(AdminHTTPClient(path), adminBaseURL)
}
