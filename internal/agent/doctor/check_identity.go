package doctor

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// File names in the state folder (conn owns the layout; doctor only looks).
const (
	identityName = "identity.json"
	certName     = "cert.pem"
	renewKeyName = "renew.key"
	enrollKey    = "enroll.key"
)

// identityFile is the part of identity.json doctor reads. The key and the
// salt are not fields here, so decoding never keeps them.
type identityFile struct {
	Version      int       `json:"version"`
	MachineID    string    `json:"machine_id"`
	CertNotAfter time.Time `json:"cert_not_after"`
	CertPEM      string    `json:"cert_pem"`
	CAPEM        string    `json:"ca_pem"`
}

// problemKind says why a file in the state folder could not be used.
type problemKind int

const (
	problemMissing problemKind = iota + 1
	problemLoose
	problemDamaged
	problemUnreadable
)

// fileProblem is a state-folder file that could not be used, with the mode
// when the problem is a loose one.
type fileProblem struct {
	kind problemKind
	mode string
}

// privateFile checks that path is a regular file owned by the user with no
// group or other access (what conn.readPrivate insists on) and reads it.
func (e *Env) privateFile(path string) ([]byte, *fileProblem) {
	p, err := lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &fileProblem{kind: problemMissing}
	case err != nil:
		return nil, &fileProblem{kind: problemUnreadable}
	case !p.Regular:
		return nil, &fileProblem{kind: problemDamaged}
	case !e.owned(p) || p.perm()&0o077 != 0:
		return nil, &fileProblem{kind: problemLoose, mode: modeText(p)}
	}
	data, err := readSmall(path)
	if err != nil {
		return nil, &fileProblem{kind: problemDamaged}
	}
	return data, nil
}

// Identity reads identity.json once.
func (e *Env) loadIdentity() (identityFile, *fileProblem) {
	if e.identityRead {
		return e.identity, e.identityErr
	}
	e.identityRead = true
	data, prob := e.privateFile(filepath.Join(e.StateDir(), identityName))
	if prob != nil {
		e.identityErr = prob
		return e.identity, prob
	}
	var id identityFile
	if err := json.Unmarshal(data, &id); err != nil || id.Version != 1 || id.MachineID == "" || id.CertPEM == "" || id.CAPEM == "" {
		e.identityErr = &fileProblem{kind: problemDamaged}
		return e.identity, e.identityErr
	}
	e.identity = id
	return id, nil
}

func identityCheck(_ context.Context, e *Env) Outcome {
	id, prob := e.loadIdentity()
	path := filepath.Join(e.StateDir(), identityName)
	if prob == nil {
		return ok("enrolled as machine " + id.MachineID)
	}
	switch prob.kind {
	case problemMissing:
		if _, err := lstat(filepath.Join(e.StateDir(), enrollKey)); err == nil {
			return fail("not enrolled: an enroll was started and not finished (enroll.key is kept for a retry)",
				"fleet-agent enroll --token <token>, with the same token as before: the saved key lets the hub replay its answer")
		}
		return fail("not enrolled: there is no identity.json in "+e.StateDir(), "fleet-agent enroll --token <token>")
	case problemLoose:
		return fail(fmt.Sprintf("identity.json is mode %s or not yours: other users could read the key", prob.mode), "chmod 600 "+shellQuote(path))
	case problemUnreadable:
		return fail("identity.json cannot be read", "check its permissions")
	case problemDamaged:
	}
	return fail("identity.json is damaged or from another version",
		"remove only identity.json (keep agent.db, which holds the install id), then fleet-agent enroll --token <new token>")
}

// notEnrolled is the outcome of a check that needs an identity when there is none.
func notEnrolled() Outcome {
	return unknown("no usable identity (not enrolled, or identity.json is unusable)", "see the identity check")
}

// leafOf returns the first CERTIFICATE block of pemText; other blocks (a
// private key) are skipped unread.
func leafOf(pemText []byte) (*x509.Certificate, bool) {
	for {
		var blk *pem.Block
		blk, pemText = pem.Decode(pemText)
		if blk == nil {
			return nil, false
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		return c, err == nil
	}
}

// renewedCert is cert.pem as doctor judged it.
type renewedCert struct {
	present bool
	problem *fileProblem
	// why is the sentence for a certificate that is there but not usable.
	why  string
	leaf *x509.Certificate
}

// loadRenewed judges cert.pem against the identity. The private key block
// in the file is never parsed: doctor cannot say the key matches, and says so.
func (e *Env) loadRenewed() renewedCert {
	if e.renewedRead {
		return e.renewed
	}
	e.renewedRead = true
	r := &e.renewed
	data, prob := e.privateFile(filepath.Join(e.StateDir(), certName))
	if prob != nil {
		if prob.kind != problemMissing {
			r.present, r.problem = true, prob
		}
		return *r
	}
	r.present = true
	leaf, okLeaf := leafOf(data)
	id, idProb := e.loadIdentity()
	switch {
	case !okLeaf:
		r.why = "cert.pem is damaged: it holds no certificate doctor can read"
	case idProb != nil:
		r.leaf = leaf // judged against nothing; the caller checks the identity first
	case leaf.Subject.CommonName != id.MachineID:
		r.why = "cert.pem is for another machine"
	default:
		ca, okCA := leafOf([]byte(id.CAPEM))
		if !okCA || ca.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
			r.why = "cert.pem is not signed by the pinned CA"
			break
		}
		r.leaf = leaf
	}
	return *r
}

func certFileCheck(_ context.Context, e *Env) Outcome {
	path := filepath.Join(e.StateDir(), certName)
	r := e.loadRenewed()
	if !r.present {
		return ok("no renewed certificate yet (normal until the first renewal at two thirds of the certificate's life)")
	}
	if r.problem != nil {
		if r.problem.kind == problemLoose {
			return fail(fmt.Sprintf("cert.pem is mode %s or not yours: other users could read its key", r.problem.mode), "chmod 600 "+shellQuote(path))
		}
		return warn("cert.pem cannot be read as a certificate file", "the agent keeps using the enrolled certificate; the next renewal replaces cert.pem")
	}
	if _, idProb := e.loadIdentity(); idProb != nil {
		return notEnrolled()
	}
	if r.why != "" {
		return warn(r.why, "the agent keeps using the enrolled certificate; the next renewal replaces cert.pem")
	}
	return ok(fmt.Sprintf("cert.pem holds a valid renewed certificate for this machine that ends %s; its key was not read, so a key that does not match it is not checked",
		r.leaf.NotAfter.UTC().Format(time.DateOnly)))
}

func renewKeyCheck(_ context.Context, e *Env) Outcome {
	path := filepath.Join(e.StateDir(), renewKeyName)
	p, err := lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ok("no renewal in progress (no renew.key)")
	case err != nil:
		return unknown("cannot look at renew.key", "check the permissions of the state folder")
	case !p.Regular || !e.owned(p) || p.perm()&0o077 != 0:
		return fail(fmt.Sprintf("renew.key is mode %s or not a private file: the renewal is blocked until it is fixed", modeText(p)), "chmod 600 "+shellQuote(path)+", or remove it if you do not need it")
	}
	return ok("renew.key is there: a renewal is in progress or was cut off, and the next renewal check finishes it; its contents were not read")
}

// certLife is the validity window of one certificate.
type certLife struct{ notBefore, notAfter time.Time }

// lastEndingCert is the certificate that ends last, the one the agent follows
// (03 W1, "Certificate expired"): the enrolled one or a valid renewed one.
// It returns the identity problem when identity.json cannot be used, and nil
// with no problem when it holds no certificate doctor can read.
func lastEndingCert(e *Env) (*certLife, *fileProblem) {
	id, prob := e.loadIdentity()
	if prob != nil {
		return nil, prob
	}
	var best *certLife
	consider := func(p certLife) {
		if best == nil || p.notAfter.After(best.notAfter) {
			best = &p
		}
	}
	if leaf, okLeaf := leafOf([]byte(id.CertPEM)); okLeaf {
		consider(certLife{leaf.NotBefore, leaf.NotAfter})
	} else if !id.CertNotAfter.IsZero() {
		consider(certLife{notAfter: id.CertNotAfter})
	}
	if r := e.loadRenewed(); r.leaf != nil && r.why == "" && r.problem == nil {
		consider(certLife{r.leaf.NotBefore, r.leaf.NotAfter})
	}
	return best, nil
}

// certExpiryCheck judges the certificate that ends last by this machine's
// clock. Past two thirds of its life the agent should have renewed it.
func certExpiryCheck(_ context.Context, e *Env) Outcome {
	best, prob := lastEndingCert(e)
	if prob != nil {
		return notEnrolled()
	}
	if best == nil {
		return unknown("identity.json holds no certificate doctor can read", "see the identity check")
	}
	now := e.Now()
	end := best.notAfter.UTC().Format(time.DateOnly)
	left := best.notAfter.Sub(now)
	switch {
	case left <= 0:
		return fail(fmt.Sprintf("the certificate that ends last expired on %s, by this machine's clock", end),
			"re-enroll: remove only identity.json (keep agent.db), then fleet-agent enroll --token <new token>")
	case !best.notBefore.IsZero() && !now.Before(best.notBefore.Add(best.notAfter.Sub(best.notBefore)/3*2)):
		return warn(fmt.Sprintf("the certificate that ends last runs out %s (in %d days) and is past two thirds of its life, so the agent should have renewed it", end, int(left.Hours()/24)),
			"check that fleet-agent run is running and connected to the hub (see the connection check); it renews while connected")
	}
	return ok(fmt.Sprintf("the certificate that ends last runs out %s (in %d days)", end, int(left.Hours()/24)))
}
