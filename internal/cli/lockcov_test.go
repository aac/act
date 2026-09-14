package cli

// act-94bbea: lock coverage for the nested-repo commit/push paths in this
// package that act-38330b's write lock did not reach. Each test holds
// .act/.write.lock from the test process (a separate flock open file
// description, so it contends with the code under test exactly as a sibling
// act process would), shortens the bounded wait, and asserts the path times
// out with nothing published — proof the path acquires the lock before it
// touches the nested repo.

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/flock"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/testfixtures"
)

// lockcovHold takes .act/.write.lock under host and sets a short bounded
// wait. The returned release is also registered with t.Cleanup.
func lockcovHold(t *testing.T, host string) func() {
	t.Helper()
	t.Setenv("ACT_WRITE_LOCK_TIMEOUT_MS", "150")
	rel, locked, err := flock.TryLock(filepath.Join(host, ".act", gitops.WriteLockFile))
	if err != nil || !locked {
		t.Fatalf("hold write lock: locked=%v err=%v", locked, err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			rel()
		}
	}
	t.Cleanup(release)
	return release
}

// lockcovAssertTimeoutEnvelope asserts out is a write_lock_timeout envelope
// with exit 1.
func lockcovAssertTimeoutEnvelope(t *testing.T, what string, out any, code int) {
	t.Helper()
	m, ok := out.(map[string]any)
	if code != 1 || !ok || m["error"] != ErrWriteLockTimeout {
		t.Fatalf("%s under a held write lock: code=%d out=%v; want exit 1 write_lock_timeout", what, code, out)
	}
}

// lockcovUpstreamRef returns refs/heads/main on the bare upstream ("" if
// unset).
func lockcovUpstreamRef(t *testing.T, bare string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+bare, "rev-parse", "--verify", "--quiet", "refs/heads/main").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func TestLockCoverage_RemoteSync(t *testing.T) {
	host, upstream := syncFixtureWithUpstream(t)
	release := lockcovHold(t, host)

	out, code := RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	lockcovAssertTimeoutEnvelope(t, "act remote sync", out, code)
	if ref := lockcovUpstreamRef(t, upstream.Path); ref != "" {
		t.Fatalf("upstream main advanced to %s while the write lock was held", ref)
	}

	release()
	out, code = RunRemoteSync(RemoteSyncOptions{SourceCWD: host})
	if res, ok := out.(RemoteSyncResult); code != 0 || !ok || !res.Pushed {
		t.Fatalf("sync after release: code=%d out=%v; want a push", code, out)
	}
}

func TestLockCoverage_RemoteAddUpstream(t *testing.T) {
	host := addUpstreamFixture(t)
	upstream := testfixtures.NewBareRemote(t)
	mustExecSync(t, "git", "--git-dir="+upstream.Path, "update-ref", "-d", "refs/heads/main")
	lockcovHold(t, host)

	out, code := RunRemoteAddUpstream(RemoteAddUpstreamOptions{URL: upstream.URL, SourceCWD: host})
	lockcovAssertTimeoutEnvelope(t, "act remote add-upstream", out, code)
	if ref := lockcovUpstreamRef(t, upstream.Path); ref != "" {
		t.Fatalf("upstream main advanced to %s while the write lock was held", ref)
	}
}

// TestLockCoverage_ClaimPush: the post-win claim push acquires the lock.
func TestLockCoverage_ClaimPush(t *testing.T) {
	host := newRemoteFixture(t)
	lockcovHold(t, host)

	c := &claimGitOps{inner: gitops.NewActGitOps(filepath.Join(host, ".act"))}
	if err := c.Push(); !errors.Is(err, gitops.ErrWriteLockTimeout) {
		t.Fatalf("claimGitOps.Push under a held write lock: err=%v; want ErrWriteLockTimeout", err)
	}
}
