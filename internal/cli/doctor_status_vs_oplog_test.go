package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
)

// findingsFor returns the status-vs-oplog findings from a doctor run,
// failing the test if the run did not produce a DoctorResult.
func statusVsOplogFindings(t *testing.T, root string) []Finding {
	t.Helper()
	out, _ := RunDoctor(root, DoctorOptions{Check: CheckStatusVsOplog})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("doctor output type = %T, want DoctorResult (%+v)", out, out)
	}
	var got []Finding
	for _, f := range res.Findings {
		if f.Check == CheckStatusVsOplog {
			got = append(got, f)
		}
	}
	return got
}

// soleOpFileFor returns the single op file under ops/<id>/<shard>/ whose
// name contains want, as an absolute path.
func soleOpFileFor(t *testing.T, root, id, want string) string {
	t.Helper()
	paths := config.Layout(root)
	var found []string
	issueDir := filepath.Join(paths.Ops, id)
	shards, err := os.ReadDir(issueDir)
	if err != nil {
		t.Fatalf("read %s: %v", issueDir, err)
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(issueDir, shard.Name()))
		if err != nil {
			t.Fatalf("read shard: %v", err)
		}
		for _, f := range files {
			if strings.Contains(f.Name(), want) {
				found = append(found, filepath.Join(issueDir, shard.Name(), f.Name()))
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("looking for one %q op file under %s, found %d: %v", want, issueDir, len(found), found)
	}
	return found[0]
}

// TestRunDoctor_StatusVsOplogCleanStore: a store whose index, op files and
// committed history all agree produces no status-vs-oplog findings. This
// is the guard against the check crying wolf on every healthy read — the
// failure mode that would get it ignored.
func TestRunDoctor_StatusVsOplogCleanStore(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("close: code = %d", code)
	}
	if got := statusVsOplogFindings(t, root); len(got) != 0 {
		t.Errorf("clean store produced findings: %+v", got)
	}
}

// TestDocClaim_StatusVsOplogReportsUncommittedOp asserts the `act help`
// claim "this status is not durable yet": when a close op is on disk but
// not in the nested repo's HEAD — the state act-3b6e58 was in for the
// three minutes three sessions read it as closed — doctor says so, at
// warn severity, naming the file.
//
// The fixture reproduces the shape rather than the incident's timing:
// close normally (op written, committed, index upserted to closed), then
// move HEAD back one commit with a soft reset. That leaves exactly the
// incident's state — index says closed, ops/ says closed, HEAD says open
// — without needing a multi-minute gate to hold the window open.
func TestDocClaim_StatusVsOplogReportsUncommittedOp(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("close: code = %d", code)
	}
	closeOp := soleOpFileFor(t, root, id, "-close.json")

	// Un-commit the close op, leaving it on disk. HEAD now predates it.
	paths := config.Layout(root)
	mustGit(t, paths.Root, "reset", "--soft", "HEAD~1")

	got := statusVsOplogFindings(t, root)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Severity != "warn" {
		t.Errorf("severity = %q, want %q (an in-flight write is not loss)", f.Severity, "warn")
	}
	if f.IssueID != id {
		t.Errorf("issue_id = %q, want %q", f.IssueID, id)
	}
	for _, want := range []string{
		`act reports "closed"`,
		`the committed op log says "open"`,
		"not durable yet",
		filepath.Base(closeOp),
	} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message missing %q:\n%s", want, f.Message)
		}
	}
}

// TestDocClaim_StatusVsOplogReportsRetractedOp asserts the `act help`
// claim under "A STATUS THE OP LOG DOES NOT SUPPORT": when HEAD carries
// an op file the working tree no longer has, doctor reports it as an
// error and names the file, because that is data loss rather than a
// write in flight.
//
// This is the second half of the act-fec192 incident: the close op was
// committed by an act-sync sweep, then removed from ops/ by close.go's
// rollback, and the next sweep committed the deletion. Reproduced here by
// deleting the committed op file and rebuilding the index from what is
// left on disk — the same end state, reached without a sweep.
func TestDocClaim_StatusVsOplogReportsRetractedOp(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("close: code = %d", code)
	}
	closeOp := soleOpFileFor(t, root, id, "-close.json")

	// Retract the op from the working tree only; HEAD keeps it.
	if err := os.Remove(closeOp); err != nil {
		t.Fatalf("remove close op: %v", err)
	}
	// Rebuild the index from what survives on disk, so the index and the
	// disk agree (status open) and only the committed history dissents.
	if _, code := RunDoctor(root, DoctorOptions{Check: "index-divergence", Fix: true}); code != 0 {
		t.Fatalf("index rebuild: code = %d", code)
	}

	got := statusVsOplogFindings(t, root)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 finding, got %d: %+v", len(got), got)
	}
	f := got[0]
	if f.Severity != "error" {
		t.Errorf("severity = %q, want %q (a retracted op is loss)", f.Severity, "error")
	}
	if f.IssueID != id {
		t.Errorf("issue_id = %q, want %q", f.IssueID, id)
	}
	for _, want := range []string{
		`act reports "open"`,
		`the committed op log says "closed"`,
		"retracted with no record of the retraction",
		filepath.Base(closeOp),
		"git -C .act show HEAD:ops/" + id + "/",
	} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message missing %q:\n%s", want, f.Message)
		}
	}
}

// TestRunDoctor_StatusVsOplogStaleIndex: the check also catches the plain
// case the ticket's reporter assumed had happened — an index row that no
// fold of the op files supports. index-divergence reports that too, but
// only as one opaque diff string; this check names the issue and both
// statuses, which is what a reader chasing a specific ticket needs.
func TestRunDoctor_StatusVsOplogStaleIndex(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("close: code = %d", code)
	}
	closeOp := soleOpFileFor(t, root, id, "-close.json")

	// Remove the close op from BOTH the working tree and HEAD, leaving
	// index.db as the only thing that still believes in the close.
	if err := os.Remove(closeOp); err != nil {
		t.Fatalf("remove close op: %v", err)
	}
	paths := config.Layout(root)
	mustGit(t, paths.Root, "commit", "-q", "--no-verify", "-a", "-m", "retract")

	got := statusVsOplogFindings(t, root)
	if len(got) == 0 {
		t.Fatalf("stale index produced no findings")
	}
	f := got[0]
	if f.Severity != "error" {
		t.Errorf("severity = %q, want %q", f.Severity, "error")
	}
	for _, want := range []string{
		`index says "closed"`,
		`the op log on disk says "open"`,
		"act doctor --fix",
	} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message missing %q:\n%s", want, f.Message)
		}
	}
}
