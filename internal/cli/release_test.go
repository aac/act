package cli

// Release-pipeline tests. Spec §Distribution requires that the release
// workflow embed the tag (the user-visible binary version) into the
// produced binary via -ldflags so `act version --json` reports it.
//
// This test exercises the same ldflag plumbing the release workflow uses
// (-X github.com/aac/act/internal/cli.BinaryVersion=...) so a refactor
// that turns BinaryVersion back into a const, drops the module path, or
// otherwise breaks the override is caught locally before tagging.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestVersionEmbedsBuildVersion builds cmd/act with a custom ldflag and
// asserts the resulting binary's `act version --json` output reports
// the injected string in binary_version.
func TestVersionEmbedsBuildVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping release ldflag test in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getcwd: %v", err)
	}
	// internal/cli/ -> ../.. is the module root (where go.mod lives).
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "act-relbin")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	const want = "test-version"
	ldflags := "-X github.com/aac/act/internal/cli.BinaryVersion=" + want

	build := exec.Command(
		"go", "build",
		"-ldflags", ldflags,
		"-o", bin,
		"./cmd/act",
	)
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	var buildErr bytes.Buffer
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		t.Fatalf("go build with ldflags failed: %v\nstderr:\n%s", err, buildErr.String())
	}

	run := exec.Command(bin, "version", "--json")
	var out, errBuf bytes.Buffer
	run.Stdout = &out
	run.Stderr = &errBuf
	if err := run.Run(); err != nil {
		t.Fatalf("act version --json failed: %v\nstderr:\n%s", err, errBuf.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("parse JSON: %v\noutput:\n%s", err, out.String())
	}

	got, _ := payload["binary_version"].(string)
	if got != want {
		t.Fatalf("binary_version: got %q, want %q (ldflag did not take effect)\nfull output: %s",
			got, want, out.String())
	}
}
