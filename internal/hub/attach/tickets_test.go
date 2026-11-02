package attach

import (
	"errors"
	"testing"
	"time"
)

// testRand is a deterministic secret source: every 32-byte read differs,
// so mints in one test never collide the way a constant reader would.
type testRand struct{ n byte }

func (r *testRand) Read(p []byte) (int, error) {
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

func testClock() (func() time.Time, *time.Time) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }, &now
}

// TestTicketMintBurnOnce: a minted ticket burns exactly once; the burn
// binds principal, session and origin, and AttachOpen may only tighten
// read-only.
func TestTicketMintBurnOnce(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	s := NewStore(now, &testRand{})
	secret, expires, err := s.Mint(MintIn{
		PrincipalID: "local-owner", SessionID: "sess-1", RequestID: "req-1",
		ReadOnly: false, Origin: "http://localhost:8080", Body: []byte("req-1-body"), TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(secret) != 64 {
		t.Fatalf("ticket secret is %d chars, want 64 hex (256-bit)", len(secret))
	}
	if expires.Sub(now()) != time.Minute {
		t.Fatalf("ticket TTL = %v, want 60s", expires.Sub(now()))
	}
	got, err := s.Burn(secret, "local-owner", "sess-1", "http://localhost:8080", false)
	if err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if got.ReadOnly {
		t.Fatal("burn widened nothing: ticket was read-write, open read-write, want read-write")
	}
	if _, err := s.Burn(secret, "local-owner", "sess-1", "http://localhost:8080", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second Burn = %v, want ErrInvalid (single use)", err)
	}
}

// TestTicketBurnTightensReadOnly: a read-write ticket opened read-only
// stays read-only; a read-only ticket opened read-write is clamped, not
// widened.
func TestTicketBurnTightensReadOnly(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	s := NewStore(now, &testRand{})
	rw, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "r1",
		Origin: "http://localhost:8080", Body: []byte("b1"), TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err := s.Burn(rw, "p", "s", "http://localhost:8080", true)
	if err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if !got.ReadOnly {
		t.Fatal("read-only open on a read-write ticket stayed read-write, want read-only")
	}
	ro, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "r2", ReadOnly: true,
		Origin: "http://localhost:8080", Body: []byte("b2"), TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	got, err = s.Burn(ro, "p", "s", "http://localhost:8080", false)
	if err != nil {
		t.Fatalf("Burn: %v", err)
	}
	if !got.ReadOnly {
		t.Fatal("read-write open on a read-only ticket widened it, want clamped read-only")
	}
}

// TestTicketBurnBindings: the wrong principal, session or origin, and an
// empty session that matches, each behave per 02f.
func TestTicketBurnBindings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                       string
		principal, session, origin string
		wantErr                    bool
	}{
		{"wrong principal", "mallory", "sess-1", "http://localhost:8080", true},
		{"wrong session", "local-owner", "sess-2", "http://localhost:8080", true},
		{"wrong origin", "local-owner", "sess-1", "https://evil.test", true},
		{"empty session matches", "local-owner", "", "http://localhost:8080", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now, _ := testClock()
			s := NewStore(now, &testRand{})
			secret, _, err := s.Mint(MintIn{
				PrincipalID: "local-owner", SessionID: "sess-1", RequestID: "req-" + tc.name,
				Origin: "http://localhost:8080", Body: []byte("b"), TTL: time.Minute,
			})
			if err != nil {
				t.Fatalf("Mint: %v", err)
			}
			_, err = s.Burn(secret, tc.principal, tc.session, tc.origin, false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Burn err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestTicketExpiry: a ticket past its 60 s life burns invalid, and a
// replay of the request_id after expiry still refuses (never re-mints).
func TestTicketExpiry(t *testing.T) {
	t.Parallel()
	now, nowP := testClock()
	s := NewStore(now, &testRand{})
	secret, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "req-1",
		Origin: "http://localhost:8080", Body: []byte("b"), TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	*nowP = nowP.Add(61 * time.Second)
	if _, err := s.Burn(secret, "p", "s", "http://localhost:8080", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired Burn = %v, want ErrInvalid", err)
	}
	if _, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "req-1",
		Origin: "http://localhost:8080", Body: []byte("b"), TTL: time.Minute,
	}); !errors.Is(err, ErrDelivered) {
		t.Fatalf("replay Mint after expiry = %v, want ErrDelivered", err)
	}
}

// TestTicketReplay: the same request_id with the same body refuses as
// already-delivered (the secret is never re-shown); another body with the
// same request_id is a conflict.
func TestTicketReplay(t *testing.T) {
	t.Parallel()
	now, _ := testClock()
	s := NewStore(now, &testRand{})
	if _, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "req-1",
		Origin: "http://localhost:8080", Body: []byte("b"), TTL: time.Minute,
	}); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "req-1",
		Origin: "http://localhost:8080", Body: []byte("b"), TTL: time.Minute,
	}); !errors.Is(err, ErrDelivered) {
		t.Fatalf("same-body replay = %v, want ErrDelivered", err)
	}
	if _, _, err := s.Mint(MintIn{
		PrincipalID: "p", SessionID: "s", RequestID: "req-1",
		Origin: "http://localhost:8080", Body: []byte("other"), TTL: time.Minute,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("other-body replay = %v, want ErrConflict", err)
	}
}
