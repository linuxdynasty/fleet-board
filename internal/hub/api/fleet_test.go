package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

var tokenPattern = regexp.MustCompile(`^fbe1_[A-Za-z0-9_-]+$`)

func enrollReq(id, machine string) *connect.Request[hubv1.CreateEnrollmentTokenRequest] {
	return connect.NewRequest(&hubv1.CreateEnrollmentTokenRequest{RequestId: id, MachineName: machine})
}

func TestCreateEnrollmentTokenIssuesATokenForTheStoredCA(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	req := enrollReq("6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60", "laptop")
	req.Msg.Labels = []string{"gpu", "home"}
	req.Msg.ExpectedHostname = "laptop.lan"
	req.Msg.StartDrained = true
	resp, err := e.client.CreateEnrollmentToken(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	tok := resp.Msg.GetToken()
	if !tokenPattern.MatchString(tok) {
		t.Fatalf("token %q does not match %s", tok, tokenPattern)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, "fbe1_"))
	if err != nil || len(raw) != 65 || raw[0] != 1 {
		t.Fatalf("decoded %d bytes (err %v), want 65 starting with 1", len(raw), err)
	}
	fp := sha256.Sum256(e.caDER)
	if !bytes.Equal(raw[1:33], fp[:]) {
		t.Error("the token's CA hash is not SHA-256 of the stored CA certificate")
	}
	if resp.Msg.GetCaFingerprint() != hex.EncodeToString(fp[:]) {
		t.Errorf("ca_fingerprint %q, want %x", resp.Msg.GetCaFingerprint(), fp)
	}
	if got, want := resp.Msg.GetExpireTime().AsTime(), fleetNow.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("expire_time %v, want %v (15 min default)", got, want)
	}
	if got := resp.Msg.GetEnrollCommand(); got != "fleet-agent enroll --token "+tok {
		t.Errorf("enroll_command %q", got)
	}

	secret := raw[33:]
	wantHash := sha256.Sum256(secret)
	var stored []byte
	var notAfter int64
	err = e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT secret_hash, not_after_time FROM enroll_tokens`).Scan(&stored, &notAfter)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, wantHash[:]) {
		t.Error("stored secret_hash is not SHA-256 of the decoded secret")
	}
	if notAfter != fleetNow.Add(15*time.Minute).UnixMilli() {
		t.Errorf("not_after_time %d", notAfter)
	}

	issued := e.logs.events(t, "enroll.issued")
	if len(issued) != 1 {
		t.Fatalf("%d enroll.issued lines, want 1", len(issued))
	}
	if issued[0]["machine"] != "laptop" || issued[0]["actor"] != "local:alice" || issued[0]["expire_time"] == nil {
		t.Errorf("enroll.issued = %v", issued[0])
	}
	logs := e.logs.String()
	for name, s := range map[string]string{
		"token": tok, "secret (base64url)": base64.RawURLEncoding.EncodeToString(secret),
		"secret hash prefix":   hex.EncodeToString(wantHash[:])[:12],
		"secret hash (base64)": base64.StdEncoding.EncodeToString(wantHash[:])[:16],
	} {
		if strings.Contains(logs, s) {
			t.Errorf("the logs hold the %s", name)
		}
	}
	if got := sample(e.scrape(t), "fleet_hub_enroll_tokens_total", `result="issued"`); got != 1 {
		t.Errorf("fleet_hub_enroll_tokens_total{issued} = %v, want 1", got)
	}
}

func TestEnrollmentTokenLifetime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ttl  *durationpb.Duration
		want time.Duration // 0: refused
	}{
		{"unset is 15 min", nil, 15 * time.Minute},
		{"1 s", durationpb.New(time.Second), time.Second},
		{"24 h is the most", durationpb.New(24 * time.Hour), 24 * time.Hour},
		{"25 h refused", durationpb.New(25 * time.Hour), 0},
		{"24 h and 1 ns refused", durationpb.New(24*time.Hour + 1), 0},
		{"0 refused", durationpb.New(0), 0},
		{"negative refused", durationpb.New(-time.Minute), 0},
		{"malformed refused", &durationpb.Duration{Seconds: 1, Nanos: -1}, 0},
	}
	e := newFleetEnv(t, localUser("alice"))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := enrollReq("ttl-"+string(rune('a'+i)), "laptop")
			req.Msg.Ttl = tc.ttl
			resp, err := e.client.CreateEnrollmentToken(context.Background(), req)
			if tc.want == 0 {
				code, reason, fields := detail(t, err)
				if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT ||
					len(fields) != 1 || fields[0] != "ttl" {
					t.Errorf("got %v %v %v, want invalid_argument INVALID_ARGUMENT on ttl", code, reason, fields)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := resp.Msg.GetExpireTime().AsTime().Sub(fleetNow); got != tc.want {
				t.Errorf("lifetime %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnrollmentTokenRequestValidation(t *testing.T) {
	t.Parallel()
	many := make([]string, 33)
	for i := range many {
		many[i] = "l" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	cases := []struct {
		name   string
		edit   func(*hubv1.CreateEnrollmentTokenRequest)
		reason typev1.ReasonCode
		field  string
	}{
		{
			"missing request_id", func(r *hubv1.CreateEnrollmentTokenRequest) { r.RequestId = "" },
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id",
		},
		{
			"request_id over 64 bytes", func(r *hubv1.CreateEnrollmentTokenRequest) { r.RequestId = strings.Repeat("a", 65) },
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "request_id",
		},
		{
			"request_id with a space", func(r *hubv1.CreateEnrollmentTokenRequest) { r.RequestId = "a b" },
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "request_id",
		},
		{
			"missing machine_name", func(r *hubv1.CreateEnrollmentTokenRequest) { r.MachineName = "" },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name",
		},
		{
			"machine_name over 128 bytes", func(r *hubv1.CreateEnrollmentTokenRequest) { r.MachineName = strings.Repeat("m", 129) },
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "machine_name",
		},
		{
			"machine_name with a control character", func(r *hubv1.CreateEnrollmentTokenRequest) { r.MachineName = "lap\x1btop" },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name",
		},
		{
			"machine_name with a right-to-left override", func(r *hubv1.CreateEnrollmentTokenRequest) { r.MachineName = "lap\u202etop" },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name",
		},
		{
			"machine_name with a zero-width space", func(r *hubv1.CreateEnrollmentTokenRequest) { r.MachineName = "lap\u200btop" },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name",
		},
		{
			"expected_hostname with a space", func(r *hubv1.CreateEnrollmentTokenRequest) { r.ExpectedHostname = "my host" },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "expected_hostname",
		},
		{
			"expected_hostname over 253 bytes", func(r *hubv1.CreateEnrollmentTokenRequest) { r.ExpectedHostname = strings.Repeat("h", 254) },
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "expected_hostname",
		},
		{
			"upper-case label", func(r *hubv1.CreateEnrollmentTokenRequest) { r.Labels = []string{"ok", "GPU"} },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "labels[1]",
		},
		{
			"label of 33 characters", func(r *hubv1.CreateEnrollmentTokenRequest) { r.Labels = []string{strings.Repeat("a", 33)} },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "labels[0]",
		},
		{
			"label starting with a dash", func(r *hubv1.CreateEnrollmentTokenRequest) { r.Labels = []string{"-x"} },
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "labels[0]",
		},
		{
			"33 labels", func(r *hubv1.CreateEnrollmentTokenRequest) { r.Labels = many },
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "labels",
		},
		{
			"repeated label", func(r *hubv1.CreateEnrollmentTokenRequest) { r.Labels = []string{"gpu", "gpu"} },
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "labels[1]",
		},
		{
			"empty previous_machine", func(r *hubv1.CreateEnrollmentTokenRequest) { r.PreviousMachine = &typev1.MachineId{} },
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "previous_machine.value",
		},
		{
			"previous_machine with a slash", func(r *hubv1.CreateEnrollmentTokenRequest) {
				r.PreviousMachine = &typev1.MachineId{Value: "../01jabc"}
			},
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "previous_machine.value",
		},
		{
			"previous_machine of 33 characters", func(r *hubv1.CreateEnrollmentTokenRequest) {
				r.PreviousMachine = &typev1.MachineId{Value: strings.Repeat("a", 33)}
			},
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "previous_machine.value",
		},
	}
	e := newFleetEnv(t, localUser("alice"))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := enrollReq("valid-id", "laptop")
			tc.edit(req.Msg)
			_, err := e.client.CreateEnrollmentToken(context.Background(), req)
			code, reason, fields := detail(t, err)
			if code != connect.CodeInvalidArgument || reason != tc.reason || len(fields) != 1 || fields[0] != tc.field {
				t.Errorf("got %v %v %v, want invalid_argument %v on %s", code, reason, fields, tc.reason, tc.field)
			}
		})
	}
	if got := countRows(t, e); got != 0 {
		t.Errorf("%d rows stored by refused requests, want 0", got)
	}
	if got := sample(e.scrape(t), "fleet_hub_enroll_tokens_total", `result="refused"`); got != float64(len(cases)) {
		t.Errorf("fleet_hub_enroll_tokens_total{refused} = %v, want %d", got, len(cases))
	}
}

func countRows(t *testing.T, e *fleetEnv) int {
	t.Helper()
	var n int
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM enroll_tokens`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnrollmentTokenRequestIDReuse(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	ctx := context.Background()
	if _, err := e.client.CreateEnrollmentToken(ctx, enrollReq("req-1", "laptop")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		machine string
		code    connect.Code
		reason  typev1.ReasonCode
	}{
		{"same body", "laptop", connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED},
		{"different body", "desktop", connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT},
	} {
		resp, err := e.client.CreateEnrollmentToken(ctx, enrollReq("req-1", tc.machine))
		if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
			t.Errorf("%s: %v %v, want %v %v", tc.name, code, reason, tc.code, tc.reason)
		}
		if resp != nil {
			t.Errorf("%s: a response came back with the error", tc.name)
		}
	}
	if got := countRows(t, e); got != 1 {
		t.Errorf("%d rows, want 1", got)
	}
}

func TestConcurrentRequestIDReuseMintsOneToken(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	const n = 16
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.client.CreateEnrollmentToken(context.Background(), enrollReq("req-race", "laptop"))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	issued := 0
	for err := range errs {
		if err == nil {
			issued++
			continue
		}
		if code, reason, _ := detail(t, err); code != connect.CodeAlreadyExists ||
			reason != typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED {
			t.Errorf("got %v %v, want already_exists SECRET_ALREADY_DELIVERED", code, reason)
		}
	}
	if issued != 1 {
		t.Errorf("%d tokens issued for one request_id, want 1", issued)
	}
	if got := countRows(t, e); got != 1 {
		t.Errorf("%d rows, want 1", got)
	}
}

func TestAPreviousMachineIsNotReenrollable(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	req := enrollReq("req-1", "laptop")
	req.Msg.PreviousMachine = &typev1.MachineId{Value: "01jabcdefghjkmnpqrstvwxyz0"}
	_, err := e.client.CreateEnrollmentToken(context.Background(), req)
	if code, reason, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		reason != typev1.ReasonCode_REASON_CODE_MACHINE_NOT_REENROLLABLE {
		t.Errorf("got %v %v, want failed_precondition MACHINE_NOT_REENROLLABLE", code, reason)
	}
	if got := countRows(t, e); got != 0 {
		t.Errorf("%d rows, want 0", got)
	}
}

// CreateEnrollmentToken needs ADMIN (02d); in 2b only the local user on the
// admin socket holds it.
func TestEnrollmentTokenDeniedPaths(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", user, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			_, err := e.client.CreateEnrollmentToken(context.Background(), enrollReq("", ""))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
			if got := countRows(t, e); got != 0 {
				t.Errorf("%d rows, want 0", got)
			}
		})
	}
}

func TestEveryOtherFleetMethodIsUnimplemented(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	ctx := context.Background()
	_, err := e.client.ListMachines(ctx, connect.NewRequest(&hubv1.ListMachinesRequest{}))
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Errorf("ListMachines: %v, want unimplemented", code)
	}
	_, err = e.client.CreateApiToken(ctx, connect.NewRequest(&hubv1.CreateApiTokenRequest{RequestId: "x"}))
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Errorf("CreateApiToken: %v, want unimplemented", code)
	}
}
