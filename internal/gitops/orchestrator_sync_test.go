package gitops

// Phase 2 ticket 6b (act-a9a59e) — orchestrator-write upstream-sync
// trigger tests.
//
// These tests assert that AutoPushAfterCommit fires the background
// `act remote sync` child process iff `act.role=orchestrator` is set
// in the nested .act/.git/config. The three acceptance criteria from
// the ticket:
//
//   1. act.role=orchestrator → trigger fires (TestOrchestratorSync_
//      OrchestratorRole_FiresBackgroundSync).
//   2. act.role=worker → trigger does NOT fire
//      (TestOrchestratorSync_WorkerRole_DoesNotFire).
//   3. act.role unset → treated as worker; trigger does NOT fire
//      (TestOrchestratorSync_UnsetRole_TreatedAsWorker).
//
// Signal: gitops.TestOrchestratorSyncFireCount is a process-global
// counter incremented only when the trigger successfully spawns the
// background child (Setsid-detached `act remote sync`). The counter
// is unchanged on the worker / unset / no-origin paths. Tests
// snapshot the value at start and compare against the post-call
// delta.
//
// The "fake act" binary: gitops tests do not have a TestMain that
// builds the real `act` binary (cli/concurrent_helper_test.go owns
// that). To make cmd.Start() succeed without pulling in a build
// dependency, each test prepends a temp directory containing a stub
// `act` shell script to PATH. The stub exits 0 immediately —
// asserting on the fire counter (which increments on Start success)
// is sufficient for the orchestrator-role gate; real `act remote
// sync` behavior is covered by the cli package's remote_sync_test.go.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aac/act/internal/testfixtures"
)

// makeOrchestratorActStateRepo builds a .act/ working tree wired to a
// bare remote (origin) so AutoPushAfterCommit reaches the trigger
// branch. Returns (actStateRoot, bareRemote). The role is NOT set;
// individual tests write it via setActRole.
func makeOrchestratorActStateRepo(t *testing.T) (string, *testfixtures.BareRemote) {
	t.Helper()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)
	// Force-push the seed so subsequent pushes are vanilla fast-forwards
	// (mirror of cli.makeRepoWithRemoteOrigin pattern).
	writeFile(t, filepath.Join(work, "seed.txt"), "seed\n")
	runGit(t, work, "add", "seed.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "seed")
	runGit(t, work, "push", "-f", "origin", "main")
	return work, remote
}

// setActRole writes act.role to <actStateRoot>/.git/config (the
// canonical key location per Phase 2 ticket 1a's config.ReadRole).
func setActRole(t *testing.T, actStateRoot, role string) {
	t.Helper()
	configPath := filepath.Join(actStateRoot, ".git", "config")
	runGit(t, actStateRoot, "config", "-f", configPath, "act.role", role)
}

// installFakeActBinary writes a stub `act` shell script to a fresh
// temp dir, prepends that dir to PATH, and registers a Cleanup that
// restores PATH. The stub exits 0 immediately — asserting on the
// orchestrator-fire counter requires only that cmd.Start() succeed,
// not that the child do meaningful work.
func installFakeActBinary(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake act binary uses a POSIX shell script; Windows path unsupported")
	}
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "act")
	body := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(stub, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake act stub: %v", err)
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", stubDir+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	t.Cleanup(func() { os.Setenv("PATH", origPath) })
}

// waitForChildren is a small reaper helper: when AutoPushAfterCommit
// returns we may still have unreaped child processes from the stub
// `act` invocations. The Setsid-detached children are reparented to
// init, so the test process is not their parent — but defensive
// best-effort reaping via os.FindProcess + Wait would race. We don't
// need to reap; the OS handles detached children. This stub is here
// so future tests that DO need reaping can plug in cleanly.
func waitForChildren() {}

// TestOrchestratorSync_OrchestratorRole_FiresBackgroundSync covers
// AC #1: act.role=orchestrator → AutoPushAfterCommit fires the
// background trigger. Asserted via TestOrchestratorSyncFireCount
// delta (increments when the child Start() succeeds).
func TestOrchestratorSync_OrchestratorRole_FiresBackgroundSync(t *testing.T) {
	installFakeActBinary(t)
	work, _ := makeOrchestratorActStateRepo(t)
	setActRole(t, work, "orchestrator")

	// Local commit so AutoPushAfterCommit has something to publish.
	writeFile(t, filepath.Join(work, "orch.txt"), "x\n")
	runGit(t, work, "add", "orch.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "orch")

	before := TestOrchestratorSyncFireCount.Load()
	g := NewGitOps(work)
	if err := g.AutoPushAfterCommit(); err != nil {
		t.Fatalf("AutoPushAfterCommit: %v", err)
	}
	waitForChildren()
	after := TestOrchestratorSyncFireCount.Load()
	if after-before != 1 {
		t.Errorf("TestOrchestratorSyncFireCount delta = %d, want 1 (orchestrator role)", after-before)
	}
}

// TestOrchestratorSync_WorkerRole_DoesNotFire covers AC #2:
// act.role=worker → trigger does NOT fire. The fixture is otherwise
// identical to the orchestrator case so the only variable is the
// role-key value.
func TestOrchestratorSync_WorkerRole_DoesNotFire(t *testing.T) {
	installFakeActBinary(t)
	work, _ := makeOrchestratorActStateRepo(t)
	setActRole(t, work, "worker")

	writeFile(t, filepath.Join(work, "worker.txt"), "x\n")
	runGit(t, work, "add", "worker.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "worker")

	before := TestOrchestratorSyncFireCount.Load()
	g := NewGitOps(work)
	if err := g.AutoPushAfterCommit(); err != nil {
		t.Fatalf("AutoPushAfterCommit: %v", err)
	}
	after := TestOrchestratorSyncFireCount.Load()
	if after-before != 0 {
		t.Errorf("TestOrchestratorSyncFireCount delta = %d, want 0 (worker role)", after-before)
	}
}

// TestOrchestratorSync_UnsetRole_TreatedAsWorker covers AC #3: a
// repo with act.role unset (legacy state) is treated as worker — no
// upstream sync fired. The role key is never written; the fixture's
// virgin .act/.git/config has no [act] section.
func TestOrchestratorSync_UnsetRole_TreatedAsWorker(t *testing.T) {
	installFakeActBinary(t)
	work, _ := makeOrchestratorActStateRepo(t)
	// Deliberately do NOT call setActRole. The key remains unset.

	writeFile(t, filepath.Join(work, "legacy.txt"), "x\n")
	runGit(t, work, "add", "legacy.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "legacy")

	before := TestOrchestratorSyncFireCount.Load()
	g := NewGitOps(work)
	if err := g.AutoPushAfterCommit(); err != nil {
		t.Fatalf("AutoPushAfterCommit: %v", err)
	}
	after := TestOrchestratorSyncFireCount.Load()
	if after-before != 0 {
		t.Errorf("TestOrchestratorSyncFireCount delta = %d, want 0 (unset role → worker default)", after-before)
	}
}
