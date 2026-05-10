package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/hooks"
	"github.com/aac/act/internal/op"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// makeWriteRepo initializes a git repo plus the .act layout under repoRoot
// and returns the LayoutPaths. Distinct from init_test.go's makeRepo.
func makeWriteRepo(t *testing.T) (string, config.LayoutPaths) {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.email", "u@example.com")
	mustGit(t, dir, "config", "user.name", "U")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, dir, "add", "README")
	mustGit(t, dir, "commit", "-q", "--no-verify", "-m", "init")

	paths := config.Layout(dir)
	if err := config.InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	return dir, paths
}

// fixedEnvelope builds a minimal valid envelope for testing.
func fixedEnvelope(t *testing.T) (op.Envelope, []byte) {
	t.Helper()
	payload, err := json.Marshal(op.ClaimPayload{Assignee: "alice"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "claim",
		IssueID:       "act-deadbeefdeadbeef",
		Payload:       payload,
		HLC:           hlc.HLC{Wall: 1700000000000, Logical: 0, NodeID: "abcdef01"},
		NodeID:        "abcdef01",
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return env, body
}

func TestWriteOpAndAutoCommitBasic(t *testing.T) {
	dir, paths := makeWriteRepo(t)
	g := gitops.NewGitOps(dir)
	env, body := fixedEnvelope(t)

	if err := WriteOpAndAutoCommit(env, body, paths, g, WriteOpts{}); err != nil {
		t.Fatalf("WriteOpAndAutoCommit: %v", err)
	}
	subj := strings.TrimSpace(runOut(t, dir, "git", "log", "-1", "--format=%s"))
	if !strings.HasPrefix(subj, "act-op: ") || !strings.HasSuffix(subj, " claim") {
		t.Fatalf("subject = %q", subj)
	}
}

func TestWriteOpNoCommit(t *testing.T) {
	dir, paths := makeWriteRepo(t)
	env, body := fixedEnvelope(t)
	headBefore := strings.TrimSpace(runOut(t, dir, "git", "rev-parse", "HEAD"))

	if err := WriteOpAndAutoCommit(env, body, paths, nil, WriteOpts{NoCommit: true}); err != nil {
		t.Fatalf("WriteOpAndAutoCommit: %v", err)
	}
	headAfter := strings.TrimSpace(runOut(t, dir, "git", "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Fatalf("expected no new commit; HEAD changed %s -> %s", headBefore, headAfter)
	}
	// Op file must exist on disk.
	matches, err := filepath.Glob(filepath.Join(paths.Ops, env.IssueID, "*", "*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no op file written: %v matches=%d", err, len(matches))
	}
}

func TestWriteOpInvalidFlags(t *testing.T) {
	dir, paths := makeWriteRepo(t)
	g := gitops.NewGitOps(dir)
	env, body := fixedEnvelope(t)

	cases := []WriteOpts{
		{NoCommit: true, Push: true},
		{Isolated: true, Push: true},
	}
	for _, opts := range cases {
		if err := WriteOpAndAutoCommit(env, body, paths, g, opts); !errors.Is(err, ErrInvalidFlags) {
			t.Errorf("opts=%+v: want ErrInvalidFlags, got %v", opts, err)
		}
	}
}

// TestWriteOpAndAutoCommitHookSuccess verifies the hook is executed
// pre-commit and the op is committed when the hook exits 0.
func TestWriteOpAndAutoCommitHookSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook scripts not supported on windows")
	}
	dir, paths := makeWriteRepo(t)
	g := gitops.NewGitOps(dir)
	env, body := fixedEnvelope(t)

	hookPath := filepath.Join(paths.Hooks, "claim")
	marker := filepath.Join(dir, "hook-fired")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if err := WriteOpAndAutoCommit(env, body, paths, g, WriteOpts{}); err != nil {
		t.Fatalf("WriteOpAndAutoCommit: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("hook did not fire: %v", err)
	}
	subj := strings.TrimSpace(runOut(t, dir, "git", "log", "-1", "--format=%s"))
	if !strings.HasPrefix(subj, "act-op: ") {
		t.Errorf("expected commit; got %q", subj)
	}
}

// TestWriteOpAndAutoCommitHookFailure verifies a non-zero hook exit
// results in HookFailedError, the staged op is unstaged, the op file is
// deleted, and no commit lands.
func TestWriteOpAndAutoCommitHookFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook scripts not supported on windows")
	}
	dir, paths := makeWriteRepo(t)
	g := gitops.NewGitOps(dir)
	env, body := fixedEnvelope(t)
	headBefore := strings.TrimSpace(runOut(t, dir, "git", "rev-parse", "HEAD"))

	hookPath := filepath.Join(paths.Hooks, "claim")
	script := "#!/bin/sh\nprintf 'boom' 1>&2\nexit 1\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	err := WriteOpAndAutoCommit(env, body, paths, g, WriteOpts{})
	var herr *hooks.HookFailedError
	if !errors.As(err, &herr) {
		t.Fatalf("err = %v; want *hooks.HookFailedError", err)
	}
	if herr.Code != 1 {
		t.Errorf("Code = %d; want 1", herr.Code)
	}
	if herr.StderrTail != "boom" {
		t.Errorf("StderrTail = %q; want %q", herr.StderrTail, "boom")
	}
	// No commit must have been created.
	headAfter := strings.TrimSpace(runOut(t, dir, "git", "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Errorf("HEAD moved %s -> %s; expected unchanged", headBefore, headAfter)
	}
	// Op file deleted.
	matches, _ := filepath.Glob(filepath.Join(paths.Ops, env.IssueID, "*", "*.json"))
	if len(matches) != 0 {
		t.Errorf("expected op file removed; found %v", matches)
	}
	// Staging area must not contain the op file. (Other untracked
	// `.act/` artifacts from InitDirs are unrelated to the hook
	// failure; we look for staged adds specifically.)
	staged := strings.TrimSpace(runOut(t, dir, "git", "diff", "--cached", "--name-only"))
	if staged != "" {
		t.Errorf("staging area not clean after hook failure: %q", staged)
	}
}

func runOut(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return string(out)
}
