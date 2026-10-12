package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// pdfDeferred is the part of a write command's `--json` result these tests
// read: the two deferral keys, and create's `pushed`.
type pdfDeferred struct {
	Error              string `json:"error"`
	Pushed             *bool  `json:"pushed"`
	Claimed            bool   `json:"claimed"`
	PushDeferred       bool   `json:"push_deferred"`
	PushDeferredReason string `json:"push_deferred_reason"`
}

// pdfAddMissingOrigin points the tracker's `origin` at a local path that does
// not exist, so every push fails. Local only: no network.
func pdfAddMissingOrigin(t *testing.T, dir string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "pdf-no-such-origin.git")
	sp6Git(t, "--git-dir", filepath.Join(dir, ".act", ".git"), "remote", "add", "origin", missing)
}

// pdfRun runs `act <args>`, wants exit 0, and returns the parsed result with
// the raw stdout and stderr.
func pdfRun(t *testing.T, dir string, args ...string) (pdfDeferred, string, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", args...)
	var res pdfDeferred
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act %v: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res, out, stderr
}

// pdfWantDeferred asserts one command's result with an unreachable origin:
// exit 0 (pdfRun), no error, both deferral keys, the stderr warning block and
// a queued commit.
func pdfWantDeferred(t *testing.T, dir string, args ...string) pdfDeferred {
	t.Helper()
	res, out, stderr := pdfRun(t, dir, args...)
	t.Logf("act %s, origin unreachable: stdout=%s", strings.Join(args, " "), strings.TrimSpace(out))
	if res.Error != "" || !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("act %s with an unreachable origin = %s; want push_deferred:true and a push_deferred_reason",
			strings.Join(args, " "), strings.TrimSpace(out))
	}
	if !strings.Contains(stderr, "COMMITTED LOCALLY but NOT PUSHED") {
		t.Errorf("act %s with an unreachable origin: stderr lacks the not-pushed warning block:\n%s", strings.Join(args, " "), stderr)
	}
	if q := pnrPendingPushes(t, dir); q == "" {
		t.Errorf("act %s with an unreachable origin queued nothing in .act/.pending-pushes", strings.Join(args, " "))
	}
	return res
}

// pdfWantNotDeferred asserts a result carries neither deferral key.
func pdfWantNotDeferred(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, out, _ := pdfRun(t, dir, args...); strings.Contains(out, "push_deferred") {
		t.Errorf("act %s with no deferred push carries push_deferred: %s", strings.Join(args, " "), strings.TrimSpace(out))
	}
}

// TestDocClaim_Spec_PdfWritesReportPushDeferred asserts the sentence of the
// "Publish failure is a deferral" paragraph that names the commands setting
// `push_deferred`: with an `origin` that cannot be reached, `act create`,
// `act reopen`, `act dep add` (both forms) and `act delete` `--json` exit 0
// carrying `push_deferred: true` and `push_deferred_reason` (act-087b2b: they
// printed the stderr warning and left the JSON result silent). create keeps
// `pushed: false` beside the two keys.
func TestDocClaim_Spec_PdfWritesReportPushDeferred(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		dir := sp6Site(t)
		pdfAddMissingOrigin(t, dir)
		res := pdfWantDeferred(t, dir, "create", "--json", "pdf create target")
		if res.Pushed == nil || *res.Pushed {
			t.Errorf("act create --json with an unreachable origin: pushed = %v, want the key present and false", res.Pushed)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "pdf reopen target")
		pass5Run(t, dir, 0, "", "close", id, "--reason", "pdf")
		pdfAddMissingOrigin(t, dir)
		pdfWantDeferred(t, dir, "reopen", "--json", id)
	})

	t.Run("dep add", func(t *testing.T) {
		dir := sp6Site(t)
		child := createIssue(t, dir, "pdf dep child")
		parent := createIssue(t, dir, "pdf dep parent")
		pdfAddMissingOrigin(t, dir)
		pdfWantDeferred(t, dir, "dep", "add", "--json", child, parent)
	})

	t.Run("dep add --external", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "pdf external dep subject")
		pdfAddMissingOrigin(t, dir)
		pdfWantDeferred(t, dir, "dep", "add", "--json", id, "--external", "linear:PDF-1")
	})

	t.Run("delete", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "pdf delete target")
		pdfAddMissingOrigin(t, dir)
		pdfWantDeferred(t, dir, "delete", "--json", "--reason", "pdf", id)
	})

	// A push that reaches origin, and a tracker with no origin, report no
	// deferral: the keys are absent.
	t.Run("absent when the push is not deferred", func(t *testing.T) {
		for _, withOrigin := range []bool{true, false} {
			dir := sp6Site(t)
			if withOrigin {
				wfxAddBareOrigin(t, dir)
			}
			pdfWantNotDeferred(t, dir, "create", "--json", "pdf quiet create")
			a := createIssue(t, dir, "pdf quiet a")
			b := createIssue(t, dir, "pdf quiet b")
			pdfWantNotDeferred(t, dir, "dep", "add", "--json", a, b)
			pdfWantNotDeferred(t, dir, "dep", "add", "--json", a, "--external", "linear:PDF-2")
			pass5Run(t, dir, 0, "", "close", b, "--reason", "pdf")
			pdfWantNotDeferred(t, dir, "reopen", "--json", b)
			pdfWantNotDeferred(t, dir, "delete", "--json", "--reason", "pdf", b)
		}
	})
}

// TestDocClaim_Spec_PdfNextCarriesTheClaimsPushDeferred asserts the `act
// next` sentence "The claiming form also carries the claim's `warnings`,
// `push_deferred` and `push_deferred_reason`" for the deferral: `act next
// --json` whose claim push does not reach `origin` exits 0, claims, and
// carries both keys (act-087b2b: confirmed at f904fc6, no test held it).
func TestDocClaim_Spec_PdfNextCarriesTheClaimsPushDeferred(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "pdf next target")
	pdfAddMissingOrigin(t, dir)

	res, out, _ := pdfRun(t, dir, "next", "--json")
	t.Logf("act next --json, origin unreachable: stdout=%s", strings.TrimSpace(out))
	if !res.Claimed || !strings.Contains(out, id) || !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("act next --json with an unreachable origin = %s; want claimed:true for %s, push_deferred:true and a push_deferred_reason",
			strings.TrimSpace(out), id)
	}
	if q := pnrPendingPushes(t, dir); q == "" {
		t.Errorf("act next with an unreachable origin queued nothing in .act/.pending-pushes")
	}
}
