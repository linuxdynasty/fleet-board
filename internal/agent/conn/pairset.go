package conn

import (
	"context"
	"errors"
	"path/filepath"
	"time"
)

// pairSet is the enrolled pair and the renewed one from cert.pem (nil when
// there is no valid one). A set is never changed once built; a renewal
// stores a new one.
type pairSet struct {
	enrolled, renewed *certPair
}

// lead is the pair that ends last, whether it has started or not. The
// renewal decision, the loop's expiry check and the expiry gauge follow
// it, so a renewed pair that starts in the future (a hub clock ahead when
// it signed) does not start a renewal with a new key at every check.
func (s *pairSet) lead() *certPair {
	if s.renewed != nil && s.renewed.notAfter().After(s.enrolled.notAfter()) {
		return s.renewed
	}
	return s.enrolled
}

// dial is the pair to present at a handshake: of the pairs inside their
// life at now (by the agent's clock), the one that ends last; when none
// is, the lead. A renewed pair that has not started yet is not presented
// while the enrolled one is still good.
func (s *pairSet) dial(now time.Time) *certPair {
	var best *certPair
	for _, p := range []*certPair{s.enrolled, s.renewed} {
		if p != nil && p.validAt(now) && (best == nil || p.notAfter().After(best.notAfter())) {
			best = p
		}
	}
	if best == nil {
		return s.lead()
	}
	return best
}

// present is dial(now), except that when that is avoid (the pair the
// last attempt presented before it failed) and the other pair is inside
// its life at now, the other is presented: a hub that refuses one pair at
// the handshake (its clock, or a renewal it does not know) is offered the
// other at the next attempt, whatever its TLS alert said.
func (s *pairSet) present(now time.Time, avoid *certPair) *certPair {
	p := s.dial(now)
	if avoid == nil || p.serial() != avoid.serial() {
		return p
	}
	if o := s.other(p); o != nil && o.validAt(now) {
		return o
	}
	return p
}

// other is the pair of the set that is not p, or nil.
func (s *pairSet) other(p *certPair) *certPair {
	if p == s.enrolled {
		return s.renewed
	}
	return s.enrolled
}

// hubAnchor is a HelloAck's hub_time and when it arrived, with its
// monotonic reading.
type hubAnchor struct{ at, recv time.Time }

// hubTime is the clock of the renewal decision, the loop's expiry check
// and the expiry gauge: the last HelloAck's hub_time plus the monotonic
// time since (02i "Clocks"), or the agent's clock before the first
// HelloAck and after one with no hub_time. An agent clock days off then
// neither renews at every check nor ends the loop while the hub would
// take the pair. The pair presented stays on the agent's clock (dial).
func (l *Loop) hubTime() time.Time {
	if a := l.anchor.Load(); a != nil {
		return a.at.Add(l.since(a.recv))
	}
	return l.now()
}

// avoidPresented, after an attempt that failed before Connected, marks
// the pair it presented so the next attempt presents the other one when
// that one is inside its life. Connected clears it. Nothing is inferred
// from the refusal: a refused handshake is never taken as a revoke.
func (l *Loop) avoidPresented(ctx context.Context) {
	p := l.presented.Load()
	if p == nil {
		return
	}
	set := l.pairs.Load()
	o := set.other(p)
	if o == nil || !o.validAt(l.now()) {
		l.avoid.Store(nil)
		return
	}
	l.log.InfoContext(ctx, "renew.pair_switched", "from_source", p.source, "from_serial", p.serial(),
		"to_source", o.source, "to_serial", o.serial())
	l.avoid.Store(p)
}

// loadPair reads the enrolled pair and [CertFile]. A cert.pem that is not
// valid is logged (cert.file_invalid) and kept for doctor to report; the
// next renewal replaces it. A renew.key whose key is the valid cert.pem's
// is the leftover of a renewal that saved its certificate, and is
// removed; any other is kept for the next try.
func (l *Loop) loadPair(ctx context.Context) error {
	id := l.opts.Identity
	enrolled, err := identityPair(id)
	if err != nil {
		return err
	}
	file, err := filePair(l.opts.Dir, l.ca, id.MachineID)
	if err != nil {
		check := "file"
		if pe, ok := errors.AsType[*pairError](err); ok {
			check = pe.check
		}
		l.log.WarnContext(ctx, "cert.file_invalid", "file", CertFile, "check", check, "err_kind", pairErrKind(err))
	}
	if file != nil {
		path := filepath.Join(l.opts.Dir, RenewKeyFile)
		if k, err := readKey(path); err == nil && sameKey(k, file.key) {
			if err := removePrivate(l.opts.Dir, RenewKeyFile); err != nil {
				l.renewFileFailed(ctx, "cleanup", &fileError{file: path, check: "unremovable", err: err})
			} else {
				l.log.InfoContext(ctx, "renew.leftover_removed", "file", RenewKeyFile)
			}
		}
	}
	set := &pairSet{enrolled: enrolled, renewed: file}
	l.pairs.Store(set)
	lead, dial := set.lead(), set.dial(l.now())
	l.log.InfoContext(ctx, "renew.pair_loaded", "source", lead.source, "serial", lead.serial(),
		"not_after", lead.notAfter().UTC(), "renew_after", lead.renewAfter().UTC(), "dial_source", dial.source)
	return nil
}
