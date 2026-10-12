package mcp

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// clpOriginHasHead reports whether the bare repo holds the HEAD commit of
// the tracker's nested repo — whether the last op commit really was pushed.
func clpOriginHasHead(t *testing.T, root, bare string) bool {
	t.Helper()
	head := gitOutput(t, filepath.Join(root, ".act"), "rev-parse", "HEAD")
	if head == "" {
		t.Fatalf("rev-parse HEAD of the nested repo returned nothing")
	}
	return exec.Command("git", "--git-dir", bare, "cat-file", "-e", head+"^{commit}").Run() == nil
}

// clpRepoWithBareOrigin is makeRealRepo plus an empty local bare repo set as
// `origin` of the nested tracker repo, and one seeded issue (whose create
// commit puts the branch on origin). Local only: no network.
func clpRepoWithBareOrigin(t *testing.T, title string) (root, bare, id string) {
	t.Helper()
	root = makeRealRepo(t)
	bare = filepath.Join(t.TempDir(), "clp-origin.git")
	mustGitMCP(t, root, "init", "-q", "--bare", bare)
	mustGitMCP(t, filepath.Join(root, ".act"), "remote", "add", "origin", bare)
	id = seedIssue(t, root, title)
	if !clpOriginHasHead(t, root, bare) {
		t.Fatalf("setup: origin does not hold the create commit")
	}
	return root, bare, id
}

// TestDocClaim_ClpMCPNextPublishesItsClaim: act_next's claim reaches origin without
// the caller asking for a push, and isolated:true holds it back — the same
// behaviour as `act update --claim` and `act next`, whose claim path it
// shares (act-2cfc30).
func TestDocClaim_ClpMCPNextPublishesItsClaim(t *testing.T) {
	t.Run("origin", func(t *testing.T) {
		root, bare, id := clpRepoWithBareOrigin(t, "clp mcp next target")
		srv := NewServer(root, false, nil, nil)
		before := gitOutput(t, filepath.Join(root, ".act"), "rev-parse", "HEAD")
		out, isErr := srv.callNext(json.RawMessage(`{}`))
		if isErr {
			t.Fatalf("callNext: %+v", out)
		}
		if after := gitOutput(t, filepath.Join(root, ".act"), "rev-parse", "HEAD"); after == before {
			t.Fatalf("act_next made no commit; expected it to claim %s: %+v", id, out)
		}
		if !clpOriginHasHead(t, root, bare) {
			t.Errorf("after act_next, origin does not hold the claim commit")
		}
	})

	t.Run("isolated", func(t *testing.T) {
		root, bare, id := clpRepoWithBareOrigin(t, "clp mcp isolated next target")
		srv := NewServer(root, false, nil, nil)
		before := gitOutput(t, filepath.Join(root, ".act"), "rev-parse", "HEAD")
		out, isErr := srv.callNext(json.RawMessage(`{"isolated":true}`))
		if isErr {
			t.Fatalf("callNext isolated: %+v", out)
		}
		if after := gitOutput(t, filepath.Join(root, ".act"), "rev-parse", "HEAD"); after == before {
			t.Fatalf("act_next isolated made no commit; expected it to claim %s: %+v", id, out)
		}
		if clpOriginHasHead(t, root, bare) {
			t.Errorf("act_next isolated:true pushed the claim commit to origin")
		}
	})
}
