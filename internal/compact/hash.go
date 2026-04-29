package compact

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256Hex returns the hex-encoded sha256 digest of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
