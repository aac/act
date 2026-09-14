package importer

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/flock"
	"github.com/aac/act/internal/gitops"
)

// TestLockCoverage_Import (act-94bbea): a committing import acquires the
// cross-process write lock before writing any op, so while a sibling holds
// .act/.write.lock it times out with no op written and nothing committed.
func TestLockCoverage_Import(t *testing.T) {
	root := setup(t)
	jsonl := authorJSONL(t, root)
	t.Setenv("ACT_WRITE_LOCK_TIMEOUT_MS", "150")
	release, locked, err := flock.TryLock(filepath.Join(root, ".act", gitops.WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}
	defer release()

	g := &fakeGitOps{}
	_, err = Run(root, Options{JSONLPath: jsonl, Push: true}, g)
	if !errors.Is(err, gitops.ErrWriteLockTimeout) {
		t.Fatalf("Run under a held write lock: err=%v; want ErrWriteLockTimeout", err)
	}
	if len(g.staged) != 0 || len(g.commits) != 0 || g.pushed != 0 {
		t.Fatalf("git touched under a held lock: staged=%v commits=%v pushed=%d", g.staged, g.commits, g.pushed)
	}
	if n := countOpFiles(t, config.Layout(root).Ops); n != 0 {
		t.Fatalf("%d op files written under a held lock, want 0", n)
	}
}
