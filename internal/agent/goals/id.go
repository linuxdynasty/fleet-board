package goals

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"
	"time"
)

// MintID mints the observed-goal id (02h "Observed goals"): "obs-" plus
// the first 26 characters of the lowercase base32 (RFC 4648, no padding)
// of SHA-256 over machine_id, a NUL, the goal file's absolute path, a NUL
// and the first-seen time in Unix nanoseconds. The path alone is not
// enough: a file reused for the next goal after DONE is a new episode with
// a new first-seen time and so a new id. A hub-minted id never contains
// "-", so the two never collide (03k).
func MintID(machineID, path string, firstSeen time.Time) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", machineID, path, firstSeen.UnixNano())))
	return "obs-" + strings.ToLower(base32.StdEncoding.EncodeToString(sum[:])[:26])
}
