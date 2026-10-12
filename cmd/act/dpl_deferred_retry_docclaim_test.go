package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dplNestedGitDir is the nested tracker repo's git dir.
func dplNestedGitDir(dir string) string {
	return filepath.Join(dir, ".act", ".git")
}

// dplRepoint sets the URL of a remote of the nested repo. Local only.
func dplRepoint(t *testing.T, dir, remote, url string) {
	t.Helper()
	sp6Git(t, "--git-dir", dplNestedGitDir(dir), "remote", "set-url", remote, url)
}

// dplHelpText returns the whole of help.go, the source of every `act help`
// topic, so a text assertion covers each topic at once.
func dplHelpText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("help.go")
	if err != nil {
		t.Fatalf("read help.go: %v", err)
	}
	return string(b)
}

// TestDocClaim_Spec_DplDeferredPushRetriedByTheNextWrite asserts the sentence
// of the "Publish failure is a deferral" paragraph that says what publishes a
// deferred commit: the next write without `--offline` or `--isolated`, and
// NOT `act remote sync`, which pushes to `origin-upstream` and never to
// `origin` (act-18bd72: the WARNING block, the spec and `act help` all named
// `act remote sync` as a retry).
//
// Shape: a local bare origin, made unreachable by repointing its URL at a
// missing path, a write deferred, the URL restored. No network.
func TestDocClaim_Spec_DplDeferredPushRetriedByTheNextWrite(t *testing.T) {
	dir := sp6Site(t)
	bare := wfxAddBareOrigin(t, dir)
	dplRepoint(t, dir, "origin", filepath.Join(t.TempDir(), "dpl-no-such-origin.git"))

	_, _, stderr := pdfRun(t, dir, "create", "--json", "dpl deferred create")
	if !strings.Contains(stderr, "COMMITTED LOCALLY but NOT PUSHED") {
		t.Fatalf("create with an unreachable origin printed no not-pushed warning block:\n%s", stderr)
	}
	if pnrPendingPushes(t, dir) == "" {
		t.Fatalf("create with an unreachable origin queued nothing in .act/.pending-pushes")
	}
	if strings.Contains(stderr, "remote sync") {
		t.Errorf("the not-pushed warning block names 'act remote sync', which does not push to origin:\n%s", stderr)
	}
	if want := "the next act write without --offline or --isolated retries the push"; !strings.Contains(stderr, want) {
		t.Errorf("the not-pushed warning block does not say %q:\n%s", want, stderr)
	}

	// Origin is reachable again.
	dplRepoint(t, dir, "origin", bare)

	// `act remote sync` with no origin-upstream is a usage error and
	// publishes nothing.
	out, serr, code := runActIn(t, dir, "remote", "sync", "--json")
	t.Logf("act remote sync --json, no origin-upstream: exit %d stdout=%s stderr=%s", code, strings.TrimSpace(out), strings.TrimSpace(serr))
	if code != 2 || !strings.Contains(out, "upstream_not_configured") {
		t.Errorf("act remote sync with no origin-upstream: exit %d stdout=%s; want exit 2 upstream_not_configured", code, out)
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Errorf("act remote sync (no origin-upstream) published the deferred commit to origin; the doc says it does not")
	}

	// With an origin-upstream it pushes there, and still not to origin.
	upstream := filepath.Join(t.TempDir(), "dpl-upstream.git")
	sp6Git(t, "init", "-q", "--bare", upstream)
	sp6Git(t, "--git-dir", dplNestedGitDir(dir), "remote", "add", "origin-upstream", upstream)
	out, serr, code = runActIn(t, dir, "remote", "sync", "--json")
	t.Logf("act remote sync --json, origin-upstream set: exit %d stdout=%s stderr=%s", code, strings.TrimSpace(out), strings.TrimSpace(serr))
	if code != 0 || !wfxOriginHasHead(t, dir, upstream) {
		t.Errorf("act remote sync with an origin-upstream: exit %d, upstream has HEAD = %v; want exit 0 and the push landed on origin-upstream\nstdout=%s",
			code, wfxOriginHasHead(t, dir, upstream), out)
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Errorf("act remote sync published the deferred commit to origin; the doc says it pushes only to origin-upstream")
	}
	if pnrPendingPushes(t, dir) == "" {
		t.Errorf("act remote sync emptied .act/.pending-pushes without pushing to origin")
	}

	// The next ordinary write does publish it, and clears the queue.
	pdfWantNotDeferred(t, dir, "create", "--json", "dpl write after origin came back")
	if !wfxOriginHasHead(t, dir, bare) {
		t.Errorf("the next write did not publish to origin")
	}
	if q := pnrPendingPushes(t, dir); q != "" {
		t.Errorf("the next write left .act/.pending-pushes non-empty:\n%s", q)
	}

	// The same statement in `act help`.
	help := dplHelpText(t)
	if want := "The next write without --offline or --isolated\n  publishes the backlog; 'act remote sync' does not"; !strings.Contains(help, want) {
		t.Errorf("act help does not say %q", want)
	}
	if strings.Contains(help, "(or 'act remote sync')") {
		t.Errorf("act help still offers 'act remote sync' as a retry of the deferred push to origin")
	}
}
