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
// Spec note (corrected per act-8dcd): the universal exit-code table
// (spec-v2.md §"Universal exit codes", lines 515-519) is the load-bearing
// authority and it places `id_ambiguous` at exit 2: the caller supplied a
// non-unique argument, which is a usage error. The §"Pre-import id
// resolution" text at line 529 confirms this — multiple matches → exit 2.
// The error-envelope summary table at line 901 lists exit 3, which is the
// stale entry; the universal table wins. `issue_not_found` stays at exit 3
// (environment error: the world doesn't contain what the caller asked for).
// The two id-resolution failures intentionally diverge: ambiguous = "fix
// your input", not_found = "your input is fine but the world is wrong."
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
