package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/hooks"
)

// act-8ee085 follow-through: the hook timeout is a host property, and a
// hook act killed must not be reported as a hook that refused.
//
// THE BUG. The timeout was a 300s constant, and HookFailureDetails rendered
// every hook failure as `hook exited 1` regardless of cause. On a machine
// where this repo's own close gate takes 15m36s, `act close` therefore
// refused every close with a message describing a test failure that had not
// happened — measured with a 320s no-op hook: killed at exactly 300s,
// reported as `{"error":"hook_failed","message":"hook exited 1"}`.

// TestDocClaim_HookTimeoutIsConfigurable asserts spec §Hooks contract step 6:
// the limit defaults to 300s and ACT_HOOK_TIMEOUT overrides it, with a bad
// value falling back rather than failing the write.
func TestDocClaim_HookTimeoutIsConfigurable(t *testing.T) {
	if got := resolveHookTimeout(); got != defaultHookTimeout {
		t.Errorf("with no env set: %v; want the %v default", got, defaultHookTimeout)
	}
	for _, tc := range []struct {
		env  string
		want time.Duration
	}{
		{"20m", 20 * time.Minute},
		{"90s", 90 * time.Second},
		{"", defaultHookTimeout},
		{"   ", defaultHookTimeout},
		{"not-a-duration", defaultHookTimeout},
		{"0s", defaultHookTimeout},
		{"-5m", defaultHookTimeout},
	} {
		t.Setenv(hookTimeoutEnv, tc.env)
		if got := resolveHookTimeout(); got != tc.want {
			t.Errorf("%s=%q -> %v; want %v", hookTimeoutEnv, tc.env, got, tc.want)
		}
	}
}

// TestDocClaim_HookTimeoutReportedAsTimeout asserts the other half of step 6:
// a killed hook says it timed out, names the limit, and names the knob —
// rather than borrowing the wording of a non-zero exit.
func TestDocClaim_HookTimeoutReportedAsTimeout(t *testing.T) {
	t.Setenv(hookTimeoutEnv, "7m")
	herr := &hooks.HookFailedError{Code: 1, Cause: "timeout"}
	msg, details, isHook := HookFailureDetails(herr)
	if !isHook {
		t.Fatalf("isHookFailure = false; want true")
	}
	if strings.Contains(msg, "hook exited") {
		t.Errorf("a timeout is reported as an exit: %q", msg)
	}
	for _, want := range []string{"timed out", "7m0s", hookTimeoutEnv} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
	if got, _ := details["hook_cause"].(string); got != "timeout" {
		t.Errorf("details.hook_cause = %q; want \"timeout\"", got)
	}
	if got, _ := details["hook_timeout"].(string); got != "7m0s" {
		t.Errorf("details.hook_timeout = %q; want \"7m0s\"", got)
	}

	// A genuine non-zero exit keeps its old wording, and is not relabelled.
	msg, details, _ = HookFailureDetails(&hooks.HookFailedError{Code: 3, Cause: "exit"})
	if !strings.HasPrefix(msg, "hook exited 3") {
		t.Errorf("exit failure message = %q; want it to start with \"hook exited 3\"", msg)
	}
	if got, _ := details["hook_cause"].(string); got != "exit" {
		t.Errorf("details.hook_cause = %q; want \"exit\"", got)
	}
}

// TestDocClaim_SlowHookSurvivesRaisedTimeout runs the whole path end to end:
// a hook that outlives the default limit passes when the environment raises
// it. This is the property that matters — the unit tests above can both pass
// while the constant stays wired in somewhere.
func TestDocClaim_SlowHookSurvivesRaisedTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution is POSIX-only in v0.1")
	}
	if testing.Short() {
		t.Skip("spawns a real hook")
	}
	root, id := makeCloseRepoWithIssue(t)
	dir := filepath.Join(root, ".act", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	// Sleeps past a deliberately tiny limit, so the two configurations
	// differ only in the environment.
	if err := os.WriteFile(filepath.Join(dir, "close"), []byte("#!/bin/sh\nsleep 2\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	t.Setenv(hookTimeoutEnv, "500ms")
	out, code := RunClose(root, CloseOptions{ID: id})
	if code == 0 {
		t.Fatalf("with a 500ms limit the slow hook should have been killed; got exit 0")
	}
	if e, ok := out.(CloseErrorOutput); ok {
		if !strings.Contains(e.Message, "timed out") {
			t.Errorf("killed hook reported as %q; want a timeout", e.Message)
		}
	}

	t.Setenv(hookTimeoutEnv, "5m")
	if _, code := RunClose(root, CloseOptions{ID: id}); code != 0 {
		t.Fatalf("with a 5m limit the same hook should pass; got exit %d", code)
	}
}
