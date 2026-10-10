package conn

import (
	"bytes"
	"context"
	"crypto/x509"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"
)

// present switches away from the pair to avoid only to a pair inside its
// life by the agent's clock (amendment e).
func TestPresentSwitchesOnlyToALivePair(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := 24 * time.Hour
	mk := func(src string, n int64, nb, na time.Time) *certPair {
		return &certPair{leaf: &x509.Certificate{NotBefore: nb, NotAfter: na, SerialNumber: big.NewInt(n)}, source: src}
	}
	e := mk("E", 1, t0, t0.Add(90*d))
	r := mk("R", 2, t0.Add(60*d), t0.Add(150*d))
	s := &pairSet{enrolled: e, renewed: r}
	for _, tc := range []struct {
		name  string
		now   time.Time
		avoid *certPair
		want  string
	}{
		{"avoid R, E alive", t0.Add(61 * d), r, "E"},
		{"avoid R, E expired", t0.Add(95 * d), r, "R"},
		{"avoid E, R presented anyway", t0.Add(61 * d), e, "R"},
		{"nothing to avoid", t0.Add(61 * d), nil, "R"},
	} {
		if got := s.present(tc.now, tc.avoid).source; got != tc.want {
			t.Errorf("%s: present = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// After a failed attempt, the pair it presented is avoided and
// renew.pair_switched logged only when the other pair is inside its life;
// otherwise nothing is avoided and nothing logged.
func TestAFailedAttemptSwitchesOnlyToALivePair(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := 24 * time.Hour
	mk := func(src string, n int64, nb, na time.Time) *certPair {
		return &certPair{leaf: &x509.Certificate{NotBefore: nb, NotAfter: na, SerialNumber: big.NewInt(n)}, source: src}
	}
	for _, tc := range []struct {
		name     string
		now      time.Time
		switched bool
	}{
		{"the enrolled pair is alive", t0.Add(61 * d), true},
		{"the enrolled pair has ended", t0.Add(95 * d), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			r := mk("cert_file", 2, t0.Add(60*d), t0.Add(150*d))
			l := &Loop{log: slog.New(slog.NewJSONHandler(&logs, nil)), now: func() time.Time { return tc.now }}
			l.pairs.Store(&pairSet{enrolled: mk("identity", 1, t0, t0.Add(90*d)), renewed: r})
			l.presented.Store(r)
			l.avoidPresented(context.Background())
			if got := l.avoid.Load() == r; got != tc.switched {
				t.Errorf("avoid set = %v, want %v", got, tc.switched)
			}
			if got := strings.Contains(logs.String(), "renew.pair_switched"); got != tc.switched {
				t.Errorf("renew.pair_switched logged = %v, want %v: %s", got, tc.switched, logs.String())
			}
		})
	}
}
