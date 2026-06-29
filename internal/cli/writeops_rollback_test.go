package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// withRecordingUnstageFn swaps runUnstageFn for a recorder that captures
// the (repoRoot, path) pairs the rollback path attempts to unstage. The
// original is restored on test cleanup.
func withRecordingUnstageFn(t *testing.T) *unstageRecorder {
	t.Helper()
	rec := &unstageRecorder{}
	prev := runUnstageFn
	runUnstageFn = func(repoRoot, path string) error {
		rec.record(repoRoot, path)
		return nil
	}
	t.Cleanup(func() { runUnstageFn = prev })
	return rec
}

type unstageRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *unstageRecorder) record(_, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, path)
}

func (r *unstageRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// envelopesForRollback returns N envelopes that share an issue but use
// distinct HLC stamps so each ProbeAndWrite produces its own op file.
func envelopesForRollback(t *testing.T, n int) ([]op.Envelope, [][]byte) {
	t.Helper()
	envs := make([]op.Envelope, n)
	bodies := make([][]byte, n)
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(op.ClaimPayload{Assignee: "alice"})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "claim",
			IssueID:       "act-deadbeefdeadbeef",
			Payload:       payload,
			HLC:           hlc.HLC{Wall: 1700000000000, Logical: uint32(i), NodeID: "abcdef01"},
			NodeID:        "abcdef01",
		}
		if err := env.Validate(); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
		body, err := env.Marshal()
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		envs[i] = env
		bodies[i] = body
	}
	return envs, bodies
}

// TestWriteOpsAndAutoCommit_RollbackOnCommitFailure: when every op writes
// + stages successfully but the commit fails (gpg sign with no key),
// rollback unstages exactly the staged paths and removes the op files.
// This is the primary regression test for act-c22b's structural change:
// staged[] tracking grows with each successful StageOpFile, so the
// post-failure unstage loop visits exactly the paths that were actually
// staged — not stale entries from `written` that may include paths that
// never reached `git add`.
func TestWriteOpsAndAutoCommit_RollbackOnCommitFailure(t *testing.T) {
	dir, paths := makeWriteRepo(t)
	// Force commit failure by enabling gpg signing without a key.
	mustGit(t, dir, "config", "commit.gpgsign", "true")

	rec := withRecordingUnstageFn(t)
	envs, bodies := envelopesForRollback(t, 3)
	g := gitops.NewActGitOps(dir)

	err := WriteOpsAndAutoCommit(envs, bodies, paths, g, WriteOpts{}, "act-block: test")
	if err == nil {
		t.Fatalf("expected commit failure error, got nil")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("err = %v; want commit-failure wrapping", err)
	}

	// Rollback must have unstaged exactly N paths (one per staged op).
	got := rec.snapshot()
	if len(got) != len(envs) {
		t.Errorf("unstage call count = %d; want %d (one per staged op)", len(got), len(envs))
	}
	// All the unstaged paths must be the op files we wrote.
	for _, p := range got {
		if !strings.HasPrefix(p, paths.Ops) {
			t.Errorf("unstage called with path outside paths.Ops: %q", p)
		}
		if filepath.Ext(p) != ".json" {
			t.Errorf("unstage called with non-op-file path: %q", p)
		}
	}

	// Op files must be removed from disk by the rollback.
	matches, _ := filepath.Glob(filepath.Join(paths.Ops, envs[0].IssueID, "*", "*.json"))
	if len(matches) != 0 {
		t.Errorf("op files remain after rollback: %v", matches)
	}
}

// TestWriteOpsAndAutoCommit_RollbackOnWriteFailure: when ProbeAndWrite
// fails on op 2 of 3, no op was ever staged, so rollback must NOT call
// unstage at all. Pre-act-c22b, the old rollback iterated `written` and
// would have called unstage on op 1 (the partially-written entry) — a
// spurious call to `git restore --staged` on a never-staged file. With
// the staged[]-only iteration, the recorder must see zero calls.
//
// We trigger the write failure by replacing the issue's shard directory
// with an unwritable file after op 1 succeeds, but before op 2 attempts
// to write. WriteOpsAndAutoCommit's two loops give us no natural seam
// between the writes, so we set things up before the call: the second
// envelope deliberately targets a different shard (different HLC month)
// whose parent directory we make unwritable.
func TestWriteOpsAndAutoCommit_RollbackOnWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-denied write trigger does not work as root")
	}
	dir, paths := makeWriteRepo(t)
	rec := withRecordingUnstageFn(t)
	g := gitops.NewActGitOps(dir)

	// Op 1 writes into the 2023-11 shard. Op 2 targets the 2024-12 shard,
	// whose parent we'll pre-create as a non-directory regular file so
	// ProbeAndWrite's MkdirAll fails on the second envelope only.
	envs := make([]op.Envelope, 2)
	bodies := make([][]byte, 2)
	walls := []int64{1700000000000, 1735000000000} // Nov 2023, Dec 2024
	for i := 0; i < 2; i++ {
		payload, err := json.Marshal(op.ClaimPayload{Assignee: "alice"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "claim",
			IssueID:       "act-deadbeefdeadbeef",
			Payload:       payload,
			HLC:           hlc.HLC{Wall: walls[i], Logical: 0, NodeID: "abcdef01"},
			NodeID:        "abcdef01",
		}
		if err := env.Validate(); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
		body, err := env.Marshal()
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		envs[i] = env
		bodies[i] = body
	}

	// Pre-create the 2nd op's shard path as a regular file (not directory)
	// so MkdirAll inside ProbeAndWrite fails.
	issueDir := filepath.Join(paths.Ops, envs[0].IssueID)
	if err := os.MkdirAll(issueDir, 0o755); err != nil {
		t.Fatalf("mkdir issue dir: %v", err)
	}
	collisionPath := filepath.Join(issueDir, "2024-12")
	if err := os.WriteFile(collisionPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("seed collision file: %v", err)
	}

	err := WriteOpsAndAutoCommit(envs, bodies, paths, g, WriteOpts{}, "act-block: test")
	if err == nil {
		t.Fatalf("expected write failure on op 2, got nil")
	}

	// The key assertion: NO unstage calls, because no op was staged before
	// the failure. Pre-act-c22b this would have called unstage on op 1's
	// path (which was written but not yet staged).
	if got := rec.snapshot(); len(got) != 0 {
		t.Errorf("unstage called %d time(s) on never-staged paths: %v", len(got), got)
	}

	// Op 1's file must have been removed from disk by the rollback.
	matches, _ := filepath.Glob(filepath.Join(paths.Ops, envs[0].IssueID, "2023-11", "*.json"))
	if len(matches) != 0 {
		t.Errorf("op 1 file remains on disk after rollback: %v", matches)
	}
}
