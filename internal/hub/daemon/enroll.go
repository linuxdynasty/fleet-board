package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// ErrNotRunning means no hub answers on admin.sock.
var ErrNotRunning = errors.New("fleetd is not running")

// enrollTimeout bounds one `fleetd enroll-token` call.
const enrollTimeout = 30 * time.Second

// EnrollRequest is what `fleetd enroll-token` asks for.
type EnrollRequest struct {
	Machine    string
	ExpectHost string
	// TTL is the token lifetime; nil leaves it to the hub (15 min).
	TTL          *time.Duration
	Labels       []string
	StartDrained bool
}

// EnrollToken is `fleetd enroll-token`: it reads fleetd.yaml for the state
// folder and asks the running hub over admin.sock for an enroll token, with
// a fresh request_id. It writes no file and emits no telemetry. With no hub
// listening it is [ErrNotRunning]; a hub refusal comes back as an error
// naming the reason and the bad fields.
func EnrollToken(ctx context.Context, opts Options, req EnrollRequest) (*hubv1.CreateEnrollmentTokenResponse, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	m := &hubv1.CreateEnrollmentTokenRequest{
		RequestId: id, MachineName: req.Machine, ExpectedHostname: req.ExpectHost,
		Labels: req.Labels, StartDrained: req.StartDrained,
	}
	if req.TTL != nil {
		m.Ttl = durationpb.New(*req.TTL)
	}
	ctx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()
	resp, err := api.AdminClient(config.AdminSocketPath(cfg.StateDir)).CreateEnrollmentToken(ctx, connect.NewRequest(m))
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, ErrNotRunning
	}
	if err != nil {
		return nil, refusal(err)
	}
	return resp.Msg, nil
}

// refusal turns a hub error into one line: its message, reason and the
// fields it names. An error without the hub's ErrorDetail did not come from
// the handler: the call ran out of time, or the connection was closed
// unserved (the peer check) or broke.
func refusal(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return fmt.Errorf("the hub did not answer in time: %w", err)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	reason := ""
	var fields []string
	for _, d := range ce.Details() {
		v, derr := d.Value()
		ed, ok := v.(*typev1.ErrorDetail)
		if derr != nil || !ok {
			continue
		}
		reason = strings.TrimPrefix(ed.GetReason().String(), "REASON_CODE_")
		for _, f := range ed.GetViolations() {
			fields = append(fields, f.GetField()+": "+f.GetDescription())
		}
	}
	if reason == "" {
		return fmt.Errorf("the hub closed the connection without an answer; admin.sock serves only the OS user that runs fleetd: %w", err)
	}
	verb := "refused"
	if ce.Code() == connect.CodeInternal {
		verb = "failed"
	}
	msg := fmt.Sprintf("the hub %s: %s (%s)", verb, ce.Message(), reason)
	if len(fields) > 0 {
		msg += "; " + strings.Join(fields, "; ")
	}
	return errors.New(msg)
}
