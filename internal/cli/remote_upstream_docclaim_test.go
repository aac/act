package cli

// Doc-claim regression tests for `act remote add-upstream` (Phase 2
// ticket 1b). Each test pins a user-visible behavior claim made in a
// doc surface at the boundary an agent would actually hit:
//
//   - `act help` stdout for help-text claims (cmd/act/help.go).
//   - Subprocess stderr for the public-refusal literal line.
//
// Internal behaviour is covered by remote_upstream_test.go; this file
// is for the drift-vs-doc shape enforced by docs_sweep_test.go.

import (
	"strings"
	"testing"
)

// TestDocClaim_RemoteAddUpstream_HelpListed asserts that `act help`'s
// rendered text contains `add-upstream`. The sweep enforces the
// literal in cmd/act/help.go; this drives the actual binary so a
// refactor that re-shapes the help still surfaces the verb.
func TestDocClaim_RemoteAddUpstream_HelpListed(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "add-upstream") {
		t.Errorf("`act help` output missing `add-upstream` listing")
	}
}

// TestDocClaim_RemoteAddUpstream_ForcePublicFlag asserts that the
// `--force-public` flag is documented in `act help`. The sweep
// enforces the literal in cmd/act/help.go; this drives the actual
// binary.
func TestDocClaim_RemoteAddUpstream_ForcePublicFlag(t *testing.T) {
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "--force-public") {
		t.Errorf("`act help` output missing `--force-public` flag documentation")
	}
}

// TestDocClaim_RemoteAddUpstream_PublicRefusalStderr pins the spec
// claim that public-URL refusal produces the literal stderr line
// `refusing public upstream; pass --force-public to override`. Drives
// at the subprocess boundary (real act binary, real stderr) so the
// stderr-literal claim cannot drift away from the user-visible
// surface.
func TestDocClaim_RemoteAddUpstream_PublicRefusalStderr(t *testing.T) {
	host := addUpstreamFixture(t)
	stdout, stderr, exit := runAct(t, host, "remote", "add-upstream",
		"https://github.com/aac/public-thing")
	if exit != 2 {
		t.Errorf("public-URL refusal: exit=%d, want 2\nstdout:%s\nstderr:%s",
			exit, stdout, stderr)
	}
	const want = "refusing public upstream; pass --force-public to override"
	if !strings.Contains(stderr, want) {
		t.Errorf("public-URL refusal: stderr missing literal hint\nstderr:\n%s", stderr)
	}
}
