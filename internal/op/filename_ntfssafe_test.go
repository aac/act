package op

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// TestOpFilename_NoColon asserts that newly-written op filenames contain
// no ':' character. This is the load-bearing claim for act-2f3d: ':' is
// reserved in NTFS paths, so any op filename containing ':' breaks
// `git checkout` on Windows hosts before any Go code runs.
//
// The test exercises Filename across every valid op type to make sure
// the no-colon guarantee holds for the entire write surface, not just
// the smoke case.
func TestOpFilename_NoColon(t *testing.T) {
	for opType := range ValidOpTypes {
		e := aprilEnv(t)
		e.OpType = opType
		name := Filename(e)
		if strings.Contains(name, ":") {
			t.Errorf("op_type %q: Filename = %q contains ':' (NTFS-unsafe)", opType, name)
		}
	}
}

// TestOpFilename_SortOrder asserts that filenames produced by the
// NTFS-safe layout sort lexically in the same order as their underlying
// HLC walls. Fold ordering by filename is the contract this test
// protects (spec §Op file naming) — switching ':' to '-' must not
// perturb it.
//
// Constructed inputs span a year boundary and an hour boundary because
// those are the boundaries most likely to be affected by a separator
// change that altered character ordering.
func TestOpFilename_SortOrder(t *testing.T) {
	walls := []int64{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		time.Date(2026, 1, 1, 23, 59, 59, 999_000_000, time.UTC).UnixMilli(),
		time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC).UnixMilli(),
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}
	type pair struct {
		wall int64
		name string
	}
	pairs := make([]pair, 0, len(walls))
	for _, w := range walls {
		e := goodEnvelope()
		e.HLC.Wall = w
		// Vary the logical so distinct envelopes hash distinctly.
		e.HLC.Logical = uint32(w % 1000)
		pairs = append(pairs, pair{wall: w, name: Filename(e)})
	}
	// Sort by filename and check that the wall order matches.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })
	for i := 1; i < len(pairs); i++ {
		if pairs[i].wall < pairs[i-1].wall {
			t.Fatalf("filename sort produced wall order %d before %d (filenames: %q, %q)",
				pairs[i].wall, pairs[i-1].wall, pairs[i-1].name, pairs[i].name)
		}
	}
}

// TestOpFilename_BackwardsCompatibleParse asserts that legacy-form
// filenames (pre-act-2f3d, containing ':' in the time component) still
// parse cleanly. The ops directory is append-only, so existing files on
// disk must remain readable after the writer switches to the NTFS-safe
// shape. Without this, the first fold after upgrade would fail on every
// pre-existing op.
func TestOpFilename_BackwardsCompatibleParse(t *testing.T) {
	// Hand-crafted legacy filename: same shape as Filename() emits, but
	// with ':' in the time component (the pre-act-2f3d form).
	legacy := "2026-04-15T12:34:56.789Z-deadbeef-create.json"
	ts, hashHex, opType, err := Parse(legacy)
	if err != nil {
		t.Fatalf("Parse(legacy) returned error: %v", err)
	}
	wantMs := time.Date(2026, 4, 15, 12, 34, 56, 789_000_000, time.UTC).UnixMilli()
	if got := ts.UnixMilli(); got != wantMs {
		t.Errorf("Parse(legacy) timestamp = %d ms, want %d ms", got, wantMs)
	}
	if hashHex != "deadbeef" {
		t.Errorf("Parse(legacy) hash = %q, want %q", hashHex, "deadbeef")
	}
	if opType != "create" {
		t.Errorf("Parse(legacy) op_type = %q, want %q", opType, "create")
	}

	// Also: a new-form filename round-trips through Parse (mirrors
	// TestParse_Roundtrip but pins the no-colon assertion next to the
	// legacy assertion so a future regression flips both arms together).
	e := aprilEnv(t)
	newForm := Filename(e)
	if strings.Contains(newForm, ":") {
		t.Fatalf("new-form filename %q unexpectedly contains ':'", newForm)
	}
	ts2, _, _, err := Parse(newForm)
	if err != nil {
		t.Fatalf("Parse(new-form) returned error: %v", err)
	}
	if ts2.UnixMilli() != e.HLC.Wall {
		t.Errorf("Parse(new-form) timestamp = %d, want %d", ts2.UnixMilli(), e.HLC.Wall)
	}
}
