package gitops

// act-76cd7a — unit tests for the fetch wall-time budget machinery:
// resolveFetchTimeout precedence and runCombinedTimeout's kill-on-expiry.
// These exercise the mechanism directly and fast (sub-second via the
// FetchTimeout field); the config-key end-to-end path is asserted at the
// FetchAndRebase boundary in internal/cli (TestDocClaim_FetchTimeout_*).

import (
	"os/exec"
	"testing"
	"time"
)

// TestRunCombinedTimeout_AbortsHang: a 50ms budget kills a `sleep 5`
// injected as the git command and returns promptly with a non-nil error.
func TestRunCombinedTimeout_AbortsHang(t *testing.T) {
	g := &GitOps{
		RepoRoot: t.TempDir(),
		runner: func(name string, args ...string) *exec.Cmd {
			return exec.Command("sleep", "5")
		},
	}
	start := time.Now()
	_, err := g.runCombinedTimeout(50*time.Millisecond, "fetch", "origin", "main")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("runCombinedTimeout took %s; the 50ms budget did not abort the sleep", elapsed)
	}
}

// TestRunCombinedTimeout_ZeroDelegatesUnbounded: a zero budget delegates to
// runCombined (no kill machinery) and a fast command completes normally.
func TestRunCombinedTimeout_ZeroDelegatesUnbounded(t *testing.T) {
	g := &GitOps{RepoRoot: t.TempDir(), runner: exec.Command}
	out, err := g.runCombinedTimeout(0, "version")
	if err != nil {
		t.Fatalf("git version via zero-budget delegate: %v (out=%s)", err, out)
	}
	if out == "" {
		t.Errorf("expected `git version` output, got empty")
	}
}

// TestResolveFetchTimeout_Precedence: the FetchTimeout field wins when set;
// an unset field with no readable config key yields 0 (unbounded).
func TestResolveFetchTimeout_Precedence(t *testing.T) {
	// Field override wins.
	g := &GitOps{RepoRoot: t.TempDir(), FetchTimeout: 7 * time.Second}
	if got := g.resolveFetchTimeout(); got != 7*time.Second {
		t.Errorf("field override: got %s, want 7s", got)
	}

	// No field, no config file at RepoRoot → unbounded (0).
	g2 := &GitOps{RepoRoot: t.TempDir()}
	if got := g2.resolveFetchTimeout(); got != 0 {
		t.Errorf("unset key: got %s, want 0 (unbounded)", got)
	}
}
