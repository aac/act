// Tests in this file pin the canonical auto-commit subject format
// across every act write op (create, claim, update_field, close, dep-add,
// redact, reopen, delete) plus the cascade-tombstone batch case.
//
// Format: `act-op: (act-XXXX) <op_type>` (or `... +N` for batched ops).
//
// History (act-d3a5): three different inline templates produced three
// different shapes within a single session — `act- create`, `act-act-XXXX:
// claim …`, `act-op: (act-XXXX) close` — none of which matched on the wire.
// The previous test bytes asserted on op-file payloads, not on the commit
// subject, so the bug shipped. These tests assert directly on `git log -1
// --format=%s`, the same string `act doctor orphan-close` greps.
package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/aac/act/internal/op"
)

// stubEnvelope returns a minimal op.Envelope whose only load-bearing
// fields for commit-message construction are IssueID and OpType. Other
// fields are zero-valued; the unit tests do not need a valid envelope.
func stubEnvelope(issueID, opType string) op.Envelope {
	return op.Envelope{IssueID: issueID, OpType: opType}
}

// canonicalSubjectRE matches `act-op: (act-XXXX) <op_type>` with optional
// `+N` batch-count suffix. The `act-` prefix appears exactly once inside
// the parens and exactly once at the start; any other shape is a bug.
var canonicalSubjectRE = regexp.MustCompile(
	`^act-op: \(act-[0-9a-f]{4,16}\) [a-z_]+( \+\d+)?$`,
)

// assertCanonicalSubject is the single shared assertion. It checks:
//   - The `act-op: ` prefix is present exactly once.
//   - The parenthesized `(act-XXXX)` marker is present (doctor's grep key).
//   - There is no `act-act-` double-prefix anywhere in the subject.
//   - The subject ends with the expected op_type (modulo the `+N` suffix).
func assertCanonicalSubject(t *testing.T, root, wantOpType string) string {
	t.Helper()
	subj := strings.TrimSpace(runOut(t, root, "git", "log", "-1", "--format=%s"))
	if strings.Contains(subj, "act-act-") {
		t.Fatalf("subject %q contains double-prefix `act-act-`", subj)
	}
	if !canonicalSubjectRE.MatchString(subj) {
		t.Fatalf("subject %q does not match canonical %q", subj, canonicalSubjectRE)
	}
	// Op-type assertion: the canonical format places op_type after the
	// `(...)` marker. Match either `... <type>` or `... <type> +N`.
	if !strings.Contains(subj, ") "+wantOpType) {
		t.Fatalf("subject %q missing op_type %q after `)`", subj, wantOpType)
	}
	// Doctor's grep key: literal `(act-XXXX)` for some 4-hex prefix.
	if !regexp.MustCompile(`\(act-[0-9a-f]{4,}\)`).MatchString(subj) {
		t.Fatalf("subject %q missing parenthesized `(act-XXXX)` marker", subj)
	}
	return subj
}

// TestCommitFormat_Create asserts the canonical subject for `act create`.
func TestCommitFormat_Create(t *testing.T) {
	root := makeCreateRepo(t)
	if _, code := RunCreate(root, CreateOptions{Title: "x", Type: "task"}); code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	assertCanonicalSubject(t, root, "create")
}

// TestCommitFormat_Claim asserts the canonical subject for the bare
// claim path (driven through `act update --claim`). Historically this
// produced `act-act-XXXX: claim <assignee>` (double prefix). The fix
// routes through buildClaimCommitMessage in internal/claim/claim.go.
func TestCommitFormat_Claim(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "x", Type: "task"})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	id := out.(CreateResult).ID

	if _, code := RunUpdate(root, UpdateOptions{ID: id, Claim: true, Isolated: true}); code != 0 {
		t.Fatalf("RunUpdate --claim: code=%d", code)
	}
	assertCanonicalSubject(t, root, "claim")
}

// TestCommitFormat_UpdateField asserts the canonical subject for
// per-field updates (`act update --status`, `--priority`, etc.). The
// envelope op_type is `update_field`.
func TestCommitFormat_UpdateField(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "x", Type: "task"})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	id := out.(CreateResult).ID
	prio := 0
	if _, code := RunUpdate(root, UpdateOptions{ID: id, Priority: &prio}); code != 0 {
		t.Fatalf("RunUpdate --priority: code=%d", code)
	}
	assertCanonicalSubject(t, root, "update_field")
}

// TestCommitFormat_Close asserts the canonical subject for `act close`.
// Doctor's orphan-close grep specifically keys on this commit.
func TestCommitFormat_Close(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("RunClose: code=%d", code)
	}
	assertCanonicalSubject(t, root, "close")
}

// TestCommitFormat_DepAdd asserts the canonical subject for `act dep
// add`. The envelope op_type is `add_dep`.
func TestCommitFormat_DepAdd(t *testing.T) {
	root := makeCreateRepo(t)
	parent, code := RunCreate(root, CreateOptions{Title: "p", Type: "task"})
	if code != 0 {
		t.Fatalf("seed parent: code=%d", code)
	}
	child, code := RunCreate(root, CreateOptions{Title: "c", Type: "task"})
	if code != 0 {
		t.Fatalf("seed child: code=%d", code)
	}
	if _, code := RunDepAdd(root, DepAddOptions{
		Child:    child.(CreateResult).ID,
		Parent:   parent.(CreateResult).ID,
		EdgeType: "blocks",
	}); code != 0 {
		t.Fatalf("RunDepAdd: code=%d", code)
	}
	assertCanonicalSubject(t, root, "add_dep")
}

// TestCommitFormat_Redact asserts the canonical subject for `act redact`.
func TestCommitFormat_Redact(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:       "secret",
		Type:        "task",
		Description: "leaks: foo",
	})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	id := out.(CreateResult).ID

	if _, code := RunRedact(root, RedactOptions{
		ID:          id,
		FieldPath:   "description",
		Replacement: "[REDACTED]",
	}); code != 0 {
		t.Fatalf("RunRedact: code=%d", code)
	}
	assertCanonicalSubject(t, root, "redact")
}

// TestCommitFormat_Reopen asserts the canonical subject for `act reopen`.
func TestCommitFormat_Reopen(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("RunClose: code=%d", code)
	}
	if _, code := RunReopen(root, ReopenOptions{ID: id, Reason: "regression"}); code != 0 {
		t.Fatalf("RunReopen: code=%d", code)
	}
	assertCanonicalSubject(t, root, "reopen")
}

// TestCommitFormat_Delete asserts the canonical subject for the
// single-op (non-cascade) `act delete` path; envelope op_type is
// `tombstone`.
func TestCommitFormat_Delete(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "x", Type: "task"})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	id := out.(CreateResult).ID
	if _, code := RunDelete(root, DeleteOptions{ID: id}); code != 0 {
		t.Fatalf("RunDelete: code=%d", code)
	}
	assertCanonicalSubject(t, root, "tombstone")
}

// TestCommitFormat_DeleteCascade asserts the cascade-tombstone batch
// commit gets the `+N` suffix while still matching the canonical shape
// (so doctor's grep continues to match the parent).
func TestCommitFormat_DeleteCascade(t *testing.T) {
	root := makeCreateRepo(t)
	parent, code := RunCreate(root, CreateOptions{Title: "p", Type: "epic"})
	if code != 0 {
		t.Fatalf("seed parent: code=%d", code)
	}
	parentID := parent.(CreateResult).ID
	if _, code := RunCreate(root, CreateOptions{Title: "c1", Type: "task", Parent: parentID}); code != 0 {
		t.Fatalf("seed c1: code=%d", code)
	}
	if _, code := RunCreate(root, CreateOptions{Title: "c2", Type: "task", Parent: parentID}); code != 0 {
		t.Fatalf("seed c2: code=%d", code)
	}
	if _, code := RunDelete(root, DeleteOptions{ID: parentID, Cascade: true}); code != 0 {
		t.Fatalf("RunDelete --cascade: code=%d", code)
	}
	subj := assertCanonicalSubject(t, root, "tombstone")
	if !strings.Contains(subj, " +2") {
		t.Errorf("cascade subject %q missing `+2` (parent + 2 children = 3 ops)", subj)
	}
}

// TestBuildOpCommitMessage_Unit asserts the canonical builder produces
// the exact expected bytes for representative inputs. This is the
// regression seat-belt for act-d3a5: any future change to the format
// must update this test deliberately.
func TestBuildOpCommitMessage_Unit(t *testing.T) {
	cases := []struct {
		name    string
		issueID string
		opType  string
		want    string
	}{
		{"create_long_id", "act-d3a5b1c2e3f4a5b6", "create", "act-op: (act-d3a5) create"},
		{"claim", "act-3cdcb1c2e3f4a5b6", "claim", "act-op: (act-3cdc) claim"},
		{"close", "act-0d9d11112222aaaa", "close", "act-op: (act-0d9d) close"},
		{"update_field", "act-aaaabbbbccccdddd", "update_field", "act-op: (act-aaaa) update_field"},
		{"add_dep", "act-1234567890abcdef", "add_dep", "act-op: (act-1234) add_dep"},
		// Short id (already canonical-length) is passed through verbatim.
		{"short_passthrough", "act-d3a5", "create", "act-op: (act-d3a5) create"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := stubEnvelope(c.issueID, c.opType)
			got := BuildOpCommitMessage(env)
			if got != c.want {
				t.Errorf("BuildOpCommitMessage(%q,%q) = %q, want %q",
					c.issueID, c.opType, got, c.want)
			}
		})
	}
}

// TestBuildBatchCommitMessage_Unit covers the cascade/batch suffix
// behavior: count==1 is byte-identical to BuildOpCommitMessage; count>1
// appends `+N` (extra ops beyond the head). count<=0 is treated as 1
// (defensive).
func TestBuildBatchCommitMessage_Unit(t *testing.T) {
	env := stubEnvelope("act-d3a5b1c2e3f4a5b6", "tombstone")
	cases := []struct {
		count int
		want  string
	}{
		{0, "act-op: (act-d3a5) tombstone"},
		{1, "act-op: (act-d3a5) tombstone"},
		{2, "act-op: (act-d3a5) tombstone +1"},
		{3, "act-op: (act-d3a5) tombstone +2"},
		{42, "act-op: (act-d3a5) tombstone +41"},
	}
	for _, c := range cases {
		got := BuildBatchCommitMessage(env, c.count)
		if got != c.want {
			t.Errorf("count=%d: got %q, want %q", c.count, got, c.want)
		}
	}
}

// TestShortIssueID asserts the helper truncates known-canonical full ids
// to `act-XXXX` (8 chars) and passes shorter ids through verbatim.
func TestShortIssueID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"act-d3a5b1c2e3f4a5b6", "act-d3a5"},
		{"act-3cdc", "act-3cdc"}, // already canonical length
		{"act-d3", "act-d3"},     // shorter than canonical → passthrough
		{"", ""},
		{"act-", "act-"},
	}
	for _, c := range cases {
		if got := ShortIssueID(c.in); got != c.want {
			t.Errorf("ShortIssueID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
