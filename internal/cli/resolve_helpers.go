package cli

import (
	"sort"
	"strings"

	"github.com/aac/act/internal/ids"
)

// ambiguousCandidates re-scans allIDs to compose the deterministic candidate
// list for the spec's `id_ambiguous` error envelope. The returned slice is
// sorted lexicographically and capped at ids.MaxAmbiguousCandidates so the
// user has enough information to retype an unambiguous prefix without
// flooding the terminal on a near-empty prefix.
//
// Spec note: the universal error table (spec-v2.md §1 "Error handling")
// places `id_ambiguous` at exit code 3 with `details.candidates[]`; the older
// per-section text at line 529 says exit 2 instead. We follow the universal
// table — exit 3 for both `id_ambiguous` and `issue_not_found` — because
// "world is in a state the caller didn't anticipate" maps cleanly onto the
// "world is wrong" exit-3 bucket, and consistency between the two
// closely-related id-resolution failures lets agents handle them with one
// branch.
func ambiguousCandidates(allIDs []string, input string) []string {
	hex := normalizePrefix(input)
	var candidates []string
	for _, id := range allIDs {
		if strings.HasPrefix(stripActPrefix(id), hex) {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	if len(candidates) > ids.MaxAmbiguousCandidates {
		candidates = candidates[:ids.MaxAmbiguousCandidates]
	}
	return candidates
}
