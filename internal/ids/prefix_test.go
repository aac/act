package ids

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestShortestUniqueDistinctAtFloor(t *testing.T) {
	// Each id is unique at the MinShortHexLen floor (no shared prefix at
	// that length); shortening should land exactly at the floor.
	all := []string{"act-abc12ddeadbeef", "act-abc23ddeadbeef"}
	for _, id := range all {
		got := ShortestUnique(all, id)
		want := "act-" + hexTail(id)[:MinShortHexLen]
		if got != want {
			t.Errorf("ShortestUnique(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestShortestUniqueGrowsOnSharedFloor(t *testing.T) {
	// Both ids share their first MinShortHexLen hex chars; shortening grows
	// one past the floor to disambiguate.
	all := []string{"act-abc1232deadbeef", "act-abc1233deadbeef"}
	for _, id := range all {
		got := ShortestUnique(all, id)
		want := "act-" + hexTail(id)[:MinShortHexLen+1]
		if got != want {
			t.Errorf("ShortestUnique(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestShortestUniqueFullCollisionReturnsFull(t *testing.T) {
	// Two entries with identical full hex (upstream invariant violation).
	all := []string{"act-abcd1234", "act-abcd1234"}
	got := ShortestUnique(all, "act-abcd1234")
	if got != "act-abcd1234" {
		t.Errorf("ShortestUnique on duplicates = %q, want %q", got, "act-abcd1234")
	}
}

// TestShortestUniqueHistoricalShortID exercises the backwards-compat path:
// an id whose hex tail is below MinShortHexLen (a historical 4-hex id from
// before act-f9a0) cannot be shortened further; ShortestUnique returns the
// id verbatim rather than skipping the loop entirely.
func TestShortestUniqueHistoricalShortID(t *testing.T) {
	all := []string{"act-aaaa"}
	got := ShortestUnique(all, all[0])
	if got != "act-aaaa" {
		t.Errorf("ShortestUnique on historical 4-hex id = %q, want %q", got, "act-aaaa")
	}
}

func TestShortestUniqueSingleton(t *testing.T) {
	all := []string{"act-deadbeef00"}
	got := ShortestUnique(all, all[0])
	want := "act-" + hexTail(all[0])[:MinShortHexLen]
	if got != want {
		t.Errorf("ShortestUnique singleton = %q, want %q", got, want)
	}
}

func TestShortestUniquePrefixesMixed(t *testing.T) {
	// Six ids: two share their MinShortHexLen-prefix, four others are unique
	// at the floor.
	all := []string{
		"act-abc1232deadbeef",
		"act-abc1233deadbeef",
		"act-11112233aaaa",
		"act-22223344bbbb",
		"act-33334455cccc",
		"act-44445566dddd",
	}
	got := ShortestUniquePrefixes(all)
	if len(got) != len(all) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(all))
	}
	// The two shared-floor ids grow to MinShortHexLen+1 to disambiguate.
	wantShared0 := "act-" + hexTail(all[0])[:MinShortHexLen+1]
	wantShared1 := "act-" + hexTail(all[1])[:MinShortHexLen+1]
	if got[all[0]] != wantShared0 {
		t.Errorf("short(%q) = %q, want %q", all[0], got[all[0]], wantShared0)
	}
	if got[all[1]] != wantShared1 {
		t.Errorf("short(%q) = %q, want %q", all[1], got[all[1]], wantShared1)
	}
	for _, id := range all[2:] {
		want := "act-" + hexTail(id)[:MinShortHexLen]
		if got[id] != want {
			t.Errorf("short(%q) = %q, want %q", id, got[id], want)
		}
	}
}

func TestResolvePrefixExact(t *testing.T) {
	all := []string{"act-bd70cafebabe", "act-1396deadbeef"}
	full, amb, found := ResolvePrefix(all, "act-bd70")
	if !found || amb || full != "act-bd70cafebabe" {
		t.Errorf("ResolvePrefix(act-bd70) = (%q,%v,%v), want (act-bd70cafebabe,false,true)", full, amb, found)
	}
}

func TestResolvePrefixAmbiguous(t *testing.T) {
	all := []string{"act-abc12deadbeef", "act-abc13deadbeef"}
	full, amb, found := ResolvePrefix(all, "abc1")
	if !found || !amb || full != "" {
		t.Errorf("ResolvePrefix(abc1) = (%q,%v,%v), want (\"\",true,true)", full, amb, found)
	}
}

func TestResolvePrefixMissing(t *testing.T) {
	all := []string{"act-bd70cafebabe"}
	full, amb, found := ResolvePrefix(all, "ffff")
	if found || amb || full != "" {
		t.Errorf("ResolvePrefix(ffff) = (%q,%v,%v), want (\"\",false,false)", full, amb, found)
	}
}

func TestResolvePrefixAcceptsBothForms(t *testing.T) {
	all := []string{"act-bd70cafebabe"}
	for _, in := range []string{"act-bd70", "bd70", "BD70", "act-BD70", "  act-bd70  "} {
		full, amb, found := ResolvePrefix(all, in)
		if !found || amb || full != "act-bd70cafebabe" {
			t.Errorf("ResolvePrefix(%q) = (%q,%v,%v)", in, full, amb, found)
		}
	}
}

func TestResolvePrefixEmptySet(t *testing.T) {
	full, amb, found := ResolvePrefix(nil, "bd70")
	if found || amb || full != "" {
		t.Errorf("ResolvePrefix(nil) = (%q,%v,%v), want (\"\",false,false)", full, amb, found)
	}
}

func TestResolvePrefixTooShort(t *testing.T) {
	// Only a completely empty hex tail (bare "act-" or whitespace) is
	// rejected; sub-MinShortHexLen prefixes (1 through MinShortHexLen-1 hex
	// chars) are accepted and resolve normally. MinInputHexLen=1 is the
	// floor.
	all := []string{"act-bd70cafebabe"}
	for _, in := range []string{"", "act-", "  ", "act-   "} {
		full, amb, found := ResolvePrefix(all, in)
		if found || amb || full != "" {
			t.Errorf("ResolvePrefix(%q) = (%q,%v,%v), want (\"\",false,false)", in, full, amb, found)
		}
	}
}

func TestResolvePrefixSubMinShortHexLen(t *testing.T) {
	// Sub-MinShortHexLen prefixes are accepted when they uniquely identify
	// one issue. This is the fix for act-6fca: "prefix ok" docs were right,
	// the resolver just wasn't honouring them for short prefixes. After
	// act-f9a0 widened MinShortHexLen from 4 to 6, this also serves as the
	// regression test for 1-5-char prefix resolution.
	all := []string{"act-bd70cafebabe"}
	for _, in := range []string{"b", "bd", "bd7", "bd70", "bd70c", "act-b", "act-bd", "act-bd7"} {
		full, amb, found := ResolvePrefix(all, in)
		if !found || amb || full != "act-bd70cafebabe" {
			t.Errorf("ResolvePrefix(%q) = (%q,%v,%v), want (act-bd70cafebabe,false,true)", in, full, amb, found)
		}
	}
}

func TestResolveErrNotFound(t *testing.T) {
	if _, err := Resolve("ffff", []string{"act-bd70cafebabe"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve missing = %v, want ErrNotFound", err)
	}
	if _, err := Resolve("", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve empty = %v, want ErrNotFound", err)
	}
}

func TestResolveAmbiguousCandidatesSorted(t *testing.T) {
	all := []string{
		"act-abc13deadbeef",
		"act-abc12deadbeef",
		"act-abc11deadbeef",
	}
	_, err := Resolve("abc1", all)
	var amb *ErrAmbiguousID
	if !errors.As(err, &amb) {
		t.Fatalf("Resolve abc1 err = %v, want *ErrAmbiguousID", err)
	}
	cands := amb.Candidates()
	if len(cands) != 3 {
		t.Fatalf("len(candidates) = %d, want 3", len(cands))
	}
	if !sort.StringsAreSorted(cands) {
		t.Errorf("candidates not sorted: %v", cands)
	}
	if amb.Error() == "" {
		t.Errorf("Error() returned empty string")
	}
}

func TestResolveAmbiguousCandidatesCapped(t *testing.T) {
	all := make([]string, 0, MaxAmbiguousCandidates+5)
	for i := 0; i < MaxAmbiguousCandidates+5; i++ {
		// `act-aaaa` + 4 more hex chars derived from i, ensuring shared `aaaa` 4-prefix.
		suf := []byte{
			"0123456789abcdef"[(i>>4)&0xf],
			"0123456789abcdef"[i&0xf],
		}
		all = append(all, "act-aaaa"+string(suf)+"00")
	}
	_, err := Resolve("aaaa", all)
	var amb *ErrAmbiguousID
	if !errors.As(err, &amb) {
		t.Fatalf("Resolve aaaa err = %v, want *ErrAmbiguousID", err)
	}
	if got := len(amb.Candidates()); got != MaxAmbiguousCandidates {
		t.Errorf("capped candidates = %d, want %d", got, MaxAmbiguousCandidates)
	}
}

func TestResolveCaseInsensitive(t *testing.T) {
	all := []string{"act-a1b2c3d4"}
	got, err := Resolve("A1B2", all)
	if err != nil || got != "act-a1b2c3d4" {
		t.Errorf("Resolve(A1B2) = (%q,%v), want (act-a1b2c3d4,nil)", got, err)
	}
}

func TestResolveUnique(t *testing.T) {
	all := []string{"act-bd70cafebabe", "act-1396deadbeef"}
	got, err := Resolve("bd70", all)
	if err != nil || got != "act-bd70cafebabe" {
		t.Errorf("Resolve(bd70) = (%q,%v)", got, err)
	}
}

// TestShortestRoundTrip is the spec's property: every short id produced by
// ShortestUniquePrefixes resolves uniquely back to its full id via Resolve.
func TestShortestRoundTrip(t *testing.T) {
	for trial := 0; trial < 20; trial++ {
		n := 1 + trial*7
		all := make([]string, 0, n)
		seen := make(map[string]struct{}, n)
		for len(all) < n {
			var b [10]byte
			if _, err := rand.Read(b[:]); err != nil {
				t.Fatalf("rand: %v", err)
			}
			id := "act-" + hex.EncodeToString(b[:])
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, id)
		}
		short := ShortestUniquePrefixes(all)
		for _, id := range all {
			s, ok := short[id]
			if !ok {
				t.Fatalf("missing short for %q", id)
			}
			// floor enforcement
			if hl := len(strings.TrimPrefix(s, "act-")); hl < MinShortHexLen {
				t.Errorf("short %q for %q below floor", s, id)
			}
			got, err := Resolve(s, all)
			if err != nil {
				t.Errorf("Resolve(%q) err = %v", s, err)
				continue
			}
			if got != id {
				t.Errorf("Resolve(%q) = %q, want %q", s, got, id)
			}
		}
	}
}
