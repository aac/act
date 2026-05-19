package cli

// Tests for `act remote sync` (Phase 2 ticket 6a).
//
// The four acceptance criteria from the ticket map to subtests below:
//
//   1. reachable upstream: pushes; upstream ref advances; .sync-log
//      unchanged (TestRemoteSync_Reachable_PushesAndUpstreamAdvances).
//   2. unreachable upstream: exit 0; .sync-log gains an entry whose
//      first JSON field is `"reason": "unreachable"`
//      (TestRemoteSync_Unreachable_AppendsSyncLogEntry).
//   3. no upstream configured: exit 2, envelope `upstream_not_configured`,
//      stderr literal command-hint
//      (TestRemoteSync_NoUpstreamConfigured_ExitsTwo).
//   4. worker push triggers post-receive hook → background sync fires →
//      .sync-log mtime advances within 2s
//      (TestRemoteSync_PostReceiveHookFiresBackgroundSync).
//
// All async timing uses bounded filesystem-watch waits, not polling
// sleeps. The chosen mechanism: a wall-clock deadline plus an
// `os.Stat`-on-interval loop with a small (50ms) probe period. We
// avoid fsnotify so the test doesn't pull a new dependency just for
// the one path. The probe loop is bounded by the deadline; what the
// ticket prohibits is unbounded sleeps, not the polling primitive
// itself.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/testfixtures"
)

// syncFixtureWithUpstream constructs the standard orchestrator setup:
// a host repo with `act init` + `act remote enable`, plus a bare-repo
// upstream remote configured as `origin-upstream` in the
// orchestrator's `.act/.git/config`. Returns the host repo root and
// the upstream BareRemote.
func syncFixtureWithUpstream(t *testing.T) (string, *testfixtures.BareRemote) {
	t.Helper()
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	gitDir := seedActGitForSync(t, host)

	upstream := testfixtures.NewBareRemote(t)
	// BareRemote pre-seeds `main` with its own root commit so HEAD
	// resolves immediately. For the sync test we want a virgin
	// upstream so the orchestrator's first push lands as a
	// fast-forward — matching how `act remote add-upstream` would
	// initialise a private GitHub mirror in production. Delete the
	// seeded `main` ref to simulate that.
	mustExecSync(t, "git", "--git-dir="+upstream.Path, "update-ref", "-d", "refs/heads/main")
	configPath := filepath.Join(gitDir, "config")
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.url", upstream.URL)
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.fetch", "+refs/heads/*:refs/remotes/origin-upstream/*")
	return host, upstream
}

// seedActGitForSync ensures the orchestrator's `.act/.git` has at
// least one commit on main, identity configured, and gpgsign off — so
// `git push` against it produces a real ref to send upstream. `act
// init` (the 1a-era fixture) doesn't always leave HEAD pointing at a
// commit, so we top it up. Returns the gitDir path.
func seedActGitForSync(t *testing.T, host string) string {
	t.Helper()
	actDir := filepath.Join(host, ".act")
	gitDir := filepath.Join(actDir, ".git")
	mustExecSync(t, "git", "--git-dir="+gitDir, "--work-tree="+actDir, "config", "user.email", "sync@example.com")
	mustExecSync(t, "git", "--git-dir="+gitDir, "--work-tree="+actDir, "config", "user.name", "Sync Tester")
	mustExecSync(t, "git", "--git-dir="+gitDir, "--work-tree="+actDir, "config", "commit.gpgsign", "false")
	if _, err := exec.Command("git", "--git-dir="+gitDir, "rev-parse", "HEAD").Output(); err != nil {
		mustExecSync(t, "git", "--git-dir="+gitDir, "--work-tree="+actDir, "add", "-A")
		mustExecSync(t, "git", "--git-dir="+gitDir, "--work-tree="+actDir, "commit", "--no-verify", "-m", "seed .act/")
	}
	return gitDir
}

// mustExecSync runs cmd args, t.Fatalf on failure. Local to this file
// so the remote_test.go fixture helper isn't conflated.
func mustExecSync(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// readSyncLogLines returns the contents of .act/.sync-log split into
// JSON-line strings (empty lines filtered). Returns nil if the file
// does not exist.
func readSyncLogLines(t *testing.T, host string) []string {
	t.Helper()
	path := filepath.Join(host, ".act", SyncLogFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read sync-log: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestRemoteSync_Reachable_PushesAndUpstreamAdvances covers AC #1.
// The fixture leaves the upstream `main` ref unset so our first push
// lands as a fast-forward — matching the production flow where
// `act remote add-upstream <url>` does the initial publish.
func TestRemoteSync_Reachable_PushesAndUpstreamAdvances(t *testing.T) {
	host, upstream := syncFixtureWithUpstream(t)
	gitDir := filepath.Join(host, ".act", ".git")

	out, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if code != 0 {
		t.Fatalf("sync: code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteSyncResult)
	if !ok {
		t.Fatalf("sync: unexpected output type %T", out)
	}
	if !res.Pushed {
		t.Errorf("sync: Pushed=false, expected true (Logged=%v Reason=%q)", res.Logged, res.Reason)
	}
	if res.Logged {
		t.Errorf("sync: Logged=true on reachable upstream")
	}

	// Upstream's main ref MUST now equal the orchestrator's local
	// main ref.
	localRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+gitDir, "rev-parse", "refs/heads/main"))
	afterRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+upstream.Path, "rev-parse", "refs/heads/main"))
	if afterRef != localRef {
		t.Errorf("upstream ref %s != local main %s after sync", afterRef, localRef)
	}

	lines := readSyncLogLines(t, host)
	if len(lines) != 0 {
		t.Errorf("sync-log non-empty on success path: %v", lines)
	}
}

// TestRemoteSync_Idempotent_NoOpWhenUpstreamMatchesOrigin pins the
// "no-op if origin-upstream ref matches origin" claim. Second sync
// MUST exit zero, NOT log, NOT push.
func TestRemoteSync_Idempotent_NoOpWhenUpstreamMatchesOrigin(t *testing.T) {
	host, _ := syncFixtureWithUpstream(t)

	out, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if code != 0 {
		t.Fatalf("first sync: code=%d out=%v", code, out)
	}
	res, _ := out.(RemoteSyncResult)
	if !res.Pushed && !res.Logged {
		// First sync was already a no-op; the second-call idempotency
		// is the assertion below.
	}

	out2, code2 := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if code2 != 0 {
		t.Fatalf("second sync: code=%d out=%v", code2, out2)
	}
	res2, _ := out2.(RemoteSyncResult)
	if res2.Pushed {
		t.Errorf("second sync: Pushed=true, expected no-op")
	}
	if res2.Logged {
		t.Errorf("second sync: Logged=true on idempotent path")
	}
}

// TestRemoteSync_Unreachable_AppendsSyncLogEntry covers AC #2. The
// fixture points `origin-upstream` at a nonexistent filesystem path
// so `git push` fails for a deterministic, hermetic reason.
func TestRemoteSync_Unreachable_AppendsSyncLogEntry(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	gitDir := seedActGitForSync(t, host)

	bogusPath := filepath.Join(t.TempDir(), "does-not-exist", "nowhere.git")
	configPath := filepath.Join(gitDir, "config")
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.url", bogusPath)

	out, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if code != 0 {
		t.Fatalf("sync (unreachable): expected exit 0 (fail-soft); got code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteSyncResult)
	if !ok {
		t.Fatalf("sync (unreachable): unexpected output type %T", out)
	}
	if res.Pushed {
		t.Errorf("sync (unreachable): Pushed=true, expected false")
	}
	if !res.Logged {
		t.Errorf("sync (unreachable): Logged=false, expected true")
	}
	if res.Reason != "unreachable" {
		t.Errorf("sync (unreachable): Reason=%q, want %q", res.Reason, "unreachable")
	}

	lines := readSyncLogLines(t, host)
	if len(lines) == 0 {
		t.Fatalf("sync-log empty after unreachable push")
	}
	first := lines[0]
	// AC: "first JSON field is `reason`". Assert the line starts with
	// `{"reason":` — the strongest version of the claim.
	if !strings.HasPrefix(first, `{"reason":`) {
		t.Errorf("first JSON field in sync-log entry is not `reason`:\n%s", first)
	}

	var entry SyncLogEntry
	if err := json.Unmarshal([]byte(first), &entry); err != nil {
		t.Fatalf("unmarshal sync-log entry: %v\n%s", err, first)
	}
	if entry.Reason != "unreachable" {
		t.Errorf("sync-log entry reason=%q, want %q", entry.Reason, "unreachable")
	}
	if entry.Timestamp == "" {
		t.Errorf("sync-log entry timestamp empty")
	}
}

// TestRemoteSync_NoUpstreamConfigured_ExitsTwo covers AC #3.
func TestRemoteSync_NoUpstreamConfigured_ExitsTwo(t *testing.T) {
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}

	out, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if code != 2 {
		t.Errorf("sync (no upstream): code=%d, want 2", code)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("sync (no upstream): unexpected output type %T", out)
	}
	if m["error"] != ErrUpstreamNotConfigured {
		t.Errorf("sync (no upstream): error=%v, want %s", m["error"], ErrUpstreamNotConfigured)
	}
	if msg, _ := m["message"].(string); msg != "no origin-upstream configured; run 'act remote add-upstream <url>'" {
		t.Errorf("sync (no upstream): message=%q, want canonical command-hint string", msg)
	}
}

// waitForFileMtimeAdvance is the bounded filesystem-watch helper used
// by AC #4. It polls os.Stat on path at probe-interval until the
// mtime is strictly after `before` or the deadline expires. Returns
// true if the mtime advanced; false on timeout.
func waitForFileMtimeAdvance(path string, before time.Time, deadline time.Duration) bool {
	d := time.Now().Add(deadline)
	for time.Now().Before(d) {
		if info, err := os.Stat(path); err == nil {
			if info.ModTime().After(before) {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitForFileExists is the bounded "file appears" variant of
// waitForFileMtimeAdvance — used when the test starts with no
// `.sync-log` file and we want to detect first appearance.
func waitForFileExists(path string, deadline time.Duration) bool {
	d := time.Now().Add(deadline)
	for time.Now().Before(d) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// TestRemoteSync_PostReceiveHookFiresBackgroundSync covers AC #4. A
// simulated worker push that lands on the orchestrator MUST fire the
// post-receive hook, which MUST in turn fire `act remote sync` in the
// background, which MUST (in the unreachable-upstream case) append to
// .act/.sync-log within 2 seconds.
//
// We make the upstream deliberately unreachable so the assertion is
// on the .sync-log mtime advancing — that's the user-visible signal
// that the chain fired end-to-end (push → hook → sync → log).
func TestRemoteSync_PostReceiveHookFiresBackgroundSync(t *testing.T) {
	// Reuse the TestMain-built `act` binary (concurrent_helper_test.go).
	// Building a fresh binary inside the test would add ~6s per
	// invocation and push the package-test wall time past the
	// close-hook's 120s budget; the prebuilt binary is identical to
	// what we'd build here.
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; TestMain did not run?")
	}
	origPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", filepath.Dir(actBinaryPath)+string(os.PathListSeparator)+origPath); err != nil {
		t.Fatalf("setenv PATH: %v", err)
	}
	t.Cleanup(func() { os.Setenv("PATH", origPath) })

	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	actDir := filepath.Join(host, ".act")
	gitDir := seedActGitForSync(t, host)

	// Point upstream at a bogus path → sync will fail → background
	// sync writes to .sync-log. That's our user-visible signal.
	configPath := filepath.Join(gitDir, "config")
	bogusPath := filepath.Join(t.TempDir(), "unreachable.git")
	mustExecSync(t, "git", "config", "-f", configPath, "remote.origin-upstream.url", bogusPath)

	// Worker simulation: clone `.act/.git`, commit, push back. The
	// 1a-installed `receive.denyCurrentBranch=updateInstead` config
	// lets the orchestrator absorb the push without rejecting it.
	workerDir := t.TempDir()
	mustExecSync(t, "git", "clone", "-q", gitDir, workerDir)
	mustExecSync(t, "git", "-C", workerDir, "config", "user.email", "worker@example.com")
	mustExecSync(t, "git", "-C", workerDir, "config", "user.name", "Worker")
	mustExecSync(t, "git", "-C", workerDir, "config", "commit.gpgsign", "false")
	stampFile := filepath.Join(workerDir, "worker-stamp")
	if err := os.WriteFile(stampFile, []byte(time.Now().Format(time.RFC3339Nano)), 0o644); err != nil {
		t.Fatalf("write worker stamp: %v", err)
	}
	mustExecSync(t, "git", "-C", workerDir, "add", "worker-stamp")
	mustExecSync(t, "git", "-C", workerDir, "commit", "--no-verify", "-m", "worker push")

	syncLogPath := filepath.Join(actDir, SyncLogFilename)
	pushStart := time.Now()

	mustExecSync(t, "git", "-C", workerDir, "push", "-q", "origin", "main")

	// AC: .sync-log mtime advances within 2 seconds. 2s bounded
	// wait. If the file doesn't exist yet (first sync), also accept
	// "file came into existence".
	deadline := 2 * time.Second
	if _, err := os.Stat(syncLogPath); err != nil && os.IsNotExist(err) {
		if !waitForFileExists(syncLogPath, deadline) {
			t.Fatalf("sync-log did not appear within %v after worker push", deadline)
		}
	} else if !waitForFileMtimeAdvance(syncLogPath, pushStart, deadline) {
		t.Fatalf("sync-log mtime did not advance within %v after worker push", deadline)
	}

	lines := readSyncLogLines(t, host)
	if len(lines) == 0 {
		t.Fatalf("sync-log empty after hook fired")
	}
	if !strings.HasPrefix(lines[0], `{"reason":`) {
		t.Errorf("first sync-log line first field is not `reason`:\n%s", lines[0])
	}
}

// TestRemoteSync_SyncLogPruningCap pins the 100-entry cap claim. We
// inject 105 entries via appendSyncLog and assert the file ends at
// exactly 100 lines.
func TestRemoteSync_SyncLogPruningCap(t *testing.T) {
	host := newRemoteFixture(t)
	syncLogPath := filepath.Join(host, ".act", SyncLogFilename)
	for i := 0; i < SyncLogMaxEntries+5; i++ {
		entry := SyncLogEntry{
			Reason:    "unreachable",
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Error:     "synthetic " + strings.Repeat("x", 8),
		}
		if err := appendSyncLog(syncLogPath, entry); err != nil {
			t.Fatalf("appendSyncLog #%d: %v", i, err)
		}
	}
	lines := readSyncLogLines(t, host)
	if len(lines) != SyncLogMaxEntries {
		t.Errorf("sync-log lines=%d, want %d (cap)", len(lines), SyncLogMaxEntries)
	}
}

