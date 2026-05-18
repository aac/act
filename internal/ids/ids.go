// Package ids generates and resolves task and op identifiers using the nonce
// protocol described in spec-v2.md §ID model.
//
// Identifiers have the form `act-<hex>` where the hex part is a prefix of a
// SHA-256 digest taken over the canonical-JSON encoding of the create-op
// payload (which itself contains the 16-byte crypto-random nonce as 32 hex
// chars). The prefix length starts at MinShortHexLen and grows by one hex
// character at a time on collision.
//
// Generation floor (MinShortHexLen) is distinct from the resolution floor
// (MinInputHexLen): historical ids generated under a smaller MinShortHexLen
// remain valid because the on-disk syntax accepts any length in [4,16] and
// prefix resolution accepts any length >= MinInputHexLen=1. Bumping
// MinShortHexLen only changes the size of *newly minted* ids.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/aac/act/internal/canonicaljson"
)

// MinShortHexLen is the starting length (in hex chars) of newly minted short
// ids. Widened from 4 to 6 (act-f9a0) ahead of the Phase 1 nested-repo
// migration: once contributors run several act states concurrently, birthday-
// collision math says marker collisions appear within a few hundred issues
// per state at 4 hex (~65k space). 6 hex (~16M space) buys two orders of
// magnitude of headroom before the same risk recurs.
//
// IMPORTANT — backwards compatibility. The on-disk id syntax (`idPattern`)
// still accepts the historical 4-hex form so existing ids in `.act/ops/`
// keep resolving. The Min*HexLen constants govern *generation* and the
// human-readable boundary for display; the *resolution* floor is
// MinInputHexLen=1 (see internal/ids/prefix.go), which has not changed.
const MinShortHexLen = 6

// MaxShortHexLen is the largest length we will grow a short id to before
// giving up.
//
// The 16-hex cap is authoritative per docs/spec-v2.md §ID model and the
// "Issue schema (folded state)" validation rules: IDs on disk are always the
// short form, "act-" + N hex chars where 4 <= N <= 16. The 64-char sha256
// digest (`full_hex` in the spec) is an internal intermediate value used to
// derive the short id; it is never written as an `id` or `issue_id` field.
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

// idPattern enforces the on-disk id syntax: "act-" prefix followed by 4..16
// lowercase hex chars. The upper bound (16) is authoritative per
// docs/spec-v2.md §ID model and matches MaxShortHexLen. The lower bound (4)
// is intentionally below MinShortHexLen (which is the *generation* floor —
// 6 since act-f9a0): historical ids minted under MinShortHexLen=4 must keep
// validating so existing repos can be read after the bump.
var idPattern = regexp.MustCompile(`^act-[0-9a-f]{4,16}$`)

// IsValidID reports whether s is a syntactically valid short id.
func IsValidID(s string) bool {
	return idPattern.MatchString(s)
}
