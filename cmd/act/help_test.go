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
		"act help errors",
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
		// Commit-marker invariants per act-aa8c: format, source,
		// doctor's substring-match guarantee, the don't-hand-roll rule.
		"COMMIT MARKER INVARIANTS",
		"ShortestUniquePrefixes",
		"--commit-marker",
		"do NOT slice the id by hand",
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
	if !strings.Contains(errOut, "workflow") || !strings.Contains(errOut, "ops-model") || !strings.Contains(errOut, "errors") {
		t.Errorf("stderr should list valid topics; got %q", errOut)
	}
}

// TestRunHelpErrors asserts that the error-envelope topic covers every
// load-bearing piece of the contract: shape, the always-present details
// key, a representative slice of code categories from both tiers, the
// byte-counted length rule with a pointer to its precedent, and how to
// emit. If a future change drops one of these, an agent picking up an
// unrelated issue would have to fall back to reading internal/cli/
// errors.go — which is exactly the regression act-acd9 was filed to
// prevent.
func TestRunHelpErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := runHelpTo(&stdout, &stderr, []string{"errors"}); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr: %q", stderr.String())
	}
	out := stdout.String()
	mustContain(t, out, []string{
		// Shape.
		"ENVELOPE SHAPE",
		`"error"`,
		`"message"`,
		`"details"`,
		"always present",
		// Code categories called out by the issue's accept criteria.
		"bad_flag",
		"id_ambiguous",
		// At least one canonical spec code and one internal/per-command code
		// beyond the ones the accept line names.
		"not_in_git",
		"claim_failed",
		// Byte-counted length rule + precedent file.
		"byte",
		"internal/op/payloads.go",
		// How to emit (so a new error path can be implemented from this page).
		"cli.New",
		"cli.Emit",
	})
}

// TestRunHelpErrorsAliases lets agents reach the topic under a few
// natural spellings.
func TestRunHelpErrorsAliases(t *testing.T) {
	for _, alias := range []string{"errors", "error", "error-envelope", "ERRORS"} {
		var stdout, stderr bytes.Buffer
		if got := runHelpTo(&stdout, &stderr, []string{alias}); got != 0 {
			t.Errorf("alias %q: exit = %d, want 0; stderr=%q", alias, got, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ENVELOPE SHAPE") {
			t.Errorf("alias %q: missing errors content", alias)
		}
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
