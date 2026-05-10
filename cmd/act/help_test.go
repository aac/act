package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunHelpDefault asserts the no-arg tutorial covers the load-bearing
// onboarding concepts an agent needs to start being useful: the work
// loop primitives, commit marker convention, and pointers to deeper
// topics. If a future change drops one of these, the tutorial has
// regressed below "agent can onboard from this alone".
func TestRunHelpDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runHelpTo(&stdout, &stderr, nil); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr: %q", stderr.String())
	}
	out := stdout.String()
	mustContain(t, out, []string{
		"act ready",
		"act update --claim",
		"act close",
		"commit_marker",
		"(act-",
		"act help workflow",
		"act help ops-model",
	})
}

func TestRunHelpWorkflow(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runHelpTo(&stdout, &stderr, []string{"workflow"}); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	out := stdout.String()
	mustContain(t, out, []string{
		"THE LOOP IN DETAIL",
		"Pulling work",
		"ESCAPE HATCHES",
	})
}

func TestRunHelpOpsModel(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runHelpTo(&stdout, &stderr, []string{"ops-model"}); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	out := stdout.String()
	mustContain(t, out, []string{
		"EVERY WRITE IS AN OP FILE",
		"LOGICAL CONFLICTS",
		"INDEX",
	})
}

// TestRunHelpOpsModelAliases lets agents type the topic in any of three
// natural forms without hitting a usage error.
func TestRunHelpOpsModelAliases(t *testing.T) {
	for _, alias := range []string{"ops-model", "opsmodel", "ops_model", "OPS-MODEL"} {
		var stdout, stderr bytes.Buffer
		if got := runHelpTo(&stdout, &stderr, []string{alias}); got != 0 {
			t.Errorf("alias %q: exit = %d, want 0; stderr=%q", alias, got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "EVERY WRITE IS AN OP FILE") {
			t.Errorf("alias %q: missing ops-model content", alias)
		}
	}
}

func TestRunHelpUnknownTopic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := runHelpTo(&stdout, &stderr, []string{"bogus"})
	if got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Errorf("unexpected stdout: %q", stdout.String())
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "bogus") {
		t.Errorf("stderr should name the unknown topic; got %q", errOut)
	}
	if !strings.Contains(errOut, "workflow") || !strings.Contains(errOut, "ops-model") {
		t.Errorf("stderr should list valid topics; got %q", errOut)
	}
}

func mustContain(t *testing.T, out string, needles []string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Errorf("output missing %q", n)
		}
	}
}
