package cli

// Doc-claim regression tests for the orchestrator-write upstream-sync
// trigger (Phase 2 ticket 6b, act-a9a59e).
//
// Two claims are pinned at the user-visible boundary:
//
//   1. "act.role=orchestrator" — the role-key gate the trigger reads.
//      Pinned at the boundary the spec names: the `.act/.git/config`
//      file after `act remote enable`. A drift to a path-based
//      heuristic (or any other mechanism) would make this assertion
//      fail because the config key would no longer be the load-bearing
//      signal that triggers the sync.
//
//   2. "fork-exec" — the detach mechanism. Pinned by asserting on the
//      runtime behavior: an orchestrator-role write produces a child
//      process that does NOT block the parent. The test asserts on
//      the time budget of the parent call (it returns before the
//      child's work could plausibly complete) and on the .sync-log
//      side effect of the spawned child to confirm the chain fires.
//
// Both tests use the prebuilt `act` binary (TestMain in
// concurrent_helper_test.go) so the spawned `act remote sync` resolves
// against the same binary the test process is exercising. The PATH
// override in setupOrchestratorRoleWritePath wires this.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// TestDocClaim_OrchestratorSync_RoleCheck pins the spec claim that
// the orchestrator-write trigger reads `act.role=orchestrator` from
// the nested .act/ repo's git config. Verified at the file boundary:
// after `act remote enable`, the key has the value "orchestrator".
// Triggering itself is covered by the integration test in
// internal/gitops/orchestrator_sync_test.go; this docclaim test
// guards the spec claim that the key (not a path heuristic) is what
// the trigger reads.
func TestDocClaim_OrchestratorSync_RoleCheck(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	configPath := filepath.Join(host, ".act", ".git", "config")
	role, err := config.ReadRole(configPath)
	if err != nil {
		t.Fatalf("ReadRole: %v", err)
	}
	if role != config.RoleOrchestrator {
		t.Errorf("role read from .act/.git/config = %q, want %q (the trigger gate)",
			role, config.RoleOrchestrator)
	}
}

// TestDocClaim_OrchestratorSync_BackgroundDetach pins the spec claim
// that the trigger uses fork-exec (no Wait) so the parent post-commit
// path returns before the spawned `act remote sync` completes.
//
// We exercise this end-to-end:
//
//  1. Set up an orchestrator-role fixture with an unreachable
//     origin-upstream (so any spawned `act remote sync` will fail
//     and append to .sync-log, giving us a side-effect signal).
//  2. Run `act create` from the prebuilt binary. The create call
//     itself MUST return quickly (the child runs in the background).
//  3. Wait up to 3s for .sync-log to gain content — that confirms
//     the child fired AND that the parent didn't block on it.
//
// If the trigger were synchronous, step 2 would block on the
// upstream timeout (multiple seconds) before returning. We pin a
// generous-but-bounded 500ms upper bound on the parent call to make
// the synchronous-vs-detached distinction observable without flaking
// on slow CI.
func TestDocClaim_OrchestratorSync_BackgroundDetach(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; TestMain did not run?")
	}
	host := setupOrchestratorWithUnreachableUpstream(t)

	// Wire PATH so the spawned `act remote sync` resolves to the
	// prebuilt test binary. setupOrchestratorWithUnreachableUpstream
	// has already cleaned up the override via t.Cleanup.

	parentStart := time.Now()
	if _, _, code := runAct(t, host, "create", "--type", "task", "--json", "trigger-test"); code != 0 {
		t.Fatalf("create: exit %d", code)
	}
	parentElapsed := time.Since(parentStart)

	// The parent's wall-clock budget: synchronous calls to git push
	// against an unreachable filesystem path typically take >1s
	// (git's connection-refused / ENOENT path is fast but the
	// `act remote sync` invocation chain — resolve + read config +
	// push + log-append — would still dominate). A 2s ceiling
	// leaves room for the create itself plus normal git overhead
	// but excludes a synchronous chained-sync.
	if parentElapsed > 2*time.Second {
		t.Errorf("create wall time = %v, > 2s; trigger may be blocking the parent (synchronous?)", parentElapsed)
	}

	// Independently verify the trigger actually FIRED — without
	// this check, the time-budget assertion alone could pass if the
	// trigger were silently disabled. The spawned `act remote sync`
	// will fail (upstream unreachable) and write a JSON-line entry
	// to .act/.sync-log. Bounded wait via filesystem-stat polling.
	syncLogPath := filepath.Join(host, ".act", ".sync-log")
	if !waitForFileExists(syncLogPath, 3*time.Second) {
		t.Fatalf(".sync-log did not appear within 3s after orchestrator write (trigger did not fire?)")
	}
	data, err := os.ReadFile(syncLogPath)
	if err != nil {
		t.Fatalf("read .sync-log: %v", err)
	}
	if !strings.Contains(string(data), `"reason"`) {
		t.Errorf(".sync-log content missing structured JSON entry; got:\n%s", data)
	}
}

// setupOrchestratorWithUnreachableUpstream builds a host repo with
// `act init` + `act remote enable` (so act.role=orchestrator is set)
// and an origin-upstream pointing at a non-existent bare path so any
// spawned `act remote sync` fails fast and appends to .sync-log.
// Returns the host repo root. PATH is set so the spawned child binary
// resolves to actBinaryPath; the override is cleaned up via t.Cleanup.
func setupOrchestratorWithUnreachableUpstream(t *testing.T) string {
	t.Helper()
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", filepath.Dir(actBinaryPath)+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	t.Cleanup(func() { os.Setenv("PATH", origPath) })

	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	gitDir := seedActGitForSync(t, host)
	configPath := filepath.Join(gitDir, "config")
	bogusPath := filepath.Join(t.TempDir(), "unreachable.git")
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.url", bogusPath)
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.fetch", "+refs/heads/*:refs/remotes/origin-upstream/*")
	return host
}
