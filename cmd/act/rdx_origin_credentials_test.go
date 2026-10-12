package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rdxSecret is a made-up credential embedded in the test remote URLs. It is
// not a real token; the tests only ask whether act echoes it.
const rdxSecret = "s3cretTOKEN"

// rdxOrigins are remote URL forms that carry userinfo. None reaches a
// network service: each is a closed local port, a host name that cannot
// resolve, or a path that does not exist.
//
// git itself strips the userinfo from its http(s) and ssh messages. It does
// not for `git://`, where the whole authority — credential included — is
// echoed as the host it could not look up; that form is the one that leaked
// before act-4a538e, and the others are guards.
func rdxOrigins(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"https":      "https://user:" + rdxSecret + "@127.0.0.1:1/x.git",
		"https-user": "https://" + rdxSecret + "@127.0.0.1:1/x.git",
		"bad-port":   "https://user:" + rdxSecret + "@127.0.0.1:notaport/x.git",
		"ssh":        "ssh://user:" + rdxSecret + "@127.0.0.1:1/x.git",
		"file":       "file://user:" + rdxSecret + "@localhost" + filepath.Join(t.TempDir(), "rdx-no-such.git"),
		"git":        "git://user:" + rdxSecret + "@127.0.0.1:1/x.git",
		"git-user":   "git://" + rdxSecret + "@127.0.0.1:1/x.git",
	}
}

// rdxSite makes an isolated tracker in which git can neither prompt for a
// credential nor wait long on a connection.
func rdxSite(t *testing.T) string {
	t.Helper()
	dir := sp6Site(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "true")
	t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes -o ConnectTimeout=3 -o StrictHostKeyChecking=no")
	return dir
}

// rdxSetOrigin points the tracker's origin at url.
func rdxSetOrigin(t *testing.T, dir, url string) {
	t.Helper()
	sp6Git(t, "--git-dir", filepath.Join(dir, ".act", ".git"), "remote", "add", "origin", url)
}

// rdxWantNoSecret runs `act <args>` and fails if either stream carries the
// credential. The exit status is not asserted: the claim is about what act
// prints, whatever the command's outcome.
func rdxWantNoSecret(t *testing.T, dir string, args ...string) (string, string) {
	t.Helper()
	out, stderr, code := runActIn(t, dir, args...)
	// The command line is logged with the credential masked so a passing
	// run's -v output is itself clean.
	shown := strings.ReplaceAll(strings.Join(args, " "), rdxSecret, "<secret>")
	t.Logf("act %s: exit %d\nstdout=%s\nstderr=%s", shown, code, strings.TrimSpace(out), strings.TrimSpace(stderr))
	if strings.Contains(out, rdxSecret) {
		t.Errorf("act %s: stdout echoes the remote URL's credential:\n%s", shown, out)
	}
	if strings.Contains(stderr, rdxSecret) {
		t.Errorf("act %s: stderr echoes the remote URL's credential:\n%s", shown, stderr)
	}
	return out, stderr
}

// TestDocClaim_Spec_RdxRemoteCredentialsNeverPrinted asserts the spec's
// "Credentials in a remote URL" paragraph: with a remote whose URL carries
// userinfo, act prints the credential on neither stdout nor stderr — not in
// `push_deferred_reason`, not in the not-pushed warning block, not in
// doctor's `fetch_failure_reason`, not in `act remote add-upstream` /
// `act remote sync` output — and does not write it to `.act/.sync-log`
// (act-4a538e).
func TestDocClaim_Spec_RdxRemoteCredentialsNeverPrinted(t *testing.T) {
	for name, url := range rdxOrigins(t) {
		t.Run(name, func(t *testing.T) {
			dir := rdxSite(t)
			closeID := createIssue(t, dir, "rdx close target")
			updateID := createIssue(t, dir, "rdx update target")
			claimID := createIssue(t, dir, "rdx claim target")
			rdxSetOrigin(t, dir, url)

			out, stderr := rdxWantNoSecret(t, dir, "close", closeID, "--reason", "rdx", "--json")
			// The path under test must have run: a deferral was reported.
			if !strings.Contains(out, `"push_deferred":true`) {
				t.Errorf("act close --json did not report push_deferred; the credentialed-origin push path was not exercised:\n%s", out)
			}
			if !strings.Contains(stderr, "push failed:") {
				t.Errorf("act close --json printed no push-failed warning; the credentialed-origin push path was not exercised:\n%s", stderr)
			}
			rdxWantNoSecret(t, dir, "update", updateID, "--priority", "1", "--json")
			rdxWantNoSecret(t, dir, "update", updateID, "--priority", "0", "--push", "--json")
			rdxWantNoSecret(t, dir, "update", claimID, "--claim", "--json")
			rdxWantNoSecret(t, dir, "create", "--json", "rdx created with a credentialed origin")
			rdxWantNoSecret(t, dir, "reopen", closeID, "--json")
			rdxWantNoSecret(t, dir, "next", "--json")

			out, _ = rdxWantNoSecret(t, dir, "doctor", "--json")
			if !strings.Contains(out, `"fetch_failure_reason"`) {
				t.Errorf("act doctor --json reported no fetch_failure_reason; the credentialed-origin fetch probe was not exercised:\n%s", out)
			}
			rdxWantNoSecret(t, dir, "doctor")

			// The same URL as origin-upstream: add-upstream's initial push
			// fails and reports the URL; sync fails soft into .sync-log.
			rdxWantNoSecret(t, dir, "remote", "add-upstream", "--json", url)
			rdxWantNoSecret(t, dir, "remote", "add-upstream", url)
			rdxWantNoSecret(t, dir, "remote", "sync", "--json")
			if log, err := os.ReadFile(filepath.Join(dir, ".act", ".sync-log")); err == nil {
				t.Logf(".act/.sync-log=%s", strings.TrimSpace(string(log)))
				if strings.Contains(string(log), rdxSecret) {
					t.Errorf(".act/.sync-log records the remote URL's credential:\n%s", log)
				}
			}
		})
	}
}
