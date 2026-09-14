package integration

// act-38330b: stress evidence for the nested-.act write pipeline under
// concurrent `act` processes sharing ONE checkout (the fleet shape: several
// sessions in the same repo, each running `act create` / `act update`
// against the same .act/.git), while a second clone pushes to the same bare
// remote so the shared checkout's pushes are rejected and it must
// fetch+rebase.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/flock"
	"github.com/aac/act/internal/gitops"
)

// wlockStoreFixture is a host repo whose nested .act/.git pushes to a local
// bare remote, plus a second host clone of that remote (the "peer") whose
// writes advance origin behind the shared checkout's back.
type wlockStoreFixture struct {
	shared string // host repo root shared by the concurrent writers
	peer   string // second host repo pushing to the same bare
	bare   string
}

func wlockNewStore(t *testing.T) wlockStoreFixture {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	mustGitIn(t, root, "init", "--bare", "-q", "-b", "main", bare)

	now := func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	shared := newHostRepo(t)
	if _, code := cli.RunInit(shared, cli.InitOptions{MachineID: "machine-shared", GitEmail: "shared@example.com", Now: now}); code != 0 {
		t.Fatalf("RunInit shared: code=%d", code)
	}
	wlockConfigureActRepo(t, shared, "shared@example.com")
	gd := filepath.Join(shared, ".act", ".git")
	mustGitIn(t, "", "--git-dir="+gd, "remote", "add", "origin", bare)
	mustGitIn(t, "", "--git-dir="+gd, "--work-tree="+filepath.Join(shared, ".act"), "push", "-q", "-u", "origin", "main")

	peer := newHostRepo(t)
	if out, code := cli.RunBootstrapWorker(cli.BootstrapWorkerOptions{FromRemoteURL: bare, Target: peer}); code != 0 {
		t.Fatalf("bootstrap peer: code=%d out=%+v", code, out)
	}
	wlockConfigureActRepo(t, peer, "peer@example.com")
	// bootstrap-worker strips hooks/ from the worker tree but leaves them
	// tracked, so the peer's tree carries a tracked deletion that makes
	// every rebase refuse ("You have unstaged changes"). Restore them so
	// the peer exercises real rebases instead of that unrelated wedge.
	mustGitIn(t, "", "--git-dir="+filepath.Join(peer, ".act", ".git"), "--work-tree="+filepath.Join(peer, ".act"), "checkout", "--", ".")
	return wlockStoreFixture{shared: shared, peer: peer, bare: bare}
}

func wlockConfigureActRepo(t *testing.T, host, email string) {
	t.Helper()
	gd := filepath.Join(host, ".act", ".git")
	wt := filepath.Join(host, ".act")
	for _, kv := range [][2]string{{"user.email", email}, {"user.name", "W"}, {"commit.gpgsign", "false"}} {
		mustGitIn(t, "", "--git-dir="+gd, "--work-tree="+wt, "config", kv[0], kv[1])
	}
}

// wlockResult is one subprocess outcome.
type wlockResult struct {
	args   []string
	code   int
	stdout string
	stderr string
	id     string
}

// wlockRound runs one round: `writers` concurrent `act create` processes in
// the shared checkout (each followed by an `act update` on its own ticket)
// plus `peers` concurrent creates in the peer clone. It returns every
// result and the set of ticket titles expected on the remote.
func wlockRound(t *testing.T, fx wlockStoreFixture, round, writers, peers int) (results []wlockResult, titles []string) {
	t.Helper()
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(dir, title string, update bool) {
		defer wg.Done()
		so, se, code := runActSubprocess(t, dir, "create", "--json", title)
		r := wlockResult{args: []string{"create", title}, code: code, stdout: so, stderr: se}
		var cr struct {
			ID string `json:"id"`
		}
		if code == 0 {
			_ = json.Unmarshal([]byte(so), &cr)
			r.id = cr.ID
		}
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
		if update && r.id != "" {
			so, se, code := runActSubprocess(t, dir, "update", r.id, "--priority", "1", "--json")
			mu.Lock()
			results = append(results, wlockResult{args: []string{"update", r.id}, code: code, stdout: so, stderr: se, id: r.id})
			mu.Unlock()
		}
	}
	for i := 0; i < writers; i++ {
		title := fmt.Sprintf("wlock-r%d-shared-%d", round, i)
		titles = append(titles, title)
		wg.Add(1)
		go run(fx.shared, title, true)
	}
	for i := 0; i < peers; i++ {
		title := fmt.Sprintf("wlock-r%d-peer-%d", round, i)
		titles = append(titles, title)
		wg.Add(1)
		go run(fx.peer, title, false)
	}
	wg.Wait()
	return results, titles
}

// wlockRemoteTitles returns the set of ticket titles visible in create ops
// on the bare remote's main.
func wlockRemoteTitles(t *testing.T, bare string) map[string]bool {
	t.Helper()
	out := mustGitIn(t, "", "--git-dir="+bare, "grep", "-h", "-o", `"title": *"wlock-[^"]*"`, "main", "--", "ops")
	got := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, `"wlock-`); i >= 0 {
			got[strings.Trim(line[i:], `"`)] = true
		}
	}
	return got
}

// TestWriteLockStress drives concurrent `act` processes through the nested
// .act write pipeline and asserts that no write fails, no acked create is
// missing from the remote, and the shared checkout is left untorn.
//
// The default suite runs a small instance (2 iterations x 6 writers, a few
// seconds; skipped under -short). The act-38330b evidence run is:
//
//	ACT_WLOCK_STRESS_ITERS=20 ACT_WLOCK_STRESS_WRITERS=8 \
//	  go test ./internal/integration -run TestWriteLockStress -v -timeout 30m
//
// Recorded 2026-09-14 (darwin/arm64), 20 iterations x (8 shared-checkout
// writers doing create+update, 2 peer-clone creates):
//
//   - ACT_TEST_DISABLE_WRITE_LOCK=1 (no lock): 221 runs, 159 non-zero exits
//     (147 stale_git_lock naming a live sibling's index.lock, 12
//     write_failed at commit), 15 creates acked with exit 0 that never
//     reached the remote, 26 of 200 titles on the remote.
//   - with the lock: 360 runs, 0 non-zero exits, 0 lost, 200 of 200 titles
//     on the remote, shared tree clean, fsck clean (1 peer push deferred to
//     .pending-pushes after 5 contended retries and flushed by the next
//     write, the documented degraded path).
func TestWriteLockStress(t *testing.T) {
	iters, writers := 2, 6
	if v, err := strconv.Atoi(os.Getenv("ACT_WLOCK_STRESS_ITERS")); err == nil && v > 0 {
		iters = v
	} else if testing.Short() {
		t.Skip("skipping write-lock stress under -short")
	}
	if v, err := strconv.Atoi(os.Getenv("ACT_WLOCK_STRESS_WRITERS")); err == nil && v > 0 {
		writers = v
	}
	fx := wlockNewStore(t)

	failures := map[string]int{}
	totalRuns, totalFail, lost, deferred := 0, 0, 0, 0
	var allTitles []string
	var okTitles []string
	for it := 0; it < iters; it++ {
		results, titles := wlockRound(t, fx, it, writers, 2)
		allTitles = append(allTitles, titles...)
		for _, r := range results {
			totalRuns++
			if r.code != 0 {
				totalFail++
				key := fmt.Sprintf("%s exit=%d %s", r.args[0], r.code, wlockErrCode(r.stdout))
				failures[key]++
				if failures[key] <= 2 {
					t.Errorf("act %v exit=%d\nstdout: %s\nstderr: %s", r.args, r.code, strings.TrimSpace(r.stdout), strings.TrimSpace(r.stderr))
				}
				continue
			}
			if strings.Contains(r.stderr, "NOT PUSHED") {
				deferred++
			}
			if r.args[0] == "create" {
				okTitles = append(okTitles, r.args[1])
			}
		}
		// Settle: a final quiet write in each clone publishes any pending
		// pushes so "on remote" reflects everything durable locally.
		runActSubprocess(t, fx.shared, "create", "--json", fmt.Sprintf("settle-shared-%d", it))
		runActSubprocess(t, fx.peer, "create", "--json", fmt.Sprintf("settle-peer-%d", it))
		// Rebase state left behind in the shared checkout?
		if _, err := os.Stat(filepath.Join(fx.shared, ".act", ".git", "rebase-merge")); err == nil {
			t.Errorf("iter %d: rebase-merge dir left in shared checkout", it)
		}
		if _, err := os.Stat(filepath.Join(fx.shared, ".act", ".git", "rebase-apply")); err == nil {
			t.Errorf("iter %d: rebase-apply dir left in shared checkout", it)
		}
	}
	remote := wlockRemoteTitles(t, fx.bare)
	for _, title := range okTitles {
		if !remote[title] {
			lost++
			t.Errorf("create %q exited 0 but its op is not on the remote", title)
		}
	}
	gd := filepath.Join(fx.shared, ".act", ".git")
	status, _ := runGitIn("", "--git-dir="+gd, "--work-tree="+filepath.Join(fx.shared, ".act"), "status", "--porcelain")
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "??") {
			t.Errorf("shared checkout torn (tracked change left behind): %q", line)
		}
	}
	if fsck, err := runGitIn("", "--git-dir="+gd, "fsck", "--no-dangling"); err != nil {
		t.Errorf("fsck on shared .act/.git failed: %v\n%s", err, fsck)
	}
	t.Logf("SUMMARY iters=%d writers=%d peers=2 runs=%d nonzero=%d push_deferred=%d lost_acked_creates=%d titles=%d on_remote=%d",
		iters, writers, totalRuns, totalFail, deferred, lost, len(allTitles), len(remote))
	for k, v := range failures {
		t.Logf("  failure class %q x%d", k, v)
	}
}

// wlockErrCode extracts the envelope `error` code from act's --json output.
func wlockErrCode(stdout string) string {
	var env struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(stdout), &env) == nil && env.Error != "" {
		return env.Error
	}
	return "(no envelope)"
}

// TestWriteLock_TimeoutEnvelope: a write that cannot get the write lock
// within the bounded wait exits 1 with a write_lock_timeout envelope naming
// the lock file, and leaves no op behind.
func TestWriteLock_TimeoutEnvelope(t *testing.T) {
	host := newHostRepo(t)
	if _, code := cli.RunInit(host, cli.InitOptions{MachineID: "machine-wlock", GitEmail: "w@example.com"}); code != 0 {
		t.Fatalf("RunInit: code=%d", code)
	}
	wlockConfigureActRepo(t, host, "w@example.com")
	release, locked, err := flock.TryLock(filepath.Join(host, ".act", gitops.WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}
	defer release()

	cmd := exec.Command(actBinaryPath, "create", "--json", "wlock-timeout")
	cmd.Dir = host
	cmd.Env = append(os.Environ(), "ACT_WRITE_LOCK_TIMEOUT_MS=200")
	out, _ := cmd.Output()
	if cmd.ProcessState.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%s", cmd.ProcessState.ExitCode(), out)
	}
	var env struct {
		Error   string         `json:"error"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, out)
	}
	if env.Error != "write_lock_timeout" || env.Details["lock_file"] != ".act/.write.lock" {
		t.Fatalf("envelope = %+v, want write_lock_timeout with lock_file .act/.write.lock", env)
	}
	if n := countJSONFilesUnder(t, filepath.Join(host, ".act", "ops")); n != 0 {
		t.Fatalf("%d op files left behind after a lock timeout, want 0", n)
	}
}
