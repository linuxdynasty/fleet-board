package attach

// maxOSCHold caps a held-back incomplete OSC sequence: a longer run
// without a terminator is emitted as-is rather than held forever.
const maxOSCHold = 64 << 10

// Stripper removes the escape sequences that act on the viewer's machine
// (01f J1): OSC 52 clipboard writes and OSC 8 hyperlinks. Everything else
// passes through untouched, including other OSC sequences. It is
// streaming-safe: a sequence split across Strip calls is held until its
// terminator (BEL or ESC backslash) arrives.
type Stripper struct {
	hold []byte
}

// Strip returns p with the viewer-acting sequences removed.
func (s *Stripper) Strip(p []byte) []byte {
	if len(s.hold) > 0 {
		joined := make([]byte, 0, len(s.hold)+len(p))
		joined = append(joined, s.hold...)
		joined = append(joined, p...)
		s.hold = s.hold[:0]
		p = joined
	}
	// A trailing ESC may open a sequence in the next chunk; hold it
	// rather than emitting a lone byte the sequence then misses.
	trail := len(p) > 0 && p[len(p)-1] == 0x1b
	if trail {
		p = p[:len(p)-1]
	}
	defer func() {
		if trail {
			s.hold = append(s.hold, 0x1b)
		}
	}()
	var out []byte
	for len(p) > 0 {
		i := indexOSC(p)
		if i < 0 {
			out = append(out, p...)
			break
		}
		out = append(out, p[:i]...)
		seq, rest, complete := splitOSC(p[i:])
		if !complete {
			if len(p[i:]) > maxOSCHold {
				// Pathological: emit rather than hold forever.
				out = append(out, p[i:]...)
				break
			}
			s.hold = append(s.hold, p[i:]...)
			break
		}
		if !stripOSC(seq) {
			out = append(out, seq...)
		}
		p = rest
	}
	return out
}

// indexOSC finds the next ESC ] opening an OSC sequence.
func indexOSC(p []byte) int {
	for i := 0; i+1 < len(p); i++ {
		if p[i] == 0x1b && p[i+1] == ']' {
			return i
		}
	}
	return -1
}

// splitOSC splits p (which opens with ESC ]) into its full sequence and
// the rest. complete is false when no terminator arrived yet.
func splitOSC(p []byte) (seq, rest []byte, complete bool) {
	for i := 2; i < len(p); i++ {
		if p[i] == 0x07 {
			return p[:i+1], p[i+1:], true
		}
		if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\' {
			return p[:i+2], p[i+2:], true
		}
	}
	return nil, p, false
}

// stripOSC reports whether the full OSC sequence acts on the viewer's
// machine: OSC 52 (clipboard) or OSC 8 (hyperlink).
func stripOSC(seq []byte) bool {
	rest := seq[2:]
	semi := 0
	for semi < len(rest) && rest[semi] != ';' {
		semi++
	}
	switch string(rest[:semi]) {
	case "52", "8":
		return true
	}
	return false
}
