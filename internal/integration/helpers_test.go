// Package integration holds the Phase 2 end-to-end coordination-plane
// tests (act-612646). The package is test-only — there are no
// non-_test.go files. Helpers live here; the five behaviors live in
// phase2_test.go.
//
// Why a separate package: the cli package's existing TestMain builds
// the act binary into a temp dir scoped to the cli package's test
// process. Driving the Phase 2 lifecycle end-to-end means cross-cutting
// multiple packages (cli + gitops + testfixtures), and the integration
// suite needs its own binary handle anyway so a `go test
// ./internal/integration/...` invocation does not transitively depend
// on the cli package's test binary.
//
// The five Phase 2 behaviors covered (one test each, all `t.Parallel`):
//
//	TestE2E_TwoMachineRoundTrip       — AC1: two .act/.git clones,
//	                                    one shared bare remote, pushes
//	                                    propagate via fetch.
//	TestE2E_PushContentionFourByFifty — AC2: 4 parallel workers each
//	                                    pushing 50 ops to one bare;
//	                                    the union of 200 ops survives.
//	TestE2E_UpstreamDrift             — AC3: 60 ops without sync makes
//	                                    doctor flag case (h); a single
//	                                    `act remote sync` clears it.
//	TestE2E_SlowFilesystem            — AC4: ACT_TEST_SLOW_COMMIT_MS
//	                                    drives the slow-write append
//	                                    log; doctor surfaces the count.
//	TestE2E_DispatchLoop              — AC5: two parallel workers, each
//	                                    pushing an op back to the
//	                                    orchestrator; the post-receive
//	                                    hook fires `act remote sync`
//	                                    for both events.

package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// actBinaryPath is set by TestMain to the absolute path of the freshly
// built `act` binary. The integration tests that shell out to the
// binary read this; tests that drive the in-process entry points
// (RunHarvest, RunDoctor, RunRemoteSync, RunCreate, RunInit) do not
// need the binary at all.
var actBinaryPath string

// TestMain compiles the act binary into a temp file shared across the
// package's tests. Mirrors the pattern in internal/cli's
// concurrent_helper_test.go so the integration suite has its own
// binary handle without depending on the cli package's TestMain.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "act-integration-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdtemp: %v\n", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "act")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: getcwd: %v\n", err)
		os.Exit(2)
	}
	// internal/integration → walk up two segments to the repo root.
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/act")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: go build act: %v\n", err)
		os.Exit(2)
	}
	actBinaryPath = bin
	os.Exit(m.Run())
}

// mustGitIn runs `git <args>` with cwd=dir and t.Fatalfs on error.
// Returns combined output.
func mustGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// runGitIn is the non-fatal twin of mustGitIn. Used by the contention
// test where a non-fast-forward rejection is expected and recoverable.
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// configureRepo applies the user identity + gpg-off triple that every
// fixture clone needs so commits don't fail under a stripped CI env.
func configureRepo(t *testing.T, dir, email, name string) {
	t.Helper()
	mustGitIn(t, dir, "config", "user.email", email)
	mustGitIn(t, dir, "config", "user.name", name)
	mustGitIn(t, dir, "config", "commit.gpgsign", "false")
	mustGitIn(t, dir, "config", "tag.gpgsign", "false")
}

// runActSubprocess invokes the prebuilt act binary in cwd with the
// given args. Returns stdout, stderr, exit code. Does not fail on
// non-zero exit — the caller asserts.
func runActSubprocess(t *testing.T, cwd string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	if actBinaryPath == "" {
		t.Fatalf("runActSubprocess: actBinaryPath not set (TestMain did not run?)")
	}
	cmd := exec.Command(actBinaryPath, args...)
	cmd.Dir = cwd
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("runActSubprocess: %s %s: %v", cwd, strings.Join(args, " "), err)
		}
	}
	return so.String(), se.String(), code
}

// mustRunActSubprocess is the must-equal-want twin of runActSubprocess.
func mustRunActSubprocess(t *testing.T, cwd string, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	so, se, code := runActSubprocess(t, cwd, args...)
	if code != want {
		t.Fatalf("act %s in %s: exit %d (want %d)\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), cwd, code, want, so, se)
	}
	return so, se
}

// countJSONFilesUnder counts every regular file ending in `.json` in
// the tree rooted at `root`. Used by the contention test to count the
// total op files visible on a bare-clone view of the final state.
func countJSONFilesUnder(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// Tolerate a missing root: a worker that wrote zero ops is
			// still a legitimate outcome and the walk simply returns 0.
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".json") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}
