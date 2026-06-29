package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// testBinaryOnce builds the act binary exactly once per test binary run.
var testBinaryOnce struct {
	sync.Once
	path string
	err  error
}

// TestMain builds a fresh act binary into a per-run temp dir before any tests
// execute, so subprocess tests always exercise the current source tree rather
// than a possibly-stale ./bin/act artifact.
func TestMain(m *testing.M) {
	// Build once; subsequent calls to actBinary(t) return the cached path.
	testBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "act-test-bin-*")
		if err != nil {
			testBinaryOnce.err = fmt.Errorf("create temp dir for test binary: %w", err)
			return
		}
		bin := filepath.Join(dir, "act")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		// Resolve the repo root relative to this source file so the build
		// target is correct regardless of the `go test` invocation directory.
		_, here, _, _ := runtime.Caller(0)
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/act")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			testBinaryOnce.err = fmt.Errorf("build act binary: %w\n%s", err, out)
			return
		}
		testBinaryOnce.path = bin
	})
	os.Exit(m.Run())
}
