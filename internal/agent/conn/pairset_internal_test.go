package conn

import (
	"crypto/x509"
	"math/big"
	"testing"
	"time"
)

// rp is a pair with only the fields the choice reads.
func rp(src string, nb, na time.Time) *certPair {
	return &certPair{leaf: &x509.Certificate{NotBefore: nb, NotAfter: na, SerialNumber: big.NewInt(1)}, source: src}
}

// pairSet.dial presents, of the pairs inside their life at now, the one
// that ends later, and the lead when none is; pairSet.lead is the pair
// that ends later, started or not (an equal end keeps the enrolled one).
func TestThePairSetChoosesWhatToPresentAndWhatToRenew(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := 24 * time.Hour
	E := rp("E", t0, t0.Add(90*d))
	cases := []struct {
		name       string
		r          *certPair
		now        time.Time
		dial, lead string
	}{
		{"no renewed, enrolled valid", nil, t0.Add(10 * d), "E", "E"},
		{"no renewed, enrolled expired", nil, t0.Add(91 * d), "E", "E"},
		{"no renewed, before enrolled start", nil, t0.Add(-d), "E", "E"},
		{"both valid", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(61 * d), "R", "R"},
		{"renewed not started, enrolled valid", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(59 * d), "E", "R"},
		{"renewed not started, enrolled expired", rp("R", t0.Add(95*d), t0.Add(185*d)), t0.Add(91 * d), "R", "R"},
		{"renewed expired, enrolled valid (renewed ends earlier)", rp("R", t0.Add(-10*d), t0.Add(5*d)), t0.Add(10 * d), "E", "E"},
		{"original expired, renewed valid", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(100 * d), "R", "R"},
		{"both expired", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(200 * d), "R", "R"},
		{"equal expiry, both valid", rp("R", t0.Add(30*d), t0.Add(90*d)), t0.Add(40 * d), "E", "E"},
		{"equal expiry, renewed not started", rp("R", t0.Add(30*d), t0.Add(90*d)), t0.Add(20 * d), "E", "E"},
		{"clock before both NotBefore", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(-5 * d), "R", "R"},
		{"clock far ahead", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(5000 * d), "R", "R"},
		{"renewed ends earlier, both valid", rp("R", t0.Add(10*d), t0.Add(50*d)), t0.Add(20 * d), "E", "E"},
		{"renewed ends earlier, enrolled expired", rp("R", t0.Add(10*d), t0.Add(50*d)), t0.Add(95 * d), "E", "E"},
		{"at exactly renewed NotBefore", rp("R", t0.Add(60*d), t0.Add(150*d)), t0.Add(60 * d), "R", "R"},
		{"at exactly enrolled NotAfter, renewed not started", rp("R", t0.Add(91*d), t0.Add(180*d)), t0.Add(90 * d), "R", "R"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &pairSet{enrolled: E, renewed: tc.r}
			if got := s.dial(tc.now).source; got != tc.dial {
				t.Errorf("dial = %s, want %s", got, tc.dial)
			}
			if got := s.lead().source; got != tc.lead {
				t.Errorf("lead = %s, want %s", got, tc.lead)
			}
		})
	}
}
