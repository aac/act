package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hooks"
)

// TestHookFailureDetails_PlainError: a non-HookFailedError passes through
// with isHookFailure=false so callers fall back to err.Error() under their
// existing error code.
func TestHookFailureDetails_PlainError(t *testing.T) {
	msg, details, isHook := HookFailureDetails(errors.New("boom"))
	if isHook {
		t.Fatalf("isHookFailure = true; want false for non-hook error")
	}
	if msg != "boom" {
		t.Errorf("message = %q; want %q", msg, "boom")
	}
	if details != nil {
		t.Errorf("details = %v; want nil for non-hook error", details)
	}
}

// TestHookFailureDetails_FullPayload: a HookFailedError emits both a
// human-readable Message that includes the trailing stderr excerpt and a
// structured details map for JSON consumers — the AC for act-c83a.
func TestHookFailureDetails_FullPayload(t *testing.T) {
	tail := "gofmt -l found drift:\ninternal/cli/util.go\nexit 1"
	herr := &hooks.HookFailedError{
		Code:       1,
		Truncated:  false,
		StderrTail: tail,
	}
	msg, details, isHook := HookFailureDetails(herr)
	if !isHook {
		t.Fatalf("isHookFailure = false; want true")
	}
	// Human message: starts with exit code and contains the full tail
	// (3 lines, less than hookStderrExcerptLines).
	if !strings.HasPrefix(msg, "hook exited 1:\n") {
		t.Errorf("message = %q; want prefix %q", msg, "hook exited 1:\n")
	}
	if !strings.Contains(msg, "gofmt -l found drift") {
		t.Errorf("message %q missing tail content", msg)
	}
	// Details preserve the full tail under hook_stderr.
	if got, _ := details["hook_stderr"].(string); got != tail {
		t.Errorf("details.hook_stderr = %q; want %q", got, tail)
	}
	if got, _ := details["hook_exit_code"].(int); got != 1 {
		t.Errorf("details.hook_exit_code = %v; want 1", details["hook_exit_code"])
	}
	if got, _ := details["hook_truncated"].(bool); got {
		t.Errorf("details.hook_truncated = true; want false")
	}
}

// TestHookFailureDetails_LongStderrTrimmedInExcerpt: the inline excerpt
// shows only the last hookStderrExcerptLines lines, while details preserves
// the full captured tail (up to MaxStderrTail = 4096 bytes per hooks.Run).
func TestHookFailureDetails_LongStderrTrimmedInExcerpt(t *testing.T) {
	// Build a 30-line stderr; expect the inline excerpt to contain only
	// the last hookStderrExcerptLines (10) lines.
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	tail := b.String()

	herr := &hooks.HookFailedError{Code: 2, StderrTail: tail}
	msg, details, _ := HookFailureDetails(herr)

	if !strings.Contains(msg, "line-29") {
		t.Errorf("message missing last line: %q", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("line-%d", 30-hookStderrExcerptLines)) {
		t.Errorf("message missing earliest excerpt line: %q", msg)
	}
	// Lines beyond the excerpt window must NOT appear inline.
	if strings.Contains(msg, "line-0\n") || strings.Contains(msg, "line-5\n") {
		t.Errorf("message contains lines outside the %d-line excerpt: %q",
			hookStderrExcerptLines, msg)
	}
	// Full tail still available to JSON consumers.
	if got, _ := details["hook_stderr"].(string); !strings.Contains(got, "line-0\n") {
		t.Errorf("details.hook_stderr missing early lines: %q", got)
	}
}

// TestHookFailureDetails_EmptyStderr: hook exited non-zero but wrote no
// stderr — message is just "hook exited N", and details omits hook_stderr
// rather than carrying an empty string.
func TestHookFailureDetails_EmptyStderr(t *testing.T) {
	herr := &hooks.HookFailedError{Code: 1}
	msg, details, _ := HookFailureDetails(herr)
	if msg != "hook exited 1" {
		t.Errorf("message = %q; want %q", msg, "hook exited 1")
	}
	if _, present := details["hook_stderr"]; present {
		t.Errorf("details.hook_stderr present with empty stderr; want absent")
	}
}

// TestLastLines_TrailingNewlinesTrimmed: a stderr blob ending in "\n"
// must not produce a phantom blank line in the excerpt.
func TestLastLines_TrailingNewlinesTrimmed(t *testing.T) {
	got := lastLines("a\nb\nc\n", 2)
	want := "b\nc"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// makeRepoWithIssue is a close-tests helper specialised for hook tests:
// returns (root, paths, issueID).
func makeRepoWithIssue(t *testing.T) (string, config.LayoutPaths, string) {
	t.Helper()
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "to-close", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: code = %d, out=%+v", code, out)
	}
	return root, config.Layout(root), out.(CreateResult).ID
}

// writeHook writes an executable shell hook at paths.Hooks/<name>.
func writeHook(t *testing.T, paths config.LayoutPaths, name, script string) {
	t.Helper()
	hookPath := filepath.Join(paths.Hooks, name)
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s hook: %v", name, err)
	}
}

// TestRunClose_HookFailureSurfacesStderr is the integration AC for
// act-c83a: a failing .act/hooks/close must surface its stderr both
// inline (last lines in Message) and verbatim (full tail in Details).
func TestRunClose_HookFailureSurfacesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook scripts not supported on windows")
	}
	root, paths, id := makeRepoWithIssue(t)
	writeHook(t, paths, "close", "#!/bin/sh\n"+
		"echo 'pre-close gate: gofmt drift in internal/cli/util.go' 1>&2\n"+
		"echo 'pre-close gate: run gofmt -w to fix' 1>&2\n"+
		"exit 1\n")

	out, code := RunClose(root, CloseOptions{ID: id})
	if code != 1 {
		t.Fatalf("code = %d, want 1; out=%+v", code, out)
	}
	errOut, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CloseErrorOutput", out)
	}
	if errOut.Error != "hook_failed" {
		t.Errorf("error = %q; want hook_failed", errOut.Error)
	}
	// Human Message: starts with "hook exited 1:" and includes the
	// trailing stderr lines so the user can diagnose without re-running.
	if !strings.HasPrefix(errOut.Message, "hook exited 1:") {
		t.Errorf("message = %q; want prefix %q", errOut.Message, "hook exited 1:")
	}
	if !strings.Contains(errOut.Message, "gofmt drift") {
		t.Errorf("message %q does not include stderr signal", errOut.Message)
	}
	// Details: full captured stderr under hook_stderr, exit code under
	// hook_exit_code. JSON consumers rely on this contract.
	tail, _ := errOut.Details["hook_stderr"].(string)
	if !strings.Contains(tail, "gofmt drift") || !strings.Contains(tail, "run gofmt -w") {
		t.Errorf("details.hook_stderr missing expected lines: %q", tail)
	}
	if code, _ := errOut.Details["hook_exit_code"].(int); code != 1 {
		t.Errorf("details.hook_exit_code = %v; want 1", errOut.Details["hook_exit_code"])
	}
}

// TestRunCreate_HookFailureSurfacesStderr covers the create path: a
// .act/hooks/create that exits non-zero must surface stderr the same
// way as close — the helper is shared, so this guards the wiring in
// internal/cli/create.go (not the helper itself).
func TestRunCreate_HookFailureSurfacesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook scripts not supported on windows")
	}
	root := makeCreateRepo(t)
	paths := config.Layout(root)
	writeHook(t, paths, "create", "#!/bin/sh\n"+
		"echo 'create-gate: type=task not allowed by policy' 1>&2\n"+
		"exit 1\n")

	out, code := RunCreate(root, CreateOptions{Title: "blocked", Type: "task"})
	if code != 1 {
		t.Fatalf("code = %d, want 1; out=%+v", code, out)
	}
	errOut, ok := out.(CreateErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CreateErrorOutput", out)
	}
	if errOut.Error != "hook_failed" {
		t.Errorf("error = %q; want hook_failed (the create path's hook failure should not be mis-labelled write_failed)", errOut.Error)
	}
	if !strings.Contains(errOut.Message, "type=task not allowed") {
		t.Errorf("message %q missing hook stderr signal", errOut.Message)
	}
	tail, _ := errOut.Details["hook_stderr"].(string)
	if !strings.Contains(tail, "type=task not allowed") {
		t.Errorf("details.hook_stderr missing expected line: %q", tail)
	}
}
