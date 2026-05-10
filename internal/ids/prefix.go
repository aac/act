package ids

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxAmbiguousCandidates caps the number of full ids returned in
// ErrAmbiguousID; the user only needs enough candidates to retype an
// unambiguous prefix.
const MaxAmbiguousCandidates = 16

// ErrNotFound is returned by Resolve / ResolvePrefix when no full id matches
// the supplied prefix.
var ErrNotFound = errors.New("ids: no matching id")

// ErrAmbiguousID is returned by Resolve when a prefix matches more than one
// known full id. Candidates returns up to MaxAmbiguousCandidates matching
// full ids, sorted lexicographically.
type ErrAmbiguousID struct {
	Prefix     string
	candidates []string
}

// Candidates returns up to MaxAmbiguousCandidates candidate full ids matching
// the ambiguous prefix, sorted lexicographically.
func (e *ErrAmbiguousID) Candidates() []string {
	out := make([]string, len(e.candidates))
	copy(out, e.candidates)
	return out
}

// Error implements error.
func (e *ErrAmbiguousID) Error() string {
	return fmt.Sprintf("ids: ambiguous id prefix %q (%d candidates)", e.Prefix, len(e.candidates))
}

// Resolution describes the result of resolving a user-supplied id prefix. The
// FromMapping flag is set by callers that consult an import-mapping file
// before falling back to ResolvePrefix; this package itself never sets it.
type Resolution struct {
	Full        string
	FromMapping bool
}

// normalizeHex strips an optional `act-` prefix and lowercases the remaining
// hex characters. It does not validate that the result contains only hex.
func normalizeHex(input string) string {
	s := strings.ToLower(strings.TrimSpace(input))
	s = strings.TrimPrefix(s, "act-")
	return s
}

// hexTail returns the hex portion of a full id, panicking only if id is not
// `act-` prefixed (caller's invariant).
func hexTail(id string) string {
	return strings.TrimPrefix(strings.ToLower(id), "act-")
}

// ShortestUnique returns the shortest `act-<hex>` prefix of target (with at
// least MinShortHexLen hex chars) that is unique within allFullIDs. If target
// is not present in allFullIDs the function still returns the shortest prefix
// of target that no other id shares, with the same MinShortHexLen floor. If
// every prefix length up to the full id collides with another id (i.e.
// duplicate full ids in the input), the full target id is returned.
func ShortestUnique(allFullIDs []string, target string) string {
	tHex := hexTail(target)
	if tHex == "" {
		return target
	}
	// Count duplicate full-id matches so that a true full collision (two
	// entries with the same hex) is not silently treated as uniqueness.
	dupes := 0
	for _, id := range allFullIDs {
		if id == target {
			dupes++
		}
	}
	skipOne := dupes <= 1
	maxLen := len(tHex)
	for n := MinShortHexLen; n <= maxLen; n++ {
		p := tHex[:n]
		unique := true
		skipped := false
		for _, other := range allFullIDs {
			if skipOne && !skipped && other == target {
				skipped = true
				continue
			}
			oHex := hexTail(other)
			if strings.HasPrefix(oHex, p) {
				unique = false
				break
			}
		}
		if unique {
			return "act-" + p
		}
	}
	// Full collision: return the full target id verbatim.
	return target
}

// ShortestUniquePrefixes returns, for each full id in fullIDs, the shortest
// `act-<hex>` prefix (with floor MinShortHexLen) that uniquely identifies it
// among the input set. Duplicates in fullIDs are coalesced; the returned map
// has one entry per unique full id.
func ShortestUniquePrefixes(fullIDs []string) map[string]string {
	// Deduplicate while preserving first-seen ordering for determinism.
	seen := make(map[string]struct{}, len(fullIDs))
	uniq := make([]string, 0, len(fullIDs))
	for _, id := range fullIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	out := make(map[string]string, len(uniq))
	for _, id := range uniq {
		out[id] = ShortestUnique(uniq, id)
	}
	return out
}

// MinInputHexLen is the minimum number of hex characters required in a
// user-supplied id prefix for resolution to proceed. Any shorter input
// (including the bare "act-" prefix with no hex tail) is treated as
// not_found rather than matching everything.
//
// Note: this constant is intentionally distinct from MinShortHexLen (which
// governs display and id generation). Input resolution accepts any prefix of
// at least one hex character so that `act show act-c2` resolves when unique,
// even though the display/generation floor is 4 hex characters.
const MinInputHexLen = 1

// ResolvePrefix matches a user-supplied prefix (e.g. `act-bd7`, `bd70`,
// `BD70`, `act-c2`) against allFullIDs. It returns the unique full id when
// exactly one match exists; ambiguous=true when multiple match; found=false
// when no full id has a hex tail starting with the supplied hex prefix or
// when the effective hex prefix is empty (bare "act-" or whitespace only).
//
// Any prefix of at least MinInputHexLen hex characters is considered; the
// MinShortHexLen floor (4) applies only to display and id generation, not to
// user-supplied lookup.
func ResolvePrefix(allFullIDs []string, prefix string) (full string, ambiguous bool, found bool) {
	hex := normalizeHex(prefix)
	if len(hex) < MinInputHexLen {
		return "", false, false
	}
	matches := make([]string, 0, 4)
	for _, id := range allFullIDs {
		if strings.HasPrefix(hexTail(id), hex) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", false, false
	case 1:
		return matches[0], false, true
	default:
		return "", true, true
	}
}

// Resolve is the error-returning counterpart to ResolvePrefix; it implements
// the spec §Pre-import id resolution contract: ErrNotFound on zero matches,
// *ErrAmbiguousID on multiple matches, otherwise the unique full id.
//
// Any prefix of at least MinInputHexLen hex characters is accepted; the
// MinShortHexLen floor (4) applies only to display and id generation.
func Resolve(input string, known []string) (string, error) {
	hex := normalizeHex(input)
	if len(hex) < MinInputHexLen {
		return "", ErrNotFound
	}
	matches := make([]string, 0, 4)
	for _, id := range known {
		if strings.HasPrefix(hexTail(id), hex) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", ErrNotFound
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		if len(matches) > MaxAmbiguousCandidates {
			matches = matches[:MaxAmbiguousCandidates]
		}
		return "", &ErrAmbiguousID{Prefix: "act-" + hex, candidates: matches}
	}
}
