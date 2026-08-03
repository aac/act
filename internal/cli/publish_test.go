package cli

// act-89a595 boundary tests.
//
// The change makes a push failure on an ALREADY-COMMITTED op exit 0.
// That is only safe because the other side of the boundary is intact:
// a write whose op never committed must still fail loudly and non-zero.
// These tests pin both sides. If you are here because you are widening
// the fail-soft treatment, TestPublish_CommitFailureStillExitsNonZero is
// the test that says you have gone too far.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// breakCommits makes every subsequent `git commit` in the nested .act/
// repo fail, by pointing commit signing at a program that does not
// exist. Chosen over a stale index.lock (which fails at `git add`, one
// step too early) because it fails the COMMIT specifically — the exact
// step whose success flips the exit-code contract.
func breakCommits(t *testing.T, hostRoot string) {
	t.Helper()
	paths := config.Layout(hostRoot)
	for _, kv := range [][2]string{
		{"commit.gpgsign", "true"},
		{"gpg.program", filepath.Join(t.TempDir(), "definitely-not-a-real-gpg")},
	} {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = paths.Root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v\n%s", kv[0], err, out)
		}
	}
}

// TestPublish_CommitFailureStillExitsNonZero is the fence on act-89a595:
// exit 0 is reserved for ops that actually committed. When the commit
// itself fails, `act close` must still fail non-zero, must not report
// the issue closed, and must not queue anything to .pending-pushes —
// there is no commit to publish later.
func TestPublish_CommitFailureStillExitsNonZero(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, _ := makeRepoWithRemoteOrigin(t)

	createOut, code := RunCreate(root, CreateOptions{Title: "commit-fails", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	breakCommits(t, root)

	var stderr bytes.Buffer
	out, code := RunClose(root, CloseOptions{ID: id, Stderr: &stderr})
	if code == 0 {
		t.Fatalf("exit code = 0 on a close whose commit failed; want non-zero. out=%+v", out)
	}
	errOut, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CloseErrorOutput", out)
	}
	if errOut.Error != "commit_failed" {
		t.Errorf("envelope Error = %q, want %q", errOut.Error, "commit_failed")
	}

	// Nothing may be queued: there is no local commit to publish.
	pending, err := ReadPendingPushes(filepath.Join(root, ".act"))
	if err != nil {
		t.Fatalf("ReadPendingPushes: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("queued %d pending-push entries for an op that never committed: %+v", len(pending), pending)
	}

	// And the deferral warning must NOT have fired — it claims the write
	// landed, which would be a lie here.
	if strings.Contains(stderr.String(), "COMMITTED LOCALLY") {
		t.Errorf("emitted the committed-but-not-pushed warning on a failed commit:\n%s", stderr.String())
	}
}

// TestPublish_NoOriginDoesNotWarn guards against the deferral warning
// becoming noise. A repo with no `origin` is local-only by design (spec
// §"Auto-publish on write"); that is not a deferred push and must stay
// silent.
func TestPublish_NoOriginDoesNotWarn(t *testing.T) {
	root := makeCreateRepo(t)

	createOut, code := RunCreate(root, CreateOptions{Title: "local-only", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	var stderr bytes.Buffer
	out, code := RunClose(root, CloseOptions{ID: id, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("RunClose on a no-origin repo: code=%d out=%+v", code, out)
	}
	res, ok := out.(CloseResult)
	if !ok {
		t.Fatalf("output type = %T, want CloseResult", out)
	}
	if res.PushDeferred {
		t.Errorf("PushDeferred = true on a no-origin repo; local-only is not a deferral")
	}
	if strings.Contains(stderr.String(), "WARNING") {
		t.Errorf("no-origin close emitted a publish warning:\n%s", stderr.String())
	}
}

// TestPublish_DeferredThenFlushedOnNextWrite closes the loop the warning
// promises: a deferred push is published by the NEXT write once origin
// is reachable again. Without this, "queued in .act/.pending-pushes"
// would be an unverified claim.
func TestPublish_DeferredThenFlushedOnNextWrite(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)

	createOut, code := RunCreate(root, CreateOptions{Title: "defer-then-flush", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	// Close with every push attempt failing → deferred.
	gitops.ResetPushAttemptCounter()
	os.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")
	var stderr bytes.Buffer
	out, code := RunClose(root, CloseOptions{ID: id, Stderr: &stderr})
	os.Unsetenv("ACT_TEST_FAIL_PUSH_AFTER")
	if code != 0 {
		t.Fatalf("deferred close: code=%d out=%+v", code, out)
	}
	if !out.(CloseResult).PushDeferred {
		t.Fatalf("close was not marked PushDeferred")
	}

	tree := runOut(t, remote.Path, "git", "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(tree, "-close.json") {
		t.Fatalf("close op reached the remote despite the injected push failures:\n%s", tree)
	}

	// Next write, origin healthy again: the backlog flushes first.
	gitops.ResetPushAttemptCounter()
	if _, code := RunCreate(root, CreateOptions{Title: "the-next-write", Type: "task"}); code != 0 {
		t.Fatalf("follow-up create: code=%d", code)
	}

	tree = runOut(t, remote.Path, "git", "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "-close.json") {
		t.Errorf("deferred close op never reached the remote after a healthy write:\n%s", tree)
	}
	pending, err := ReadPendingPushes(filepath.Join(root, ".act"))
	if err != nil {
		t.Fatalf("ReadPendingPushes: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending-pushes not cleared after a successful flush: %+v", pending)
	}
}
