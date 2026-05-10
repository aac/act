package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// actBinary returns the absolute path to the freshly-built act binary at
// ./bin/act, or skips the test if it isn't present (Makefile / CI builds it).
// We avoid `go run` from a test because race-y rebuilds inside `go test ./...`
// have caused intermittent ENOTEXIT in this repo before.
func actBinary(t *testing.T) string {
	t.Helper()
	// Resolve relative to the package's own dir using runtime.Caller; works
	// no matter where `go test` is invoked from.
	_, here, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
	bin := filepath.Join(repoRoot, "bin", "act")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

// run executes the act binary with the given args and returns
// (stdout, stderr, exitCode). exitCode is the OS exit code; -1 if the
// process couldn't be started.
func runAct(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	bin := actBinary(t)
	cmd := exec.Command(bin, args...)
	var outB, errB strings.Builder
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	err := cmd.Run()
	if err == nil {
		return outB.String(), errB.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return outB.String(), errB.String(), ee.ExitCode()
	}
	t.Fatalf("act %v: failed to launch: %v", args, err)
	return "", "", -1
}

// TestUnknownSubcommandMsg locks in the canonical "unknown subcommand"
// shape so a future change to the message format is a deliberate edit
// to the test rather than a silent UX regression.
func TestUnknownSubcommandMsg(t *testing.T) {
	got := unknownSubcommandMsg("foo")
	for _, want := range []string{"unknown subcommand", `"foo"`, "act help"} {
		if !strings.Contains(got, want) {
			t.Errorf("unknownSubcommandMsg(\"foo\") = %q; missing %q", got, want)
		}
	}
	if strings.Contains(got, "not implemented") {
		t.Errorf("unknownSubcommandMsg should not say 'not implemented': %q", got)
	}
}

func TestDepUsageMsg(t *testing.T) {
	got := depUsageMsg()
	for _, want := range []string{"usage", "verbs", "add"} {
		if !strings.Contains(got, want) {
			t.Errorf("depUsageMsg() = %q; missing %q", got, want)
		}
	}
	if strings.Contains(got, "not implemented") {
		t.Errorf("depUsageMsg should not say 'not implemented': %q", got)
	}
}

func TestUnknownDepVerbMsg(t *testing.T) {
	got := unknownDepVerbMsg("rm")
	for _, want := range []string{"unknown verb", `"rm"`, "act dep --help"} {
		if !strings.Contains(got, want) {
			t.Errorf("unknownDepVerbMsg(\"rm\") = %q; missing %q", got, want)
		}
	}
	if strings.Contains(got, "not implemented") {
		t.Errorf("unknownDepVerbMsg should not say 'not implemented': %q", got)
	}
}

// TestDispatchUnknownSubcommand asserts the integrated dispatch path
// surfaces the new message (not the legacy "not implemented yet").
func TestDispatchUnknownSubcommand(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	_, stderr, code := runAct(t, "asdfqwer")
	if code != 2 {
		t.Errorf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "unknown subcommand") || !strings.Contains(stderr, "asdfqwer") {
		t.Errorf("stderr missing expected message; got %q", stderr)
	}
}

// TestDispatchBareDep asserts `act dep` with no verb prints the verb
// list, not "not implemented yet".
func TestDispatchBareDep(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	_, stderr, code := runAct(t, "dep")
	if code != 2 {
		t.Errorf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "verbs") || !strings.Contains(stderr, "add") {
		t.Errorf("stderr should list available verbs; got %q", stderr)
	}
}

// TestDispatchDepHelp asserts `act dep --help` prints the dep usage
// and exits 0 (not 2 with a misleading "not implemented yet").
func TestDispatchDepHelp(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	for _, flag := range []string{"--help", "-h", "help"} {
		_, stderr, code := runAct(t, "dep", flag)
		if code != 0 {
			t.Errorf("act dep %s: exit = %d, want 0; stderr=%q", flag, code, stderr)
		}
		if !strings.Contains(stderr, "verbs") || !strings.Contains(stderr, "add") {
			t.Errorf("act dep %s: stderr should list verbs; got %q", flag, stderr)
		}
	}
}

func TestDispatchDepUnknownVerb(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	_, stderr, code := runAct(t, "dep", "rm")
	if code != 2 {
		t.Errorf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "unknown verb") || !strings.Contains(stderr, "rm") {
		t.Errorf("stderr missing expected message; got %q", stderr)
	}
}

// TestDispatchDepFlagShapedToken covers the case where the first token
// after `dep` is flag-shaped but not -h/--help/help — e.g. `act dep --json`.
// Should route to the dep-usage / bad-flag path, not the unknown-verb path.
func TestDispatchDepFlagShapedToken(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	_, stderr, code := runAct(t, "dep", "--json")
	if code != 2 {
		t.Errorf("exit = %d, want 2; stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "not implemented") {
		t.Errorf("flag-shaped token should not yield 'not implemented': %q", stderr)
	}
}
