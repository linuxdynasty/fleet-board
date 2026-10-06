package api

import (
	"errors"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// enrollSeed is one fuzz input: the request's primitive fields, labels as one
// comma-separated string, and presence flags for the optional messages.
type enrollSeed struct {
	id, name, host, labels string
	hasTTL                 bool
	ttlSeconds             int64
	ttlNanos               int32
	hasPrev                bool
	prev                   string
}

func enrollSeeds() []enrollSeed {
	var valid32, valid33 []string
	for i := range 33 {
		l := "l" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if i < 32 {
			valid32 = append(valid32, l)
		}
		valid33 = append(valid33, l)
	}
	return []enrollSeed{
		{id: "req-1", name: "laptop"},
		{id: "req-2", name: "laptop", host: "laptop.lan", labels: "gpu,home"},
		{id: "01JABCDEFGHJKMNPQRSTVWXYZ0", name: "build box", labels: strings.Join(valid32, ",")},
		{id: "req-3", name: "laptop", labels: strings.Join(valid33, ",")},
		{id: "req-4", name: "laptop", hasTTL: true, ttlSeconds: 1},
		{id: "req-5", name: "laptop", hasTTL: true, ttlSeconds: 86400},
		{id: "req-6", name: "laptop", hasTTL: true, ttlSeconds: 86400, ttlNanos: 1},
		{id: "req-7", name: "laptop", hasTTL: true, ttlSeconds: 1, ttlNanos: -1},
		{id: "req-8", name: "laptop", hasTTL: true, ttlSeconds: -60},
		{id: "req-9", name: "laptop", hasTTL: true},
		{id: "req-10", name: "laptop", hasPrev: true, prev: "01jabcdefghjkmnpqrstvwxyz0"},
		{id: "req-11", name: "laptop", hasPrev: true},
		{id: "req-12", name: "laptop", hasPrev: true, prev: "../01jabc"},
		{id: "", name: "laptop"},
		{id: "", name: "has invalid fields"},
		{id: strings.Repeat("a", 65), name: "laptop"},
		{id: "a b", name: "laptop"},
		{id: "req-13", name: ""},
		{id: "req-14", name: strings.Repeat("m", 129)},
		{id: "req-15", name: "lap\x1btop"},
		{id: "req-16", name: "lap\u202etop"},
		{id: "req-17", name: "lap\u200btop"},
		{id: "req-18", name: "lap\xfftop"},
		{id: "req-19", name: "laptop", host: "my host"},
		{id: "req-20", name: "laptop", host: strings.Repeat("h", 254)},
		{id: "req-21", name: "laptop", labels: "ok,GPU"},
		{id: "req-22", name: "laptop", labels: "-x,gpu,gpu,,"},
	}
}

// FuzzCheckEnrollRequest checks checkEnrollRequest against the 02b field
// rules: it never panics; it accepts a request exactly when every field is
// inside the limits, and then returns the asked-for lifetime; a refusal is
// one INVALID_ARGUMENT whose violations name the bad fields, and whose message
// and violations stay byte for byte the same when every letter of the request
// is rotated, so they cannot echo the request's letters.
func FuzzCheckEnrollRequest(f *testing.F) {
	for _, s := range enrollSeeds() {
		f.Add(s.id, s.name, s.host, s.labels, s.hasTTL, s.ttlSeconds, s.ttlNanos, s.hasPrev, s.prev)
	}
	f.Fuzz(func(t *testing.T, id, name, host, labels string, hasTTL bool, ttlSeconds int64, ttlNanos int32,
		hasPrev bool, prev string,
	) {
		m := &hubv1.CreateEnrollmentTokenRequest{RequestId: id, MachineName: name, ExpectedHostname: host}
		if labels != "" {
			m.Labels = strings.Split(labels, ",")
		}
		if hasTTL {
			m.Ttl = &durationpb.Duration{Seconds: ttlSeconds, Nanos: ttlNanos}
		}
		if hasPrev {
			m.PreviousMachine = &typev1.MachineId{Value: prev}
		}
		bad, want := outsideLimits(m)
		rot := rotated(m)
		if rotBad, _ := outsideLimits(rot); !maps.Equal(bad, rotBad) {
			t.Fatalf("rotation changed which fields are bad: %v became %v", bad, rotBad)
		}
		ttl, err := checkEnrollRequest(m)
		if err == nil {
			if len(bad) > 0 {
				t.Fatalf("accepted a request with fields outside the 02b limits: %v", bad)
			}
			if ttl != want {
				t.Fatalf("lifetime %v, want %v", ttl, want)
			}
			return
		}
		if len(bad) == 0 {
			t.Fatalf("refused a request inside the 02b limits: %v", err)
		}
		ed, msg := checkRefusal(t, err, bad)
		_, rotErr := checkEnrollRequest(rot)
		if rotErr == nil {
			t.Fatal("the rotated request was accepted")
		}
		rotED, rotMsg := checkRefusal(t, rotErr, bad)
		if msg != rotMsg || !proto.Equal(ed, rotED) {
			t.Fatalf("the refusal depends on the request's letters:\n%q %v\nbecame\n%q %v", msg, ed, rotMsg, rotED)
		}
	})
}

// rotate moves each ASCII letter 13 places within its case and keeps every
// other byte, digits included. Only ASCII bytes change, so a field keeps its
// length, its UTF-8 validity, its 02b validity and which labels repeat, while
// no letter stays the same.
func rotate(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = 'a' + (c-'a'+13)%26
		case c >= 'A' && c <= 'Z':
			b[i] = 'A' + (c-'A'+13)%26
		}
	}
	return string(b)
}

func rotated(m *hubv1.CreateEnrollmentTokenRequest) *hubv1.CreateEnrollmentTokenRequest {
	r := &hubv1.CreateEnrollmentTokenRequest{
		RequestId:        rotate(m.GetRequestId()),
		MachineName:      rotate(m.GetMachineName()),
		ExpectedHostname: rotate(m.GetExpectedHostname()),
		Ttl:              m.GetTtl(),
	}
	for _, l := range m.GetLabels() {
		r.Labels = append(r.Labels, rotate(l))
	}
	if p := m.GetPreviousMachine(); p != nil {
		r.PreviousMachine = &typev1.MachineId{Value: rotate(p.GetValue())}
	}
	return r
}

// outsideLimits names each field outside the 02b rules, as the violation
// field the refusal must use, and returns the lifetime an accepted request
// gets. It is written from the spec, not from enroll_check.go.
func outsideLimits(m *hubv1.CreateEnrollmentTokenRequest) (map[string]bool, time.Duration) {
	bad := map[string]bool{}
	if id := m.GetRequestId(); len(id) < 1 || len(id) > 64 || !everyByte(id, func(b byte) bool { return b >= '!' && b <= '~' }) {
		bad["request_id"] = true
	}
	name := m.GetMachineName()
	hidden := func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }
	if len(name) < 1 || len(name) > 128 || !utf8.ValidString(name) || strings.IndexFunc(name, hidden) >= 0 {
		bad["machine_name"] = true
	}
	if host := m.GetExpectedHostname(); len(host) > 253 || !everyByte(host, hostByte) {
		bad["expected_hostname"] = true
	}
	if p := m.GetPreviousMachine(); p != nil && !shortName(p.GetValue()) {
		bad["previous_machine.value"] = true
	}
	if labels := m.GetLabels(); len(labels) > 32 {
		bad["labels"] = true
	} else {
		seen := map[string]bool{}
		for i, l := range labels {
			if !shortName(l) || seen[l] {
				bad["labels["+strconv.Itoa(i)+"]"] = true
			}
			seen[l] = true
		}
	}
	want := 15 * time.Minute
	if d := m.GetTtl(); d != nil {
		s, n := d.GetSeconds(), d.GetNanos()
		ok := s >= 0 && s <= 86400 && n > -1e9 && n < 1e9 && (s == 0 || n >= 0)
		if ok {
			want = time.Duration(s)*time.Second + time.Duration(n)
			ok = want > 0 && want <= 24*time.Hour
		}
		if !ok {
			bad["ttl"] = true
		}
	}
	return bad, want
}

// checkRefusal checks the shape of a refusal and returns its ErrorDetail and
// message. The cap of 16 violations is the handler's; 02b asks for one per
// bad field.
func checkRefusal(t *testing.T, err error, bad map[string]bool) (*typev1.ErrorDetail, string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Fatalf("refusal %v is not a connect INVALID_ARGUMENT", err)
	}
	details := ce.Details()
	if len(details) != 1 {
		t.Fatalf("refusal has %d details, want one ErrorDetail", len(details))
	}
	msg, derr := details[0].Value()
	ed, ok := msg.(*typev1.ErrorDetail)
	if derr != nil || !ok {
		t.Fatalf("refusal detail is %T (%v), want an ErrorDetail", msg, derr)
	}
	vs := ed.GetViolations()
	if len(vs) == 0 || ed.GetReason() != vs[0].GetReason() {
		t.Fatalf("refusal reason %v with violations %v, want the first violation's reason", ed.GetReason(), vs)
	}
	named := map[string]bool{}
	for _, v := range vs {
		f := v.GetField()
		if !bad[f] || named[f] {
			t.Errorf("violation on %q: not a bad field, or named twice (bad: %v)", f, bad)
		}
		named[f] = true
		if v.GetDescription() == "" {
			t.Errorf("violation on %q has no description", f)
		}
	}
	if len(named) != min(len(bad), 16) {
		t.Errorf("violations name %v, want every bad field %v", named, bad)
	}
	return ed, ce.Message()
}

func everyByte(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func lowerOrDigit(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }

func hostByte(b byte) bool {
	return lowerOrDigit(b) || (b >= 'A' && b <= 'Z') || b == '.' || b == '-' || b == '_'
}

// shortName is the ids.proto rule for labels and hub ids: 1 to 32 bytes of
// a-z, 0-9 and '-', starting with a letter or digit.
func shortName(s string) bool {
	return len(s) >= 1 && len(s) <= 32 && lowerOrDigit(s[0]) &&
		everyByte(s, func(b byte) bool { return lowerOrDigit(b) || b == '-' })
}
