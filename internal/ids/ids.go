// Package ids generates and resolves task and op identifiers using the nonce
// protocol described in spec-v2.md §ID model.
//
// Identifiers have the form `act-<hex>` where the hex part is a prefix of a
// SHA-256 digest taken over the canonical-JSON encoding of the create-op
// payload (which itself contains the 16-byte crypto-random nonce as 32 hex
// chars). The prefix length starts at 4 and grows by one hex character at a
// time on collision.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/aac/act/internal/canonicaljson"
)

// MinShortHexLen is the starting length (in hex chars) of the short id.
const MinShortHexLen = 4

// MaxShortHexLen is the largest length we will grow a short id to before
// giving up.
const MaxShortHexLen = 16

// NonceBytes is the number of bytes of crypto-random nonce embedded in each
// create-op payload.
const NonceBytes = 16

// CreatePayload is the subset of the create-op payload that participates in
// id derivation. Field tags mirror the spec's payload key names so that the
// canonical-JSON encoding matches what writers would produce.
type CreatePayload struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Priority    int      `json:"priority"`
	Type        string   `json:"type"`
	Parent      string   `json:"parent"`
	Accept      []string `json:"accept"`
	Nonce       string   `json:"nonce"`
}

// NewNonce returns a fresh 32-character lowercase-hex nonce drawn from
// crypto/rand.
func NewNonce() (string, error) {
	var b [NonceBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ids: crypto/rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// hashPayload returns the lowercase-hex SHA-256 digest of the canonical-JSON
// encoding of payload.
func hashPayload(payload CreatePayload) (string, error) {
	canon, err := canonicaljson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("ids: canonicaljson: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// DeriveID returns the default-length short id (`act-` plus the first
// MinShortHexLen hex chars of the canonical-JSON SHA-256 digest of payload).
// The caller is responsible for collision detection; see PickUnique.
func DeriveID(payload CreatePayload) (string, error) {
	return ExtendID(payload, MinShortHexLen)
}

// ExtendID returns the short id truncated to n hex chars. n must be in
// [MinShortHexLen, MaxShortHexLen].
func ExtendID(payload CreatePayload, n int) (string, error) {
	if n < MinShortHexLen || n > MaxShortHexLen {
		return "", fmt.Errorf("ids: hex length %d out of range [%d,%d]", n, MinShortHexLen, MaxShortHexLen)
	}
	full, err := hashPayload(payload)
	if err != nil {
		return "", err
	}
	return "act-" + full[:n], nil
}

// PickUnique returns the shortest non-colliding id for payload. It tries
// increasing prefix lengths from MinShortHexLen up to MaxShortHexLen,
// querying exists for each candidate. exists must return true if and only
// if the supplied id is already taken by a different issue.
func PickUnique(payload CreatePayload, exists func(id string) bool) (string, error) {
	for n := MinShortHexLen; n <= MaxShortHexLen; n++ {
		candidate, err := ExtendID(payload, n)
		if err != nil {
			return "", err
		}
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("ids: all prefix lengths %d..%d collide", MinShortHexLen, MaxShortHexLen)
}

var idPattern = regexp.MustCompile(`^act-[0-9a-f]{4,16}$`)

// IsValidID reports whether s is a syntactically valid short id.
func IsValidID(s string) bool {
	return idPattern.MatchString(s)
}
