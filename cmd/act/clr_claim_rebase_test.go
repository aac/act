package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// clrClaim is the part of `act update --claim --json` these tests read.
type clrClaim struct {
	OK                 bool           `json:"ok"`
	Claimed            bool           `json:"claimed"`
	Winner             string         `json:"winner"`
	Error              string         `json:"error"`
	Message            string         `json:"message"`
	Reason             string         `json:"reason"`
	PushDeferred       bool           `json:"push_deferred"`
	PushDeferredReason string         `json:"push_deferred_reason"`
	Details            map[string]any `json:"details"`
}

// clrRunClaim runs `act update --json <id> --claim <extra>` in dir and
// returns the parsed result, the raw streams and the exit code.
func clrRunClaim(t *testing.T, dir, id string, extra ...string) (clrClaim, string, string, int) {
	t.Helper()
	args := append([]string{"update", "--json", id, "--claim"}, extra...)
	stdout, stderr, code := runActIn(t, dir, args...)
	t.Logf("act %s (in %s): exit %d\nstdout=%s\nstderr=%s", strings.Join(args, " "), filepath.Base(dir), code, strings.TrimSpace(stdout), strings.TrimSpace(stderr))
	var res clrClaim
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("act %v: stdout is not a JSON object (%v)\nstdout=%s", args, err, stdout)
	}
	return res, stdout, stderr, code
}

// clrActDir is the nested tracker repo's working tree.
func clrActDir(dir string) string { return filepath.Join(dir, ".act") }

// clrHasUpstream reports whether the nested repo's branch has an upstream
// configured (`git push -u` or `git clone` sets one; a plain push does not).
func clrHasUpstream(dir string) bool {
	_, err := sp7GitOut("-C", clrActDir(dir), "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// clrSiteWithOrigin makes a tracker whose `origin` is a local bare repo and
// that has only ever auto-published: one issue created, its commit on
// origin, no upstream branch configured. Returns the site, the bare path and
// the issue id.
func clrSiteWithOrigin(t *testing.T) (string, string, string) {
	t.Helper()
	dir := sp6Site(t)
	bare := wfxAddBareOrigin(t, dir)
	id := createIssue(t, dir, "clr claim target")
	if !wfxOriginHasHead(t, dir, bare) {
		t.Fatalf("the create was not auto-published to the bare origin")
	}
	return dir, bare, id
}

// clrRival makes a second checkout of the tracker at bare — its own host
// repo, its own node id — through `act state import --from-remote`.
func clrRival(t *testing.T, bare string) string {
	t.Helper()
	rival := filepath.Join(t.TempDir(), "clr-rival")
	sp6Git(t, "init", "-q", "-b", "main", rival)
	sp6Git(t, "-C", rival, "config", "user.email", "clr-rival@example.com")
	sp6Git(t, "-C", rival, "config", "user.name", "Clr Rival")
	sp6Git(t, "-C", rival, "config", "commit.gpgsign", "false")
	stdout, stderr, code := runActIn(t, rival, "state", "import", "--from-remote", bare, rival, "--json")
	if code != 0 {
		t.Fatalf("act state import --from-remote: exit %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	// config.json is tracked, so the import carried the first checkout's
	// node_id along, and a claim's assignee is the node_id. Give the rival
	// its own, as a checkout on another machine would be told apart, and
	// tell git to overlook the edit so the rival's tree stays clean.
	cfgPath := filepath.Join(clrActDir(rival), "config.json")
	var cfg map[string]any
	if err := json.Unmarshal([]byte(clrReadFile(t, cfgPath)), &cfg); err != nil {
		t.Fatalf("parse the rival's config.json: %v", err)
	}
	cfg["node_id"] = "c1a0beef"
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal the rival's config.json: %v", err)
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		t.Fatalf("write the rival's config.json: %v", err)
	}
	sp6Git(t, "-C", clrActDir(rival), "update-index", "--skip-worktree", "config.json")
	return rival
}

// clrMissingOrigin is a local path with no repository at it: an `origin`
// that cannot be reached, with no network and no DNS involved.
func clrMissingOrigin(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "clr-no-such-origin.git")
}

// TestDocClaim_Spec_ClrClaimRebasesWithoutAnUpstream asserts the "The rebase
// names `origin` and the tracker's branch" paragraph of the atomic claim
// protocol (act-2bd35b item 4). A tracker that only ever auto-published has
// no upstream branch, because the publish push passes no `-u`; the claim's
// rebase used to be a bare `git pull --rebase` gated on an upstream, so it
// never ran there, and an issue a rival checkout had already claimed and
// pushed was reported claimed:true in both checkouts.
func TestDocClaim_Spec_ClrClaimRebasesWithoutAnUpstream(t *testing.T) {
	dir, bare, id := clrSiteWithOrigin(t)
	if clrHasUpstream(dir) {
		t.Fatalf("premise: the auto-published tracker has an upstream branch configured")
	}
	rival := clrRival(t, bare)
	won, _, _, code := clrRunClaim(t, rival, id)
	if code != 0 || !won.Claimed {
		t.Fatalf("the rival's claim: exit %d claimed=%v, want a win", code, won.Claimed)
	}
	if !wfxOriginHasHead(t, rival, bare) {
		t.Fatalf("the rival's claim was not pushed to origin")
	}

	lost, stdout, _, code := clrRunClaim(t, dir, id)
	if code != 5 || lost.Claimed || lost.Error != "claim_lost" {
		t.Errorf("claim of an issue a rival already claimed and pushed: exit %d claimed=%v error=%q; want exit 5, claimed false, claim_lost\nstdout=%s",
			code, lost.Claimed, lost.Error, stdout)
	}
	if lost.Winner != won.Winner {
		t.Errorf("winner = %q, want the rival %q", lost.Winner, won.Winner)
	}
	if clrHasUpstream(dir) {
		t.Errorf("the claim configured an upstream branch; the rebase is meant to name origin and the branch itself")
	}

	// origin without the branch yet: the issue was created before origin
	// was added, so nothing was ever published. The step is a no-op and
	// the won claim is published like any other write.
	fresh := sp6Site(t)
	freshID := createIssue(t, fresh, "clr claim before any publish")
	freshBare := wfxAddBareOrigin(t, fresh)
	first, stdout, stderr, code := clrRunClaim(t, fresh, freshID)
	if code != 0 || !first.Claimed || first.PushDeferred || strings.Contains(stderr, "WARNING") {
		t.Errorf("claim with an origin that has no branch yet: exit %d claimed=%v push_deferred=%v; want a plain win\nstdout=%s\nstderr=%s",
			code, first.Claimed, first.PushDeferred, stdout, stderr)
	}
	if !wfxOriginHasHead(t, fresh, freshBare) {
		t.Errorf("the won claim was not published to the origin that had no branch yet")
	}
}

// clrReadFile returns a file's content, "" when it does not exist.
func clrReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// clrBranch is the nested repo's current branch.
func clrBranch(t *testing.T, dir string) string {
	t.Helper()
	b, err := sp7GitOut("-C", clrActDir(dir), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || b == "" {
		t.Fatalf("the nested repo's branch: %q, %v", b, err)
	}
	return b
}

// clrStatus is the issue's status as `act show --json` reports it, read
// without touching origin.
func clrStatus(t *testing.T, dir, id string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json", "--no-fetch")
	if code != 0 {
		t.Fatalf("act show %s --json --no-fetch: exit %d\nstdout=%s\nstderr=%s", id, code, out, stderr)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse act show: %v\n%s", err, out)
	}
	return res.Status
}

// TestDocClaim_Spec_ClrClaimWithUnreachableOriginIsUnconfirmed asserts the
// "A claim that cannot reach `origin` is unconfirmed, not failed" paragraph
// of the atomic claim protocol (act-2bd35b item 1). The claim op is
// committed before the rebase step runs, so a fetch that cannot reach origin
// used to answer exit 1 `claim_failed` for a write that had landed — and a
// second claim then answered exit 0, already held, with nothing said about
// origin. It now exits 0 with `claimed: true` and the deferral reported.
//
// Run both with an upstream branch configured (a `git clone`d tracker, the
// only shape whose claim reached origin before act-2bd35b) and without one
// (a tracker that only auto-published).
func TestDocClaim_Spec_ClrClaimWithUnreachableOriginIsUnconfirmed(t *testing.T) {
	for _, withUpstream := range []bool{true, false} {
		name := "no upstream branch"
		if withUpstream {
			name = "upstream branch configured"
		}
		t.Run(name, func(t *testing.T) {
			dir, _, id := clrSiteWithOrigin(t)
			if withUpstream {
				sp6Git(t, "-C", clrActDir(dir), "push", "-q", "-u", "origin", "HEAD")
			}
			dplRepoint(t, dir, "origin", clrMissingOrigin(t))

			res, stdout, stderr, code := clrRunClaim(t, dir, id)
			if code != 0 || !res.Claimed || res.Error != "" {
				t.Fatalf("claim with an unreachable origin: exit %d claimed=%v error=%q; want exit 0, claimed true\nstdout=%s",
					code, res.Claimed, res.Error, stdout)
			}
			if !res.PushDeferred || res.PushDeferredReason == "" {
				t.Errorf("claim with an unreachable origin: push_deferred=%v push_deferred_reason=%q; want true and a reason\nstdout=%s",
					res.PushDeferred, res.PushDeferredReason, stdout)
			}
			if n := strings.Count(stderr, "COMMITTED LOCALLY but NOT PUSHED"); n != 1 {
				t.Errorf("claim with an unreachable origin printed %d not-pushed warning blocks, want one:\n%s", n, stderr)
			}
			if pnrPendingPushes(t, dir) == "" {
				t.Errorf("claim with an unreachable origin queued nothing in .act/.pending-pushes")
			}
			if got := clrStatus(t, dir, id); got != "in_progress" {
				t.Errorf("after the unconfirmed claim the issue's status is %q, want in_progress", got)
			}
		})
	}
}

// TestDocClaim_Spec_ClrClaimLostToARivalIsNeverAWin asserts the sentences of
// the same paragraph that keep a lost race lost (act-2bd35b item 1): a claim
// is the fleet's mutual-exclusion signal, so the deferral is only for a
// claim that won among every op this checkout can see.
func TestDocClaim_Spec_ClrClaimLostToARivalIsNeverAWin(t *testing.T) {
	// The rival's claim is already in this checkout, and origin is then
	// unreachable: the fetch fails, and the claim is still lost.
	t.Run("origin unreachable, the rival's claim already fetched", func(t *testing.T) {
		dir, bare, id := clrSiteWithOrigin(t)
		sp6Git(t, "-C", clrActDir(dir), "push", "-q", "-u", "origin", "HEAD")
		rival := clrRival(t, bare)
		won, _, _, code := clrRunClaim(t, rival, id)
		if code != 0 || !won.Claimed {
			t.Fatalf("the rival's claim: exit %d claimed=%v, want a win", code, won.Claimed)
		}
		sp6Git(t, "-C", clrActDir(dir), "pull", "-q", "--rebase", "origin", clrBranch(t, dir))
		dplRepoint(t, dir, "origin", clrMissingOrigin(t))

		lost, stdout, _, code := clrRunClaim(t, dir, id)
		if code != 5 || lost.Claimed || lost.Error != "claim_lost" || lost.Winner != won.Winner {
			t.Errorf("claim against a rival's fetched claim, origin unreachable: exit %d claimed=%v error=%q winner=%q; want exit 5, claimed false, claim_lost, winner %q\nstdout=%s",
				code, lost.Claimed, lost.Error, lost.Winner, won.Winner, stdout)
		}
		if lost.PushDeferred {
			t.Errorf("a lost claim reports push_deferred: %s", stdout)
		}
	})

	// origin is reached and holds the rival's claim, but its history
	// cannot be rebased under ours (both sides committed the same new
	// file). act cannot see who holds the issue, so it reports no win:
	// exit 1 claim_failed, the rebase aborted.
	t.Run("origin fetched but not rebased", func(t *testing.T) {
		dir, bare, id := clrSiteWithOrigin(t)
		sp6Git(t, "-C", clrActDir(dir), "push", "-q", "-u", "origin", "HEAD")
		rival := clrRival(t, bare)
		won, _, _, code := clrRunClaim(t, rival, id)
		if code != 0 || !won.Claimed {
			t.Fatalf("the rival's claim: exit %d claimed=%v, want a win", code, won.Claimed)
		}
		for _, side := range []struct{ dir, body string }{{rival, "rival\n"}, {dir, "this checkout\n"}} {
			if err := os.WriteFile(filepath.Join(clrActDir(side.dir), "clr-conflict.txt"), []byte(side.body), 0o644); err != nil {
				t.Fatalf("write the conflicting file: %v", err)
			}
			sp6Git(t, "-C", clrActDir(side.dir), "add", "clr-conflict.txt")
			sp6Git(t, "-C", clrActDir(side.dir), "commit", "-q", "--no-verify", "-m", "clr: conflicting file")
		}
		sp6Git(t, "-C", clrActDir(rival), "push", "-q", "origin", "HEAD")

		res, stdout, _, code := clrRunClaim(t, dir, id)
		if code != 1 || res.Claimed || res.Error != "claim_failed" {
			t.Errorf("claim whose rebase onto origin conflicts: exit %d claimed=%v error=%q; want exit 1, claim_failed and no win\nstdout=%s",
				code, res.Claimed, res.Error, stdout)
		}
		for _, name := range []string{"rebase-merge", "rebase-apply"} {
			if _, err := os.Stat(filepath.Join(dplNestedGitDir(dir), name)); err == nil {
				t.Errorf("the failed claim left a rebase in progress (.git/%s exists)", name)
			}
		}
		// Observation only (see the report's follow-ups): what a second
		// claim answers once the first has failed this way.
		again, _, _, code := clrRunClaim(t, dir, id)
		t.Logf("second claim after the conflicted one: exit %d claimed=%v error=%q winner=%q", code, again.Claimed, again.Error, again.Winner)
	})
}

// clrFakeGitRebaseFails puts a stand-in `git` first on PATH. It runs the
// real git for everything except `rebase` (other than `rebase --abort`),
// which prints line on stderr and exits 128: a rebase that fails after a
// fetch that worked, with output of the test's choosing.
func clrFakeGitRebaseFails(t *testing.T, line string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
# Test stand-in for git (act-2bd35b). See clrFakeGitRebaseFails.
skip=0
seen=0
for a in "$@"; do
	if [ "$skip" = 1 ]; then skip=0; continue; fi
	if [ "$seen" = 1 ]; then
		case "$a" in
			--abort) break ;;
			*) echo "` + line + `" >&2; exit 128 ;;
		esac
	fi
	case "$a" in
		-C|-c) skip=1 ;;
		-*) ;;
		rebase) seen=1 ;;
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
