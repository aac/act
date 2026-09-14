package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/flock"
	"github.com/aac/act/internal/gitops"
)

// lockcovRecordingGops records every git call writeBlockOpsViaInterface makes.
type lockcovRecordingGops struct {
	repoRoot string
	calls    *[]string
}

func (g lockcovRecordingGops) StageOpFile(p string) error {
	*g.calls = append(*g.calls, "stage")
	return nil
}
func (g lockcovRecordingGops) Commit(string) error { *g.calls = append(*g.calls, "commit"); return nil }
func (g lockcovRecordingGops) Push() error         { *g.calls = append(*g.calls, "push"); return nil }
func (g lockcovRecordingGops) Root() string        { return g.repoRoot }

// TestLockCoverage_MCPBlockInterfacePath (act-94bbea): the MCP act_block
// interface write path acquires the cross-process write lock before writing
// its op, so while a sibling holds .act/.write.lock it times out with no op
// written and no git call made.
func TestLockCoverage_MCPBlockInterfacePath(t *testing.T) {
	root := makeRealRepo(t)
	victim := seedIssue(t, root, "v")
	blocker := seedIssue(t, root, "b")
	srv := NewServer(root, false, nil, nil)
	pre := countOpFiles(t, root)

	t.Setenv("ACT_WRITE_LOCK_TIMEOUT_MS", "150")
	release, locked, err := flock.TryLock(filepath.Join(root, ".act", gitops.WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}
	defer release()

	var calls []string
	factory := func(_ string) blockGitOps { return lockcovRecordingGops{repoRoot: root, calls: &calls} }
	body := fmt.Sprintf(`{"id":%q,"blocked_by":%q,"push":true}`, victim, blocker)
	out, isErr := srv.callBlockWithGops(json.RawMessage(body), factory)
	if !isErr {
		t.Fatalf("act_block under a held write lock succeeded; out=%+v", out)
	}
	if b, _ := json.Marshal(out); !strings.Contains(string(b), "timed out waiting for the act write lock") &&
		!strings.Contains(string(b), gitops.WriteLockFile) {
		t.Fatalf("act_block error does not name the write lock: %s", b)
	}
	if len(calls) != 0 {
		t.Fatalf("git calls under a held write lock: %v", calls)
	}
	if post := countOpFiles(t, root); post != pre {
		t.Fatalf("op files: pre=%d post=%d under a held write lock; want equal", pre, post)
	}
}
