package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// dplSecret is a made-up string standing in for a credential. It is not one.
const dplSecret = "s3cretTOKEN"

// dplCredURLBase is a remote URL prefix carrying userinfo. Nothing in these
// tests contacts it: it is either rewritten to a local path by git config
// (dplRewriteToLocal) or answered by a stand-in git (dplFakeGit).
const dplCredURLBase = "git://user:" + dplSecret + "@127.0.0.1:1/"

// dplMaskedURLBase is dplCredURLBase as act must print it.
const dplMaskedURLBase = "git://***@127.0.0.1:1/"

// dplLookupFailure is the line real git prints when it cannot resolve the
// host of a `git://` URL that carries userinfo: the whole authority, secret
// included, is echoed as the host (measured, see internal/gitops/redact.go).
const dplLookupFailure = "fatal: unable to look up user:" + dplSecret + "@127.0.0.1 (port 1) (nodename nor servname provided, or not known)"

// dplRewriteToLocal makes git read every URL under dplCredURLBase from
// localDir instead, through `url.<localDir>/.insteadOf` passed in the
// environment. The credentialed URL is what act is configured with and what
// it would print; no host is contacted and nothing is resolved.
func dplRewriteToLocal(t *testing.T, localDir string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+localDir+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", dplCredURLBase)
}

// dplFakeGit puts a stand-in `git` first on PATH. It runs the real git for
// everything except the commands that talk to a remote (pull, fetch, push,
// ls-remote), which fail with dplLookupFailure and exit 128 — what real git
// does for the credentialed `git://` URL, minus the resolver call, so the
// test depends on neither DNS nor the network.
func dplFakeGit(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
# Test stand-in for git (act-18bd72). See dplFakeGit.
skip=0
for a in "$@"; do
	if [ "$skip" = 1 ]; then skip=0; continue; fi
	case "$a" in
		-C|-c|--git-dir|--work-tree) skip=1 ;;
		-*) ;;
		pull|fetch|push|ls-remote)
			echo "` + dplLookupFailure + `" >&2
			exit 128 ;;
		*) break ;;
	esac
done
exec "` + real + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the stand-in git: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// dplWantNoSecret fails when text carries the made-up credential.
func dplWantNoSecret(t *testing.T, what, text string) {
	t.Helper()
	if strings.Contains(text, dplSecret) {
		t.Errorf("%s prints the credential from the configured URL:\n%s", what, text)
	}
}

// TestDocClaim_Spec_DplTrackerRemoteCredentialsAreMasked asserts the
// tracker-remote sentences of "Credentials in a remote URL are never
// printed": `tracker_not_checked_out` — its message, its `tracker_remote`
// and `recover` details, on a read, a write and `act init` — shows the
// tracker remote with userinfo masked and says where the real URL is, and
// the "could not check tracker remote" line is masked the same way
// (act-18bd72 item 5: all of them printed the URL raw, the recovery one
// inside a copy-paste `git clone`).
func TestDocClaim_Spec_DplTrackerRemoteCredentialsAreMasked(t *testing.T) {
	t.Run("tracker_not_checked_out", func(t *testing.T) {
		brokencoIsolateEnv(t)
		bares := t.TempDir()
		_, id := brokencoTrackerBare(t, bares)
		host := brokencoHostRepo(t)
		dplRewriteToLocal(t, bares)
		t.Setenv("ACT_TRACKER_REMOTE", dplCredURLBase+"{repo}.git")

		masked := dplMaskedURLBase + "proj.git"
		for _, args := range [][]string{
			{"show", id},
			{"list", "--json"},
			{"create", "--json", "dpl write in a broken checkout"},
			{"init", "--json"},
		} {
			what := "act " + strings.Join(args, " ")
			stdout, stderr, code := runActIn(t, host, args...)
			all := stdout + stderr
			t.Logf("%s: exit %d\n%s", what, code, all)
			if code != 3 || !strings.Contains(all, "tracker_not_checked_out") && !strings.Contains(all, "tracker exists at") {
				t.Errorf("%s: exit %d, want 3 tracker_not_checked_out — the credentialed tracker remote was not found, so nothing was exercised\n%s", what, code, all)
				continue
			}
			dplWantNoSecret(t, what, all)
			for _, want := range []string{
				"tracker exists at " + masked,
				"git clone " + masked + " ",
				"credentials in the URL are masked here; the real URL is in $ACT_TRACKER_REMOTE",
			} {
				if !strings.Contains(all, want) {
					t.Errorf("%s: output lacks %q:\n%s", what, want, all)
				}
			}
		}
	})

	// A URL with no userinfo is printed as it is, with no masking note.
	t.Run("no userinfo, no note", func(t *testing.T) {
		brokencoIsolateEnv(t)
		bares := t.TempDir()
		bare, id := brokencoTrackerBare(t, bares)
		host := brokencoHostRepo(t)
		t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
		_, stderr, code := runActIn(t, host, "show", id)
		if code != 3 || !strings.Contains(stderr, "git clone "+bare+" ") || strings.Contains(stderr, "masked") {
			t.Errorf("act show with a plain tracker remote: exit %d, want 3, the bare path in the clone command and no masking note:\n%s", code, stderr)
		}
	})

	t.Run("could not check tracker remote", func(t *testing.T) {
		brokencoIsolateEnv(t)
		host := brokencoHostRepo(t)
		dplFakeGit(t)
		t.Setenv("ACT_TRACKER_REMOTE", dplCredURLBase+"{repo}.git")

		stdout, stderr, code := runActIn(t, host, "list")
		t.Logf("act list, tracker remote unreachable: exit %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		if code != 0 || !strings.Contains(stderr, "could not check tracker remote") {
			t.Fatalf("act list with an unreachable tracker remote: exit %d, want 0 and the could-not-check line — nothing was exercised\nstderr=%s", code, stderr)
		}
		dplWantNoSecret(t, "act list (could not check tracker remote)", stdout+stderr)
		for _, want := range []string{
			"could not check tracker remote " + dplMaskedURLBase + "proj.git (from $ACT_TRACKER_REMOTE)",
			"unable to look up ***@127.0.0.1",
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("act list: stderr lacks %q:\n%s", want, stderr)
			}
		}
	})
}

// TestDocClaim_Spec_DplClaimFailedStderrTailIsMasked asserts the
// `claim_failed` sentence of the same paragraph: when a claim's rebase onto
// origin fails and git's output carries the credentials in origin's URL, the
// envelope — `message` and `details.stderr_tail`, whichever carries git's
// output — has them masked (act-18bd72 item 5: the envelope was not swept
// when act-4a538e masked the others).
//
// act-2bd35b: a fetch that cannot reach origin is no longer `claim_failed`
// (the claim stands, deferred, and `push_deferred_reason` is masked like
// every deferral — asserted here too). The envelope is reached through the
// other failure: origin is fetched, from a local bare repo the credentialed
// URL is rewritten to, and the rebase then fails. A stand-in git makes the
// rebase fail with a line that repeats origin's URL.
func TestDocClaim_Spec_DplClaimFailedStderrTailIsMasked(t *testing.T) {
	dir, bare, id := clrSiteWithOrigin(t)
	second := createIssue(t, dir, "dpl second claim target")
	// A rival checkout moves origin ahead, so the claim has something to
	// rebase onto.
	rival := clrRival(t, bare)
	createIssue(t, rival, "dpl the rival's issue")
	if !wfxOriginHasHead(t, rival, bare) {
		t.Fatalf("the rival's create was not pushed to origin")
	}
	credURL := dplCredURLBase + filepath.Base(bare)
	dplRepoint(t, dir, "origin", credURL)
	dplRewriteToLocal(t, filepath.Dir(bare))
	clrFakeGitRebaseFails(t, "fatal: could not rebase onto "+credURL)

	stdout, stderr, code := runActIn(t, dir, "update", "--json", id, "--claim")
	t.Logf("act update --claim --json, rebase fails: exit %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	if code != 1 || !strings.Contains(stdout, `"claim_failed"`) || !strings.Contains(stdout, "could not rebase onto") {
		t.Fatalf("act update --claim with a failing rebase: exit %d, want 1 claim_failed carrying git's output — nothing was exercised\nstdout=%s", code, stdout)
	}
	dplWantNoSecret(t, "act update --claim --json (claim_failed)", stdout+stderr)
	if want := "could not rebase onto " + dplMaskedURLBase; !strings.Contains(stdout, want) {
		t.Errorf("claim_failed: stdout lacks git's message with the userinfo masked (%q):\n%s", want, stdout)
	}

	// Without --json the message and then git's output are on stderr.
	stdout, stderr, code = runActIn(t, dir, "update", second, "--claim")
	if code != 1 || !strings.Contains(stderr, "could not rebase onto "+dplMaskedURLBase) {
		t.Errorf("act update --claim (human) with a failing rebase: exit %d, want 1 and git's message with the userinfo masked\nstderr=%s", code, stderr)
	}
	dplWantNoSecret(t, "act update --claim (claim_failed, human)", stdout+stderr)

	// The unreachable-origin claim is a deferral, and its reason is masked.
	third := createIssue(t, dir, "dpl third claim target", "--isolated")
	dplFakeGit(t)
	stdout, stderr, code = runActIn(t, dir, "update", "--json", third, "--claim")
	t.Logf("act update --claim --json, fetch fails: exit %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	if code != 0 || !strings.Contains(stdout, `"push_deferred":true`) || !strings.Contains(stdout, "unable to look up ***@127.0.0.1") {
		t.Errorf("act update --claim with a failing fetch: exit %d, want 0 with push_deferred and git's message masked\nstdout=%s", code, stdout)
	}
	dplWantNoSecret(t, "act update --claim --json (deferred)", stdout+stderr)
}
