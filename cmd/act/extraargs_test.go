package main

import (
	"strings"
	"testing"
)

// act-a79d66: `act log <id> "message"` silently swallowed the message. Two
// agents ran it intending to append an annotation; act took the id, ignored
// the stray positional, printed the op count and exited 0. Four notes across
// three trackers were lost with no error anywhere.
//
// The class is wider than `act log` — every read-only verb had the same
// shape — so these tests table over all of them.

// TestDocClaim_ReadOnlyVerbs_RejectStrayPositionals is the boundary
// assertion for the fix: a write-looking invocation of a read-only verb
// exits 2 with a message that names both the offending argument and the
// command the caller probably wanted.
func TestDocClaim_ReadOnlyVerbs_RejectStrayPositionals(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "stray-arg probe")

	cases := []struct {
		name string
		args []string
		// wantHint is a fragment of the actionable half of the message —
		// the part that turns a refusal into a signpost.
		wantHint string
	}{
		{
			name:     "log with a message",
			args:     []string{"log", id, "a note I meant to append"},
			wantHint: "--description-append",
		},
		{
			name:     "show with a message",
			args:     []string{"show", id, "a note I meant to append"},
			wantHint: "--description-append",
		},
		{
			name:     "ready with an id",
			args:     []string{"ready", id},
			wantHint: "--under",
		},
		{
			name:     "mine with an identity",
			args:     []string{"mine", "some-node-id"},
			wantHint: "--as",
		},
		{
			name:     "list with a status word",
			args:     []string{"list", "open"},
			wantHint: "act search",
		},
		{
			name:     "search with an unquoted multi-word query",
			args:     []string{"search", "two", "words"},
			wantHint: "quote a multi-word query",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runActIn(t, dir, tc.args...)
			if code != 2 {
				t.Fatalf("act %v: exit %d, want 2 (silent swallow is the bug)\nstdout=%s\nstderr=%s",
					tc.args, code, stdout, stderr)
			}
			if !strings.Contains(stderr, "unexpected extra argument") {
				t.Errorf("act %v: stderr does not name the extra argument:\n%s", tc.args, stderr)
			}
			if !strings.Contains(stderr, tc.wantHint) {
				t.Errorf("act %v: stderr missing the actionable hint %q:\n%s", tc.args, tc.wantHint, stderr)
			}
		})
	}

	// `act help workflow` documents the rejection so an agent learns it
	// before tripping over it. Assert the live help text, not the source.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	if !strings.Contains(help, "is rejected (exit 2) rather than") {
		t.Errorf("act help workflow does not document the stray-arg rejection:\n%s", help)
	}
}

// TestReadOnlyVerbs_CorrectInvocationsStillSucceed is the other half: the
// rejection must not catch legitimate calls. Without this, "reject extra
// positionals" could be implemented as "reject positionals" and every test
// above would still pass.
func TestReadOnlyVerbs_CorrectInvocationsStillSucceed(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "control probe")

	cases := [][]string{
		{"log", id},
		{"show", id},
		{"ready"},
		{"mine"},
		{"list"},
		{"list", "--status", "open"},
		{"search", "control"},
		// A genuinely multi-word query, correctly quoted into one arg.
		{"search", "control probe"},
	}
	for _, args := range cases {
		if _, stderr, code := runActIn(t, dir, args...); code != 0 {
			t.Errorf("act %v: exit %d, want 0; stderr=%s", args, code, stderr)
		}
	}
}

// TestStrayArgRejection_NamesTheOffendingValue checks the message quotes the
// actual argument. The lost annotations were multi-word strings; a caller
// scanning output needs to see their text echoed back to recognise what got
// dropped.
func TestStrayArgRejection_NamesTheOffendingValue(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "echo probe")

	const note = "verified on the mini, parity holds"
	_, stderr, code := runActIn(t, dir, "log", id, note)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, note) {
		t.Errorf("stderr should echo the dropped text %q so the caller recognises it:\n%s", note, stderr)
	}
}
