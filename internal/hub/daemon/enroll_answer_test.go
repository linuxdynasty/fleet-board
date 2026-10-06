package daemon_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// failingFleet answers CreateEnrollmentToken with the hub's INTERNAL error.
type failingFleet struct {
	hubv1connect.UnimplementedFleetServiceHandler
}

func (failingFleet) CreateEnrollmentToken(context.Context, *connect.Request[hubv1.CreateEnrollmentTokenRequest]) (*connect.Response[hubv1.CreateEnrollmentTokenResponse], error) {
	ce := connect.NewError(connect.CodeInternal, errors.New("the hub could not create the token"))
	d, err := connect.NewErrorDetail(&typev1.ErrorDetail{Reason: typev1.ReasonCode_REASON_CODE_INTERNAL})
	if err != nil {
		return nil, err
	}
	ce.AddDetail(d)
	return nil, ce
}

// fakeAdmin listens on h's admin.sock in place of a hub and hands every
// accepted connection to serve.
func fakeAdmin(t *testing.T, h *hub, serve func(net.Listener)) {
	t.Helper()
	ln, err := net.Listen("unix", config.AdminSocketPath(h.state))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serve(ln)
}

func TestEnrollTokenNamesWhyThereIsNoToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		serve    func(t *testing.T) func(net.Listener)
		want     string
		mustNot  []string
		deadline time.Duration
	}{
		{
			name: "the hub never answers",
			serve: func(t *testing.T) func(net.Listener) {
				return func(ln net.Listener) {
					for {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						t.Cleanup(func() { _ = c.Close() })
					}
				}
			},
			want:     "the hub did not answer in time",
			mustNot:  []string{"only the OS user", "the hub refused"},
			deadline: 300 * time.Millisecond,
		},
		{
			name: "the hub fails inside",
			serve: func(t *testing.T) func(net.Listener) {
				mux := http.NewServeMux()
				mux.Handle(hubv1connect.NewFleetServiceHandler(failingFleet{}))
				srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
				t.Cleanup(func() { _ = srv.Close() })
				return func(ln net.Listener) { _ = srv.Serve(ln) }
			},
			want:     "the hub failed: the hub could not create the token (INTERNAL)",
			mustNot:  []string{"the hub refused", "only the OS user"},
			deadline: 10 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHub(t, "")
			h.init(t)
			fakeAdmin(t, h, tc.serve(t))
			ctx, cancel := context.WithTimeout(context.Background(), tc.deadline)
			defer cancel()
			_, err := daemon.EnrollToken(ctx, h.options(), daemon.EnrollRequest{Machine: "laptop"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("EnrollToken: %v, want %q", err, tc.want)
			}
			for _, s := range tc.mustNot {
				if strings.Contains(err.Error(), s) {
					t.Errorf("EnrollToken: %v, must not say %q", err, s)
				}
			}
		})
	}
}
