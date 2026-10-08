package ca

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// TokenPrefix starts every enroll token, so secret scanners can find one.
const TokenPrefix = "fbe1_"

// TokenVersion is the first byte of an enroll token's payload.
const TokenVersion = 1

// Enroll token lifetimes (defaults.md): the default, and the most a caller
// may ask for. A lifetime must also be more than zero.
const (
	DefaultTokenTTL = 15 * time.Minute
	MaxTokenTTL     = 24 * time.Hour
)

// secretLen is the number of random bytes in a token.
const secretLen = 32

// Token is a minted enroll token. Its text holds the only copy of the
// secret: show it once with [Token.Reveal], never log or store it. The hub
// stores SecretHash. Printing or logging a Token shows neither.
type Token struct {
	text string
	// SecretHash is SHA-256 of the 32 secret bytes, the value state.db keeps.
	SecretHash [32]byte
	// CAFingerprint is SHA-256 of the CA certificate DER, as in the token.
	CAFingerprint [32]byte
}

// NewToken mints `fbe1_` + unpadded base64url of 65 bytes: [TokenVersion],
// SHA-256 of caDER, and 32 secret bytes read from random (02g).
func NewToken(caDER []byte, random io.Reader) (Token, error) {
	if len(caDER) == 0 {
		return Token{}, errors.New("ca: token: no CA certificate")
	}
	payload := make([]byte, 1+sha256.Size+secretLen)
	payload[0] = TokenVersion
	fp := sha256.Sum256(caDER)
	copy(payload[1:], fp[:])
	secret := payload[1+sha256.Size:]
	if _, err := io.ReadFull(random, secret); err != nil {
		return Token{}, fmt.Errorf("ca: token secret: %w", err)
	}
	return Token{
		text:          TokenPrefix + base64.RawURLEncoding.EncodeToString(payload),
		SecretHash:    sha256.Sum256(secret),
		CAFingerprint: fp,
	}, nil
}

// ErrTokenMalformed means the text is not an `fbe1_` enroll token of this
// format. The hub answers it like a token it never issued.
var ErrTokenMalformed = errors.New("ca: not an enroll token")

// tokenTextLen is the length of a token: the prefix and 87 base64url
// characters for 65 bytes.
var tokenTextLen = len(TokenPrefix) + base64.RawURLEncoding.EncodedLen(1+sha256.Size+secretLen)

// ParseToken decodes an enroll token: the prefix, unpadded base64url of
// exactly 65 bytes, and [TokenVersion]. It returns the CA fingerprint and
// the secret's hash; the secret itself is dropped. Anything else is
// [ErrTokenMalformed], whose text never echoes the input.
func ParseToken(text string) (Token, error) {
	if len(text) != tokenTextLen || text[:len(TokenPrefix)] != TokenPrefix {
		return Token{}, ErrTokenMalformed
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(text[len(TokenPrefix):])
	if err != nil || len(payload) != 1+sha256.Size+secretLen || payload[0] != TokenVersion {
		return Token{}, ErrTokenMalformed
	}
	t := Token{text: text, SecretHash: sha256.Sum256(payload[1+sha256.Size:])}
	copy(t.CAFingerprint[:], payload[1:1+sha256.Size])
	return t, nil
}

// Reveal is the token text, the one place the secret leaves this type.
func (t Token) Reveal() string { return t.text }

const redacted = TokenPrefix + "[redacted]"

// String hides the secret from fmt.
func (Token) String() string { return redacted }

// GoString hides the secret from %#v.
func (Token) GoString() string { return redacted }

// LogValue hides the secret from slog.
func (Token) LogValue() slog.Value { return slog.StringValue(redacted) }
