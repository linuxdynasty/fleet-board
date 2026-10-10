package conn

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	tokenPrefix  = "fbe1_"
	tokenVersion = 0x01
	// tokenBytes is the decoded payload: version, CA SHA-256, secret (02g).
	tokenBytes = 1 + 32 + 32
	redacted   = "fbe1_[REDACTED]"
)

// DefaultPort is the hub's agent listener port (defaults.md, 02j), used
// when a hub address has none.
const DefaultPort = "7443"

var (
	// ErrTokenMalformed means the text is not an enroll token of 02g. Its
	// text never echoes the token.
	ErrTokenMalformed = errors.New("conn: the enroll token is not a fleet-board token (fbe1_...)")
	// ErrNoHub means no hub address was given and there was no way to
	// look for one ([Options].Discover is nil).
	ErrNoHub = errors.New("conn: no hub address: pass --hub host[:port]")
	// ErrHubAddress means the hub address is not host or host:port.
	ErrHubAddress = errors.New("conn: the hub address is not host or host:port")
)

// Token is a parsed enroll token. It prints as a redacted placeholder in
// every format and log; only [Token.Reveal] gives the text, to send it.
type Token struct {
	// CAFingerprint is the SHA-256 of the fleet CA certificate the token
	// names: the hub must present that CA (02g A1 step 2).
	CAFingerprint [32]byte
	text          string
}

// ParseToken reads "fbe1_" + unpadded base64url of 65 bytes: version 0x01,
// the CA SHA-256 and the 32-byte secret (02g). Any other text is
// [ErrTokenMalformed].
func ParseToken(text string) (Token, error) {
	body, ok := strings.CutPrefix(text, tokenPrefix)
	if !ok || len(body) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return Token{}, ErrTokenMalformed
	}
	for i := range len(body) {
		c := body[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return Token{}, ErrTokenMalformed
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil || len(raw) != tokenBytes || raw[0] != tokenVersion {
		return Token{}, ErrTokenMalformed
	}
	t := Token{text: text}
	copy(t.CAFingerprint[:], raw[1:33])
	return t, nil
}

// Reveal returns the token text, to put in EnrollRequest.token only.
func (t Token) Reveal() string { return t.text }

func (Token) String() string { return redacted }

// GoString keeps %#v from printing the secret.
func (Token) GoString() string { return redacted }

// LogValue keeps slog from printing the secret.
func (Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// HubAddress checks a hub address the owner typed, adds [DefaultPort]
// when it has no port, and gives it one spelling (see below). An empty
// address is [ErrNoHub].
func HubAddress(s string) (string, error) {
	if s == "" {
		return "", ErrNoHub
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if net.ParseIP(s) == nil && strings.Contains(s, ":") {
			return "", fmt.Errorf("%w: %q", ErrHubAddress, s)
		}
		host, port = s, DefaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%w: %q has no valid port", ErrHubAddress, s)
	}
	// One spelling per address: lower case, no trailing dot, an IP in its
	// canonical form, the port without leading zeros.
	host = strings.ToLower(strings.TrimRight(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	} else if !hostName(host) {
		return "", fmt.Errorf("%w: %q", ErrHubAddress, s)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// hostName accepts letters, digits, dots and hyphens, at most 253 bytes.
func hostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := range len(h) {
		c := h[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
