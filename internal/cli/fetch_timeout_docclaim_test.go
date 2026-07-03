package cli

// act-76cd7a — TestDocClaim_* for the act.fetchTimeoutSeconds config key.
//
// The spec config table claims the key caps an upstream `git fetch` and
// that a fetch exceeding the budget is aborted. This test pins that claim
// at the FetchAndRebase boundary — the fetch entry point that both the
// read-cache (MaybeRefresh) and push-retry paths share.

import (
	"os/exec"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_FetchTimeout_ConfigKeyBoundsFetch — writes
// act.fetchTimeoutSeconds=1 into the nested .act/.git/config, injects a
// runner whose `git fetch` hangs (sleep 5s), and asserts FetchAndRebase
// returns an error well under the 5s sleep — proving the configured budget
// aborted the fetch rather than the value being inert.
//
// Boundary note: a real `git fetch` subprocess can't be made to hang
// deterministically across environments, so the injected-runner seam
// (WithRunner) is the tightest honest boundary for the abort behavior. The
// config key is written and read for real (config.SetGitConfig →
// resolveFetchTimeout via GetGitConfig), so the user-visible surface under
// test is the KEY, not the FetchTimeout field override.
func TestDocClaim_FetchTimeout_ConfigKeyBoundsFetch(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)
	actRoot := config.Layout(root).Root
	cfgPath := config.ActGitConfigPath(actRoot)
	if err := config.SetGitConfig(cfgPath, config.FetchTimeoutSecondsKey, "1"); err != nil {
		t.Fatalf("set %s=1: %v", config.FetchTimeoutSecondsKey, err)
	}

	// Hang only `fetch`; run everything else normally. FetchAndRebase
	// issues the fetch first, so the timeout fires before any other call.
	hangingFetch := func(name string, args ...string) *exec.Cmd {
		for _, a := range args {
			if a == "fetch" {
				return exec.Command("sleep", "5")
			}
		}
		return exec.Command(name, args...)
	}

	g := gitops.NewActGitOps(actRoot).WithRunner(hangingFetch)
	branch, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}

	start := time.Now()
	ferr := g.FetchAndRebase(branch)
	elapsed := time.Since(start)

	if ferr == nil {
		t.Fatalf("FetchAndRebase returned nil; expected a fetch-timeout error")
	}
	// The 1s budget must abort the 5s hung fetch. Allow generous slack for
	// slow CI (process kill + reap) while staying well under the sleep.
	if elapsed > 3*time.Second {
		t.Errorf("FetchAndRebase took %s; the 1s fetch budget did not abort the 5s hung fetch (key appears inert)", elapsed)
	}
}
