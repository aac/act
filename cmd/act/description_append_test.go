package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// act-a79d66, second half: the reason `act log <id> "message"` looked like
// the right place to put a note is that act had no note-append path. The
// only way to add a line to a description was `act update --description-file`,
// which forces a read-modify-write of the whole body.

// TestDocClaim_Update_DescriptionAppendAppends pins the --description-append
// flag-help claim: the text is appended to the existing description,
// separated by a blank line, rather than replacing it.
func TestDocClaim_Update_DescriptionAppendAppends(t *testing.T) {
	dir := blocksSite(t)

	out, _, code := runActIn(t, dir, "create", "append probe", "--description", "original body", "--json")
	if code != 0 {
		t.Fatalf("create: exit %d; out=%s", code, out)
	}
	// Parse the id of the issue we just gave a description to.
	// (createBlocksIssue can't be reused here: it doesn't take a
	// --description, and the append needs an existing body to append to.)
	marker := `"id":"`
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no id in create output: %s", out)
	}
	rest := out[i+len(marker):]
	target := rest[:strings.IndexByte(rest, '"')]

	if _, stderr, code := runActIn(t, dir, "update", target, "--description-append", "first appended note"); code != 0 {
		t.Fatalf("--description-append: exit %d; stderr=%s", code, stderr)
	}

	shown, _, code := runActIn(t, dir, "show", target, "--full")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	// The original body survives — this is an append, not a replace. That
	// distinction is the whole point; a replace would look like success
	// while destroying the description.
	if !strings.Contains(shown, "original body") {
		t.Errorf("append destroyed the existing description:\n%s", shown)
	}
	if !strings.Contains(shown, "first appended note") {
		t.Errorf("appended text missing:\n%s", shown)
	}
	if !strings.Contains(shown, "original body\n\nfirst appended note") {
		t.Errorf("expected a blank line between body and appended note; got:\n%s", shown)
	}

	// A second append stacks below the first, so successive annotations
	// read as paragraphs rather than accumulating separator whitespace.
	if _, stderr, code := runActIn(t, dir, "update", target, "--description-append", "second appended note"); code != 0 {
		t.Fatalf("second append: exit %d; stderr=%s", code, stderr)
	}
	shown, _, _ = runActIn(t, dir, "show", target, "--full")
	if !strings.Contains(shown, "first appended note\n\nsecond appended note") {
		t.Errorf("second append not stacked with a single blank line; got:\n%s", shown)
	}

	// `act help workflow` is where an agent looking for "how do I leave a
	// note on a ticket" lands. Assert the live help names the flag — the
	// stray-arg rejection points callers here, so a drift that drops it
	// would leave the error message citing a command act no longer has.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	if !strings.Contains(help, "act update <id> --description-append") {
		t.Errorf("act help workflow does not document --description-append:\n%s", help)
	}
}

// TestUpdate_DescriptionAppendOnEmptyDescription: appending to an issue with
// no description yields the note alone, with no leading blank lines.
func TestUpdate_DescriptionAppendOnEmptyDescription(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "empty-description probe")

	if _, stderr, code := runActIn(t, dir, "update", id, "--description-append", "sole note"); code != 0 {
		t.Fatalf("append: exit %d; stderr=%s", code, stderr)
	}
	shown, _, _ := runActIn(t, dir, "show", id, "--full")
	if !strings.Contains(shown, "description: sole note") {
		t.Errorf("expected the note alone with no leading blank lines; got:\n%s", shown)
	}
}

// TestUpdate_DescriptionAppendConflicts: append and the two replacing forms
// are contradictory instructions, so the pairing is rejected rather than
// silently ordered.
func TestUpdate_DescriptionAppendConflicts(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "conflict probe")

	cases := [][]string{
		{"update", id, "--description", "replace me", "--description-append", "add me"},
		{"update", id, "--description-file", "-", "--description-append", "add me"},
	}
	for _, args := range cases {
		_, stderr, code := runActIn(t, dir, args...)
		if code != 2 {
			t.Errorf("act %v: exit %d, want 2; stderr=%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "mutually exclusive") {
			t.Errorf("act %v: stderr should say the flags conflict:\n%s", args, stderr)
		}
	}
}

// act-0bec25: `--description-append` covers the one-line note, but a
// session record is often a paragraph with quotes, newlines and backticks
// in it — exactly the payload that is hostile to a single shell argument.
// Without a file twin the caller is pushed back to the read-modify-write
// through `--description-file` that the append flag exists to remove.

// writeTempFile writes content to a file under t.TempDir() and returns its
// path.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestDocClaim_Update_DescriptionAppendFile pins the
// --description-append-file flag-help claim: the contents of the file are
// appended to the existing description (and `-` reads stdin), mirroring
// --description-file's relationship to --description.
func TestDocClaim_Update_DescriptionAppendFile(t *testing.T) {
	dir := blocksSite(t)

	out, _, code := runActIn(t, dir, "create", "append-file probe", "--description", "original body", "--json")
	if code != 0 {
		t.Fatalf("create: exit %d; out=%s", code, out)
	}
	marker := `"id":"`
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no id in create output: %s", out)
	}
	rest := out[i+len(marker):]
	target := rest[:strings.IndexByte(rest, '"')]

	// The payload is deliberately the shape that motivated the flag:
	// multi-line, with the quoting a single argv token handles badly.
	note := "SESSION 2026-08-02\n\nRan `go test ./...` — green.\nAndrew's call: \"ship it\"."
	path := writeTempFile(t, "note.md", note)

	if _, stderr, code := runActIn(t, dir, "update", target, "--description-append-file", path); code != 0 {
		t.Fatalf("--description-append-file: exit %d; stderr=%s", code, stderr)
	}

	shown, _, code := runActIn(t, dir, "show", target, "--full")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	// Append, not replace: the pre-existing body survives.
	if !strings.Contains(shown, "original body") {
		t.Errorf("append-file destroyed the existing description:\n%s", shown)
	}
	if !strings.Contains(shown, "original body\n\nSESSION 2026-08-02") {
		t.Errorf("expected one blank line between body and file contents; got:\n%s", shown)
	}
	// The interior structure of the file arrives verbatim — no
	// re-wrapping, no quote mangling.
	if !strings.Contains(shown, "Andrew's call: \"ship it\".") {
		t.Errorf("file contents were not appended verbatim:\n%s", shown)
	}

	// `-` reads stdin, the piped form a dispatch prompt actually uses.
	stdinNote := "appended from stdin"
	cmd := exec.Command(actBinary(t), "update", target, "--description-append-file", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdinNote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--description-append-file -: %v: %s", err, out)
	}
	shown, _, _ = runActIn(t, dir, "show", target, "--full")
	if !strings.Contains(shown, "Andrew's call: \"ship it\".\n\nappended from stdin") {
		t.Errorf("stdin append not stacked below the prior note; got:\n%s", shown)
	}
}

// TestUpdate_DescriptionAppendFileConflicts: the file twin extends, so it
// contradicts both replace flags AND its own inline form (which would
// leave the order of two appends unstated).
func TestUpdate_DescriptionAppendFileConflicts(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "append-file conflict probe")
	path := writeTempFile(t, "note.md", "note")

	cases := [][]string{
		{"update", id, "--description", "replace me", "--description-append-file", path},
		{"update", id, "--description-file", path, "--description-append-file", path},
		{"update", id, "--description-append", "inline", "--description-append-file", path},
	}
	for _, args := range cases {
		_, stderr, code := runActIn(t, dir, args...)
		if code != 2 {
			t.Errorf("act %v: exit %d, want 2; stderr=%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "mutually exclusive") {
			t.Errorf("act %v: stderr should say the flags conflict:\n%s", args, stderr)
		}
	}
}

// TestUpdate_DescriptionAppendFileMissingPath: a missing file is exit 3
// with the flag naming itself, not --description-file impersonating it.
// The flag name in the message is what tells a caller which of the two
// paths it fat-fingered.
func TestUpdate_DescriptionAppendFileMissingPath(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "missing-file probe")

	_, stderr, code := runActIn(t, dir, "update", id, "--description-append-file", filepath.Join(t.TempDir(), "nope.md"))
	if code != 3 {
		t.Errorf("missing file: exit %d, want 3; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--description-append-file") {
		t.Errorf("error message does not name the flag that failed:\n%s", stderr)
	}
	if strings.Contains(stderr, "--description-file ") {
		t.Errorf("error message blames --description-file:\n%s", stderr)
	}
}
