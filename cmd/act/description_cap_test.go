package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDocClaim_DescriptionCap_FileAndAppendFlags pins docs/spec.md "Length
// caps" for description at the `act` argv boundary (act-993498):
// --description-file (create and update) and --description-append-file
// accept exactly 1048576 bytes and reject 1048577 naming the cap, and an
// inline --description-append is capped on its MERGED result — the
// description the op would write — not on the fragment. The numbers are the
// spec's, as literals, so moving op.MaxDescriptionLen alone fails here.
func TestDocClaim_DescriptionCap_FileAndAppendFlags(t *testing.T) {
	dir := blocksSite(t)
	atCapFile := writeTempFile(t, "at.md", strings.Repeat("f", 1048576))
	overCapFile := writeTempFile(t, "over.md", strings.Repeat("f", 1048577))
	const fileCapMsg = "1048576-byte description cap"

	// create --description-file
	if out, stderr, code := runActIn(t, dir, "create", "file at cap", "--description-file", atCapFile, "--json"); code != 0 {
		t.Fatalf("create --description-file at 1048576 bytes: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	out, _, code := runActIn(t, dir, "create", "file over cap", "--description-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("create --description-file at 1048577 bytes: exit %d (want 2), out=%s (want %q)", code, out, fileCapMsg)
	}

	id := createBlocksIssue(t, dir, "cap target")

	// update --description-file
	out, _, code = runActIn(t, dir, "update", id, "--description-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("update --description-file at 1048577 bytes: exit %d (want 2), out=%s", code, out)
	}
	// --description-append-file
	out, _, code = runActIn(t, dir, "update", id, "--description-append-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("--description-append-file at 1048577 bytes: exit %d (want 2), out=%s", code, out)
	}

	// Inline --description-append, capped on the merged result: 1048572
	// existing bytes + "\n\n" + "xy" is exactly 1048576.
	seed := writeTempFile(t, "seed.md", strings.Repeat("s", 1048572))
	if out, stderr, code := runActIn(t, dir, "update", id, "--description-file", seed, "--json"); code != 0 {
		t.Fatalf("seed description: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	if out, stderr, code := runActIn(t, dir, "update", id, "--description-append", "xy", "--json"); code != 0 {
		t.Fatalf("--description-append landing on exactly 1048576 bytes: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	out, _, code = runActIn(t, dir, "update", id, "--description-append", "q", "--json")
	if code == 0 {
		t.Fatalf("--description-append past 1048576 bytes accepted: %s", out)
	}
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse error envelope: %v\n%s", err, out)
	}
	if want := "update_field.description length 1048579 > 1048576 bytes"; !strings.Contains(env.Message, want) {
		t.Fatalf("--description-append over-cap error does not name the merged length and cap (%q): %s", want, env.Message)
	}
}
