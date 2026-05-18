package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// makeRedactRepoWithIssue seeds a git repo + .act/ with one create op
// (synthesised via RunCreate) and returns (repoRoot, issueID).
func makeRedactRepoWithIssue(t *testing.T) (string, string) {
	t.Helper()
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:       "leaks",
		Type:        "task",
		Description: "secret-data-here",
	})
	if code != 0 {
		t.Fatalf("seed: code = %d, out=%+v", code, out)
	}
	return root, out.(CreateResult).ID
}

// TestRunRedact_HappyPath: redacting a description writes one redact op,
// auto-commits, and exits 0 with changed:true.
func TestRunRedact_HappyPath(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)

	out, code := RunRedact(root, RedactOptions{
		ID:          id,
		FieldPath:   "description",
		Replacement: "<redacted>",
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(RedactResult)
	if !ok {
		t.Fatalf("output type = %T, want RedactResult", out)
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
	if !res.Committed {
		t.Errorf("Committed = false, want true")
	}
	if !res.Changed {
		t.Errorf("Changed = false, want true")
	}
	if res.FieldPath != "description" {
		t.Errorf("FieldPath = %q, want %q", res.FieldPath, "description")
	}

	// Exactly one redact op file under the issue's shard.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-redact.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 redact op file, got %d: %v", len(matches), matches)
	}
}

// TestRunRedact_Idempotent: a second redact of the same field returns
// {changed:false} exit 0; no new op is written (per spec line 1042 +
// act-g008 acceptance).
func TestRunRedact_Idempotent(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)

	if _, code := RunRedact(root, RedactOptions{ID: id, FieldPath: "description"}); code != 0 {
		t.Fatalf("first redact: code = %d", code)
	}
	out, code := RunRedact(root, RedactOptions{ID: id, FieldPath: "description"})
	if code != 0 {
		t.Fatalf("second redact: code = %d, out=%+v", code, out)
	}
	res, ok := out.(RedactNoChange)
	if !ok {
		t.Fatalf("output type = %T, want RedactNoChange", out)
	}
	if res.Changed {
		t.Errorf("Changed = true, want false")
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}

	// Still exactly one redact op on disk (the second was elided).
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-redact.json"))
	if len(matches) != 1 {
		t.Errorf("expected exactly 1 redact op (idempotent), got %d: %v", len(matches), matches)
	}
}

// TestRunRedact_InvalidFieldPath: an unknown field path is exit 2 with
// bad_flag.
func TestRunRedact_InvalidFieldPath(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)

	out, code := RunRedact(root, RedactOptions{ID: id, FieldPath: "bogus_field"})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
	res, ok := out.(RedactErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want RedactErrorOutput", out)
	}
	if res.Error != "bad_flag" {
		t.Errorf("Error = %q, want bad_flag", res.Error)
	}
}

// TestRunRedact_RenderShowsRedacted: after a successful redact, the
// folded render replaces the field with "<redacted>".
func TestRunRedact_RenderShowsRedacted(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)

	if _, code := RunRedact(root, RedactOptions{ID: id, FieldPath: "description"}); code != 0 {
		t.Fatalf("redact: code = %d", code)
	}

	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code = %d, out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("show output type = %T, want ShowResult", out)
	}
	got, _ := res.Fields["description"].(string)
	if got != "<redacted>" {
		t.Errorf("description = %q, want %q", got, "<redacted>")
	}
}

// TestRunRedact_AcceptanceCriterionPath: the structured indexed form is
// accepted by the validator and writes a redact op successfully.
func TestRunRedact_AcceptanceCriterionPath(t *testing.T) {
	root := makeCreateRepo(t)
	createOut, code := RunCreate(root, CreateOptions{
		Title:  "with accept",
		Type:   "task",
		Accept: []string{"first criterion", "second criterion"},
	})
	if code != 0 {
		t.Fatalf("seed: code = %d", code)
	}
	id := createOut.(CreateResult).ID

	out, code := RunRedact(root, RedactOptions{
		ID:        id,
		FieldPath: "acceptance_criteria[0].text",
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	if _, ok := out.(RedactResult); !ok {
		t.Fatalf("output type = %T, want RedactResult", out)
	}
}

// TestRunRedact_NoCommit: --no-commit writes the op file but does not
// advance HEAD; the result envelope reports Committed=false.
func TestRunRedact_NoCommit(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)
	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunRedact(root, RedactOptions{
		ID:        id,
		FieldPath: "description",
		NoCommit:  true,
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(RedactResult)
	if !ok {
		t.Fatalf("type = %T, want RedactResult", out)
	}
	if res.Committed {
		t.Errorf("Committed = true, want false (--no-commit)")
	}
	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
	}
}

// TestRunRedact_MissingField: missing --field is exit 2 bad_flag.
func TestRunRedact_MissingField(t *testing.T) {
	root, id := makeRedactRepoWithIssue(t)

	out, code := RunRedact(root, RedactOptions{ID: id})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	res, ok := out.(RedactErrorOutput)
	if !ok {
		t.Fatalf("type = %T, want RedactErrorOutput", out)
	}
	if res.Error != "bad_flag" {
		t.Errorf("Error = %q", res.Error)
	}
}
