package inventory

import (
	"strings"
	"unicode/utf8"
)

// maxSummaryBytes caps an E5 PaneSummary's last_lines (01 E5, 02e).
const maxSummaryBytes = 4096

// stripANSI drops terminal escape sequences a capture holds: CSI (ESC [
// ... final byte) and OSC (ESC ] ... BEL or ESC \). Anything else,
// including a lone ESC, is kept: cleanText drops the controls later, and
// a summary must never eat text it does not understand.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: parameters, intermediates, one final byte
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				j++
			}
			i = j
		case ']': // OSC: to BEL or ESC backslash
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// renderScreen turns capture-pane lines into reportable text (E5, and the
// E10 read path row 18's CapturePane handler answers with): ANSI stripped
// unless kept, every control dropped per line (02e, through cleanText),
// trailing blank lines cut, then capped at maxBytes on a rune boundary.
// It reports whether anything was cut. Screen text is untrusted: capped
// and control-stripped, never scrubbed (row-7 ruling).
// cleanKeepANSI is cleanText that passes well-formed CSI and OSC sequences
// through byte for byte (E10 keep_ansi): every other control is still
// dropped, and the cap counts the sequences' bytes too.
func cleanKeepANSI(s string, limit int) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s) && b.Len() < limit; {
		if s[i] != 0x1b || i+1 >= len(s) {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				i += size
				continue
			}
			if b.Len()+size > limit {
				break
			}
			b.WriteString(s[i : i+size])
			i += size
			continue
		}
		end := -1
		switch s[i+1] {
		case '[':
			j := i + 2
			for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
				j++
			}
			for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
				j++
			}
			if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
				end = j + 1
			}
		case ']':
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					end = j + 1
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					end = j + 2
					break
				}
				j++
			}
		}
		if end < 0 { // a lone ESC: a control like any other
			i++
			continue
		}
		if b.Len()+(end-i) > limit {
			break
		}
		b.WriteString(s[i:end])
		i = end
	}
	return b.String()
}

func renderScreen(lines []string, keepANSI bool, maxBytes int) (string, bool) {
	kept := make([]string, 0, len(lines))
	for _, ln := range lines {
		if keepANSI {
			kept = append(kept, cleanKeepANSI(ln, len(ln)))
			continue
		}
		kept = append(kept, cleanText(stripANSI(ln), len(ln)))
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 0 {
		return "", false
	}
	text := strings.Join(kept, "\n")
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(text) <= maxBytes {
		return text, false
	}
	var b strings.Builder
	b.Grow(maxBytes)
	for _, r := range text {
		if b.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String(), true
}
