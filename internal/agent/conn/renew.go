package conn

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Certificate renewal (02g "Certificate renewal (A4)", 03 W1). While a
// stream is connected, the session checks the pair that ends last when the
// stream connects and every RenewCheck; at two thirds of its life one
// renewal runs at a time, on a goroutine the session owns, cancels and
// waits for before its connection closes. The renewal never sends on the
// stream and never ends the loop: any failure is logged, counted and tried
// again at the next check.
//
//	renew.key written (kept by every retry, so the hub returns its stored certificate)
//	→ RenewCert over the stream's connection → answer checked
//	→ cert.pem replaced (temp, fsync, rename) → the pair set swapped → renew.key removed
//
// The pair set is an atomic pointer: each TLS handshake reads it through
// GetClientCertificate, so the next connection presents the new pair once
// it has started, while the live stream keeps the connection it has.

// renewCheck starts a renewal when the pair that ends last is due by hub
// time and the gate lets it (renewgate.go). Only Run's goroutine calls it.
func (s *session) renewCheck() {
	l := s.l
	l.hookAt("check")
	lead := l.pairs.Load().lead()
	if !lead.due(s.renewNow()) {
		l.gate.unstick(lead)
		return
	}
	ok, skip := l.gate.claim(lead, l.ca, l.opts.RenewCheck)
	if skip != "" {
		l.logSkip(s.renewCtx, lead, skip)
	}
	if !ok {
		return
	}
	s.renewWG.Add(1)
	go func() {
		defer s.renewWG.Done()
		saved := l.renew(s.renewCtx, s.k, s.c.addr)
		dueAtOnce := saved != nil && saved.due(s.renewNow())
		l.gate.done(saved, dueAtOnce)
		if dueAtOnce {
			l.logSkip(s.renewCtx, saved, skipDueAtOnce)
		}
	}()
}

// renewNow is the time the renewal decision uses: hub time (pairset.go),
// so an agent clock days off neither renews early at every check nor late.
func (s *session) renewNow() time.Time { return s.l.hubTime() }

// clientCert serves the pair to present at each TLS handshake
// ([pairSet.dial]).
func (l *Loop) clientCert(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	p := l.pairs.Load().present(l.now(), l.avoid.Load())
	l.presented.Store(p)
	return &p.tls, nil
}

// renew runs one renewal of the pair that ends last and returns the new
// pair once it is saved (nil otherwise). A crash at any step leaves either
// the old pairs with renew.key kept (the next try sends the same CSR), or
// the new pair whole in cert.pem.
func (l *Loop) renew(ctx context.Context, k *link, peer string) *certPair {
	ctx, span := l.tracer.Start(ctx, "agent.cert.renew")
	defer span.End()
	r := &renewRun{l: l, span: span}
	set := l.pairs.Load()
	cur := set.lead()
	key, kept, err := renewKey(l.opts.Dir, cur)
	if err != nil {
		r.failed(ctx, "key", "failed", err)
		return nil
	}
	if l.crashAt("before_send") {
		return nil
	}
	csr, err := newCSR(key)
	if err != nil {
		r.failed(ctx, "key", "failed", err)
		return nil
	}
	l.log.InfoContext(ctx, "renew.started", "peer", peer, "serial", cur.serial(), "not_after", cur.notAfter().UTC(),
		"key_kept", kept)
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	res, err := k.client.RenewCert(cctx, connect.NewRequest(&agentv1.RenewCertRequest{CsrPem: csr}))
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			// The stream ended or the agent is stopping: not a failure. The
			// next check sends the same CSR again.
			l.hookAt("cancelled")
			l.log.InfoContext(ctx, "renew.cancelled", "step", "call", "serial", cur.serial())
			return nil
		}
		r.callFailed(ctx, err)
		return nil
	}
	next, err := checkRenewal(res.Msg, key, l.ca, l.opts.Identity.MachineID, cur.notAfter())
	if err != nil {
		r.failed(ctx, "response", "invalid", err)
		return nil
	}
	if l.crashAt("after_answer") {
		return nil
	}
	if err := writePrivate(l.opts.Dir, CertFile, next.file, true); err != nil {
		r.failed(ctx, "save", "failed", &fileError{file: filepath.Join(l.opts.Dir, CertFile), check: "unwritable", err: err})
		return nil
	}
	if l.crashAt("after_rename") {
		return nil
	}
	// The enrolled pair stays a candidate: a new pair that has not started
	// by the agent's clock is not presented until it does.
	l.pairs.Store(&pairSet{enrolled: set.enrolled, renewed: next.pair})
	l.m.renewal(ctx, "renewed")
	l.log.InfoContext(ctx, "renew.completed", "serial", next.pair.serial(), "not_before", next.pair.leaf.NotBefore.UTC(),
		"not_after", next.pair.notAfter().UTC(), "previous_serial", cur.serial())
	if err := removePrivate(l.opts.Dir, RenewKeyFile); err != nil {
		// The next renewal sees a renew.key with the key in use and makes
		// a new one; the next start removes it.
		l.renewFileFailed(ctx, "cleanup", &fileError{file: filepath.Join(l.opts.Dir, RenewKeyFile), check: "unremovable", err: err})
	}
	return next.pair
}

// renewKey returns the key of the renewal in progress from [RenewKeyFile]
// (kept is true), or makes one and saves it there before anything is sent.
// A renew.key holding the key in use is the leftover of a renewal that
// was saved, and is replaced. A renew.key that cannot be used (damaged,
// open to other users) is never replaced: the error is a *fileError
// naming it and the owner's fix.
func renewKey(dir string, cur *certPair) (ed25519.PrivateKey, bool, error) {
	path := filepath.Join(dir, RenewKeyFile)
	key, kept, err := keptKey(dir, RenewKeyFile)
	if err != nil {
		return nil, false, keyFileError(path, err)
	}
	if !sameKey(key, cur.key) {
		return key, kept, nil
	}
	if err := removePrivate(dir, RenewKeyFile); err != nil {
		return nil, false, &fileError{file: path, check: "unremovable", err: err}
	}
	key, kept, err = keptKey(dir, RenewKeyFile)
	if err != nil {
		return nil, false, keyFileError(path, err)
	}
	return key, kept, nil
}

// crashAt stops the renewal at step when a test's hook says so.
func (l *Loop) crashAt(step string) bool {
	return l.opts.renewHook != nil && l.opts.renewHook(step)
}

// hookAt shows a test's hook a point it may watch or delay but never
// skip.
func (l *Loop) hookAt(step string) {
	if l.opts.renewHook != nil {
		_ = l.opts.renewHook(step)
	}
}
