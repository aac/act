package cli

// Tests pinning the spec contract for ambiguous-id-prefix exit codes.
//
// Per spec-v2.md §"Universal exit codes" (lines 515-519) and
// §"Pre-import id resolution" (line 529), an ambiguous prefix is a usage
// error: the caller supplied a non-unique argument. Every command that
// accepts an `<id>` MUST exit 2 (not 3) when the input prefix matches
// two or more known full ids.
//
// This file is the regression net for act-8dcd. Coverage spans:
//   - `act show <id>`       (already in show_test.go; we leave that one
//                            in place and add a parallel assertion here
//                            so the surface is documented in one spot)
//   - `act close <id>`
//   - `act update <id>`
//   - `act dep add <child> <parent>` (both child and parent sub-cases)
//   - `act ready --under <id>`
//
// Resolution happens before any op write, so `makeRepoWithAct` (which
// only stages `.git/` + `.act/ops/` and skips `.act/config.json`) is
// sufficient for `show` and `ready`. The other commands also stat
// `.act/config.json` before resolution, so they reuse `makeCreateRepo`
// (which initialises a real repo + config) and drop create-op files
// manually to control the ids.

import (
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/config"
)

// seedCreateOpForAmbiguousTest writes a create-op file for `id` into a
// fully-initialised .act tree. It uses the same envelope helper as the
// show tests so the on-disk schema validates.
func seedCreateOpForAmbiguousTest(t *testing.T, root, id, title string, wallMs int64, logical uint32) {
	t.Helper()
	env := makeShowCreateEnv(t, id, wallMs, logical, title)
	writeOpFile(t, root, env, "2026-04", id+"-create.json")
}

// TestAmbiguousPrefix_ShowExits2 mirrors TestRunShow_AmbiguousPrefix but
// reads as a checklist entry alongside the other commands.
func TestAmbiguousPrefix_ShowExits2(t *testing.T) {
	root := makeRepoWithAct(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 2 {
		t.Fatalf("show: exit = %d, want 2", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("show: type = %T, want ShowErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("show: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("show: candidates = %d, want 2", len(e.Candidates))
	}
}

// TestAmbiguousPrefix_CloseExits2 seeds two create ops sharing a 4-char
// prefix and asserts `act close act-abcd` exits 2 with id_ambiguous.
func TestAmbiguousPrefix_CloseExits2(t *testing.T) {
	root := makeCreateRepo(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)

	out, code := RunClose(root, CloseOptions{ID: "act-abcd"})
	if code != 2 {
		t.Fatalf("close: exit = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("close: type = %T, want CloseErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("close: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("close: candidates = %d, want 2", len(e.Candidates))
	}
}

// TestAmbiguousPrefix_UpdateExits2 covers the primary id arg on `act
// update`. The `runUpdateClaim` and `--dep-rm` resolution paths share the
// same helper; covering one suffices to pin the contract.
func TestAmbiguousPrefix_UpdateExits2(t *testing.T) {
	root := makeCreateRepo(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)

	desc := "doc"
	out, code := RunUpdate(root, UpdateOptions{ID: "act-abcd", Description: &desc})
	if code != 2 {
		t.Fatalf("update: exit = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(UpdateErrorOutput)
	if !ok {
		t.Fatalf("update: type = %T, want UpdateErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("update: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("update: candidates = %d, want 2", len(e.Candidates))
	}
}

// TestAmbiguousPrefix_DepAddChildExits2 pins the child sub-case on
// `act dep add <child> <parent>`.
func TestAmbiguousPrefix_DepAddChildExits2(t *testing.T) {
	root := makeCreateRepo(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)
	// A second, unambiguous id to act as the parent.
	seedCreateOpForAmbiguousTest(t, root, "act-eeee0000", "parent", 1700000000002, 0)

	out, code := RunDepAdd(root, DepAddOptions{
		Child:    "act-abcd",
		Parent:   "act-eeee0000",
		EdgeType: "blocks",
	})
	if code != 2 {
		t.Fatalf("dep add child: exit = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(DepAddErrorOutput)
	if !ok {
		t.Fatalf("dep add child: type = %T, want DepAddErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("dep add child: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("dep add child: candidates = %d, want 2", len(e.Candidates))
	}
}

// TestAmbiguousPrefix_DepAddParentExits2 pins the parent sub-case. The
// child is unambiguous; the parent is the prefix that fans out.
func TestAmbiguousPrefix_DepAddParentExits2(t *testing.T) {
	root := makeCreateRepo(t)
	seedCreateOpForAmbiguousTest(t, root, "act-eeee0000", "child", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000001, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000002, 0)

	out, code := RunDepAdd(root, DepAddOptions{
		Child:    "act-eeee0000",
		Parent:   "act-abcd",
		EdgeType: "blocks",
	})
	if code != 2 {
		t.Fatalf("dep add parent: exit = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(DepAddErrorOutput)
	if !ok {
		t.Fatalf("dep add parent: type = %T, want DepAddErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("dep add parent: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("dep add parent: candidates = %d, want 2", len(e.Candidates))
	}
}

// TestAmbiguousPrefix_ReadyUnderExits2 pins the `ready --under` path
// that the issue calls out (ready.go:173). Resolution happens against
// folded index rows, so we use the same direct-write seed as the other
// commands.
func TestAmbiguousPrefix_ReadyUnderExits2(t *testing.T) {
	root := makeRepoWithAct(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)

	out, code := RunReady(root, ReadyOptions{Under: "act-abcd"})
	if code != 2 {
		t.Fatalf("ready --under: exit = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(ReadyErrorOutput)
	if !ok {
		t.Fatalf("ready --under: type = %T, want ReadyErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("ready --under: error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("ready --under: candidates = %d, want 2", len(e.Candidates))
	}
}

// silence the imports if the helpers move; keeps the file robust to
// future refactors that might collapse helpers into another package.
var _ = config.Layout
var _ = filepath.Join
