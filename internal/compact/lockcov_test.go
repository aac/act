package compact

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/flock"
	"github.com/aac/act/internal/gitops"
)

// TestLockCoverage_Compact (act-94bbea): compaction mutates and commits into
// the same nested repo as every writer, so after taking .compact.lock it
// also takes .act/.write.lock. While a sibling holds the write lock it times
// out with no snapshot written and nothing committed — and it releases
// .compact.lock on the way out, so the next compactor is not locked out.
func TestLockCoverage_Compact(t *testing.T) {
	tmp := t.TempDir()
	seedIssue(t, filepath.Join(tmp, ".act", "ops"), "act-10c4", 60, 1700000000000, "12121212")
	t.Setenv("ACT_WRITE_LOCK_TIMEOUT_MS", "150")
	release, locked, err := flock.TryLock(filepath.Join(tmp, ".act", gitops.WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}

	fc := &fakeCommitter{}
	_, err = Run(tmp, Options{}, fc)
	if !errors.Is(err, gitops.ErrWriteLockTimeout) {
		t.Fatalf("Run under a held write lock: err=%v; want ErrWriteLockTimeout", err)
	}
	if len(fc.msgs) != 0 {
		t.Fatalf("commit fired under a held write lock: %v", fc.msgs)
	}
	if _, serr := os.Stat(filepath.Join(tmp, ".act", "snapshots", "act-10c4.json")); !os.IsNotExist(serr) {
		t.Fatalf("snapshot written under a held write lock (stat err=%v)", serr)
	}

	release()
	res, err := Run(tmp, Options{}, fc)
	if err != nil || res.CompactedIssues != 1 {
		t.Fatalf("Run after release: res=%+v err=%v; want 1 compacted issue", res, err)
	}
}
