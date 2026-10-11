package ingest

import (
	"strings"
	"unicode"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// Caps on the Snapshot text the hub stores (02e "Size limits": names and
// one-line text 256 bytes; PolicyInfo.error 256 bytes). A DiskFree path is
// a folder the agent reads, at most maxDisks of them (defaults.md "Resource
// sample reads").
const (
	maxText  = 256
	maxPath  = 1024
	maxDisks = 32
)

// Cleaned says what [Clean] changed, for the caller's telemetry.
type Cleaned struct {
	// Cut counts strings cut to their cap and dropped list entries.
	Cut int
	// Scrubbed counts strings the scrubber changed (a secret replaced).
	Scrubbed int
	// ScrubBytes is how many bytes the scrubber was handed in all: at most
	// the cap per field, so its cost is bounded whatever the agent sent.
	ScrubBytes int
}

// Clean makes snap's stored text safe, in place, on the stream's ingest
// goroutine before the writer job (never in the transaction): every string
// of the facts, the resources and the policy loses its control characters
// and invalid UTF-8 and is cut to its cap; the version strings and the
// policy error, the agent's free text, are cut to their cap and only then
// passed to the shared scrubber, which is never handed more than the cap
// (04b): a hostile 1 MiB string costs what a short one does. A secret that
// straddles the cap is cut in half first; the half left is no longer a
// whole secret and may stay as it is. The rest of the Snapshot is the later
// rows' to clean.
func Clean(snap *agentv1.Snapshot) Cleaned {
	var c Cleaned
	if f := snap.GetFacts(); f != nil {
		for _, p := range []*string{&f.Hostname, &f.Os, &f.Arch, &f.Timezone, &f.AgentBinaryHash, &f.BootId, &f.Username} {
			*p = c.text(*p, maxText)
		}
		for _, p := range []*string{&f.OsVersion, &f.AgentVersion, &f.TmuxVersion, &f.PolicySchemaVersion} {
			*p = c.scrub(*p)
		}
	}
	if r := snap.GetResources(); r != nil {
		if disks := r.GetDisk(); len(disks) > maxDisks {
			c.Cut += len(disks) - maxDisks
			r.Disk = disks[:maxDisks]
		}
		for _, d := range r.GetDisk() {
			if d != nil {
				d.Path = c.text(d.GetPath(), maxPath)
			}
		}
	}
	if p := snap.GetPolicy(); p != nil {
		p.Hash = c.text(p.GetHash(), maxText)
		p.SchemaVersion = c.scrub(p.GetSchemaVersion())
		p.Error = c.scrub(p.GetError())
	}
	return c
}

// text drops control characters and invalid UTF-8 from s and cuts it to
// limit bytes on a character boundary.
func (c *Cleaned) text(s string, limit int) string {
	out, cut := clip(s, limit)
	if cut {
		c.Cut++
	}
	return out
}

// scrub is s clipped to maxText, then the scrubber at maxText.
func (c *Cleaned) scrub(s string) string { return c.scrubTo(s, maxText) }

// scrubTo is s clipped to limit, then the scrubber at limit.
func (c *Cleaned) scrubTo(s string, limit int) string {
	if s == "" {
		return s
	}
	in, cut := clip(s, limit)
	c.ScrubBytes += len(in)
	r := scrub.Scrub(in, scrub.Options{MaxBytes: limit})
	if r.Changed {
		c.Scrubbed++
	}
	if cut || r.Cut {
		c.Cut++
	}
	return r.Text
}

// clip drops control characters, format characters (a direction override,
// a zero-width space: display spoofing, 02b), line and paragraph separators
// and invalid UTF-8 from s, then cuts it to limit bytes on a character
// boundary; cut reports the second. protobuf-go already refuses a proto3
// string with invalid UTF-8 when it decodes a message, so on the wire path
// ToValidUTF8 never fires; it guards a caller that builds a Snapshot itself.
func clip(s string, limit int) (out string, cut bool) {
	out = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if len(out) <= limit {
		return out, false
	}
	k := limit
	for k > 0 && !utf8.RuneStart(out[k]) {
		k--
	}
	return out[:k], true
}
