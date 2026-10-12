package main

import (
	"encoding/json"
	"os"
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

// TestClrClaimInAutoPublishedTrackerSeesARivalClaim (act-2bd35b item 4): a
// tracker that only ever auto-published has no upstream branch, because the
// auto-publish push passes no `-u`. A claim there must still rebase onto
// origin before deciding who won: a rival checkout that claimed first and
// pushed holds the issue, and this checkout's claim is lost.
func TestClrClaimInAutoPublishedTrackerSeesARivalClaim(t *testing.T) {
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
