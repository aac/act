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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aac/act/internal/cli"
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
	peerAct := filepath.Join(peer, ".act")
	mustGitIn(t, "", "clone", "-q", bare, peerAct)
	if _, code := cli.RunInit(peer, cli.InitOptions{Force: true, MachineID: "machine-peer", GitEmail: "peer@example.com", Now: now}); code != 0 {
		t.Fatalf("RunInit peer: code=%d", code)
	}
	wlockConfigureActRepo(t, peer, "peer@example.com")
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

// TestWriteLockStress is the act-38330b evidence run. It is expensive, so it
// only runs when ACT_WLOCK_STRESS_ITERS is set:
//
//	ACT_WLOCK_STRESS_ITERS=20 ACT_WLOCK_STRESS_WRITERS=8 \
//	  go test ./internal/integration -run TestWriteLockStress -v -timeout 30m
func TestWriteLockStress(t *testing.T) {
	iters, _ := strconv.Atoi(os.Getenv("ACT_WLOCK_STRESS_ITERS"))
	if iters <= 0 {
		t.Skip("set ACT_WLOCK_STRESS_ITERS to run the write-lock stress test")
	}
	writers := 8
	if v, err := strconv.Atoi(os.Getenv("ACT_WLOCK_STRESS_WRITERS")); err == nil && v > 0 {
		writers = v
	}
	fx := wlockNewStore(t)

	failures := map[string]int{}
	totalRuns, totalFail, lost := 0, 0, 0
	var allTitles []string
	var okTitles []string
	for it := 0; it < iters; it++ {
		results, titles := wlockRound(t, fx, it, writers, 2)
		allTitles = append(allTitles, titles...)
		for _, r := range results {
			totalRuns++
			if r.code != 0 {
				totalFail++
				key := fmt.Sprintf("%s exit=%d %s", r.args[0], r.code, wlockErrCode(r.stdout+r.stderr))
				failures[key]++
				if failures[key] <= 2 {
					t.Logf("FAIL %v exit=%d\nstdout: %s\nstderr: %s", r.args, r.code, strings.TrimSpace(r.stdout), strings.TrimSpace(r.stderr))
				}
			} else if r.args[0] == "create" {
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
			t.Errorf("LOST: create %q exited 0 but its op is not on the remote", title)
		}
	}
	status, _ := runGitIn("", "--git-dir="+filepath.Join(fx.shared, ".act", ".git"), "--work-tree="+filepath.Join(fx.shared, ".act"), "status", "--porcelain")
	fsck, fsckErr := runGitIn("", "--git-dir="+filepath.Join(fx.shared, ".act", ".git"), "fsck", "--no-dangling")
	t.Logf("SUMMARY iters=%d writers=%d peers=2 runs=%d nonzero=%d lost_acked_creates=%d titles=%d on_remote=%d",
		iters, writers, totalRuns, totalFail, lost, len(allTitles), len(remote))
	for k, v := range failures {
		t.Logf("  failure class %q x%d", k, v)
	}
	t.Logf("shared status --porcelain:\n%s", status)
	if fsckErr != nil {
		t.Errorf("fsck on shared .act/.git failed: %v\n%s", fsckErr, fsck)
	}
}

// wlockErrCode extracts the envelope error code from act's JSON output.
func wlockErrCode(s string) string {
	i := strings.Index(s, `"code"`)
	if i < 0 {
		first := strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
		if len(first) > 80 {
			first = first[:80]
		}
		return first
	}
	rest := s[i:]
	if j := strings.Index(rest, ","); j > 0 {
		rest = rest[:j]
	}
	return rest
}
