package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// collisionFixture builds the exact state act-650378 was reported from:
// a local repo whose branch has diverged from origin, where origin
// tracks an op file at a path that also exists on disk locally as an
// UNTRACKED file. `git rebase origin/main` refuses to detach HEAD in
// that state.
//
// Returns the GitOps handle for the local repo and the colliding
// repo-relative path.
func collisionFixture(t *testing.T, localContent, remoteContent string) (*GitOps, string) {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "bare.git")
	runGit(t, base, "init", "-q", "--bare", bare)

	local := initRepo(t)
	runGit(t, local, "remote", "add", "origin", bare)
	runGit(t, local, "push", "-q", "-u", "origin", "main")

	// A peer clone commits the op file and pushes it, so origin tracks
	// the path.
	peer := filepath.Join(base, "peer")
	runGit(t, base, "clone", "-q", bare, peer)
	runGit(t, peer, "config", "user.email", "peer@example.com")
	runGit(t, peer, "config", "user.name", "peer")
	runGit(t, peer, "config", "commit.gpgsign", "false")
	opRel := "ops/act-aaaaaa/2026-07/2026-07-31T06-17-02.114Z-close.json"
	writeFile(t, filepath.Join(peer, opRel), remoteContent)
	runGit(t, peer, "add", "ops")
	runGit(t, peer, "commit", "-q", "--no-verify", "-m", "peer op")
	runGit(t, peer, "push", "-q", "origin", "main")

	// The local repo diverges (its own commit) AND has the same op path
	// on disk, untracked.
	writeFile(t, filepath.Join(local, "local-work.txt"), "local")
	runGit(t, local, "add", "local-work.txt")
	runGit(t, local, "commit", "-q", "--no-verify", "-m", "local op")
	writeFile(t, filepath.Join(local, opRel), localContent)

	return NewGitOps(local), opRel
}

// TestCollision_ReproducesGitRefusal is the positive control for the
// whole fix: it asserts the raw git behavior act-650378 reported, so the
// recovery tests below are known to be exercising a real refusal rather
// than a fixture that never collides. If git ever stops refusing here,
// this test fails and the recovery path becomes dead code worth
// deleting — which is exactly what we'd want to be told.
func TestCollision_ReproducesGitRefusal(t *testing.T) {
	g, opRel := collisionFixture(t, `{"local":1}`, `{"remote":1}`)

	if _, err := g.runCombined("fetch", "origin", "main"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	out, err := g.runCombined("rebase", "origin/main")
	if err == nil {
		t.Fatalf("git rebase unexpectedly succeeded; the fixture no longer reproduces the collision\n%s", out)
	}
	if !strings.Contains(out, collisionHeader) {
		t.Errorf("rebase output missing the untracked-collision refusal:\n%s", out)
	}
	if !strings.Contains(out, "could not detach HEAD") {
		t.Errorf("rebase output missing 'could not detach HEAD' (the symptom act-650378 reported):\n%s", out)
	}
	if !strings.Contains(out, opRel) {
		t.Errorf("rebase output does not name the colliding op file %q:\n%s", opRel, out)
	}
}

// TestFetchAndRebase_RecoversFromOpFileCollision_Identical: the common
// real case. The untracked local op file is byte-identical to origin's,
// so nothing is at stake — act removes it and the rebase proceeds.
func TestFetchAndRebase_RecoversFromOpFileCollision_Identical(t *testing.T) {
	same := `{"op":"close","id":"act-aaaaaa"}`
	g, opRel := collisionFixture(t, same, same)

	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase: %v", err)
	}

	// The op file is now present and TRACKED (it came from origin).
	if !g.isTracked(opRel) {
		t.Errorf("%s is not tracked after the rebase", opRel)
	}
	body, err := os.ReadFile(filepath.Join(g.RepoRoot, opRel))
	if err != nil {
		t.Fatalf("read %s: %v", opRel, err)
	}
	if string(body) != same {
		t.Errorf("op file content = %q, want %q", body, same)
	}
	// Nothing was quarantined — there was nothing to preserve.
	if entries, err := os.ReadDir(filepath.Join(g.RepoRoot, collisionQuarantineDir)); err == nil && len(entries) > 0 {
		t.Errorf("quarantined %d entries for an identical-content collision; want 0", len(entries))
	}
	// The local commit survived the rebase.
	log, err := g.runCombined("log", "--oneline")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(log, "local op") || !strings.Contains(log, "peer op") {
		t.Errorf("rebase lost history; log:\n%s", log)
	}
}

// TestFetchAndRebase_RecoversFromOpFileCollision_Differing: the local
// file differs from origin's. act must NOT delete it — the bytes may be
// a concurrent writer's op that was never staged. It is moved into
// .collisions/<stamp>/ and the rebase proceeds.
func TestFetchAndRebase_RecoversFromOpFileCollision_Differing(t *testing.T) {
	g, opRel := collisionFixture(t, `{"local":"unstaged"}`, `{"remote":"published"}`)

	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase: %v", err)
	}

	// Origin's version is what's in the tree now.
	body, err := os.ReadFile(filepath.Join(g.RepoRoot, opRel))
	if err != nil {
		t.Fatalf("read %s: %v", opRel, err)
	}
	if string(body) != `{"remote":"published"}` {
		t.Errorf("tree has %q, want origin's version", body)
	}

	// The local bytes must still exist somewhere under .collisions/.
	var found string
	root := filepath.Join(g.RepoRoot, collisionQuarantineDir)
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && string(b) == `{"local":"unstaged"}` {
			found = p
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if found == "" {
		t.Fatalf("the differing local op file was not preserved under %s/", collisionQuarantineDir)
	}
	if !strings.HasSuffix(filepath.ToSlash(found), opRel) {
		t.Errorf("quarantined at %q; want the original repo-relative path preserved under the stamp dir", found)
	}
}

// TestResolveOpFileCollisions_RefusesOutsideOpsDir fences the recovery:
// a colliding path act does not own is never touched, and the caller
// gets an error so the original rebase failure surfaces unchanged. This
// is what keeps the self-heal from becoming a general-purpose "delete
// whatever is in the way".
func TestResolveOpFileCollisions_RefusesOutsideOpsDir(t *testing.T) {
	g, opRel := collisionFixture(t, "local", "remote")
	writeFile(t, filepath.Join(g.RepoRoot, "not-acts-file.txt"), "someone else's")

	for _, paths := range [][]string{
		{"not-acts-file.txt"},
		{opRel, "not-acts-file.txt"}, // one bad path poisons the whole batch
		{"../escape.json"},
		{"/etc/passwd"},
	} {
		_, _, _, err := g.resolveOpFileCollisions("main", paths)
		if err == nil {
			t.Errorf("resolveOpFileCollisions(%v) = nil error; want refusal", paths)
		}
	}
	// The all-or-nothing fence: the op file in the mixed batch must be
	// untouched, not half-resolved.
	if _, err := os.Stat(filepath.Join(g.RepoRoot, opRel)); err != nil {
		t.Errorf("op file was touched despite the batch being refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(g.RepoRoot, "not-acts-file.txt")); err != nil {
		t.Errorf("non-act file was touched: %v", err)
	}
}

// TestResolveOpFileCollisions_RefusesTrackedPath: the recovery only ever
// operates on untracked files. A tracked path reaching it means the
// caller's premise is wrong, and deleting a tracked file would be real
// data loss.
func TestResolveOpFileCollisions_RefusesTrackedPath(t *testing.T) {
	g, _ := collisionFixture(t, "local", "remote")
	tracked := "ops/act-bbbbbb/2026-07/tracked.json"
	writeFile(t, filepath.Join(g.RepoRoot, tracked), "committed")
	runGit(t, g.RepoRoot, "add", tracked)
	runGit(t, g.RepoRoot, "commit", "-q", "--no-verify", "-m", "track an op")

	if _, _, _, err := g.resolveOpFileCollisions("main", []string{tracked}); err == nil {
		t.Fatalf("resolveOpFileCollisions on a tracked path = nil error; want refusal")
	}
	if _, err := os.Stat(filepath.Join(g.RepoRoot, tracked)); err != nil {
		t.Errorf("tracked file was removed: %v", err)
	}
}

func TestUntrackedCheckoutCollisionPaths(t *testing.T) {
	out := `error: The following untracked working tree files would be overwritten by checkout:
	ops/act-aaa/2026-07/one.json
	ops/act-bbb/2026-07/two.json
Please move or remove them before you switch branches.
Aborting
error: could not detach HEAD
`
	got := untrackedCheckoutCollisionPaths(out)
	want := []string{"ops/act-aaa/2026-07/one.json", "ops/act-bbb/2026-07/two.json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d = %q, want %q", i, got[i], want[i])
		}
	}
	if p := untrackedCheckoutCollisionPaths("CONFLICT (content): Merge conflict in foo"); p != nil {
		t.Errorf("non-collision output parsed as a collision: %v", p)
	}
}

// TestDocClaim_Sync_ResolvesUntrackedOpCollision pins the act-650378
// claim in docs/spec.md — that act resolves untracked op-file collisions
// itself, deleting only byte-identical copies and moving differing ones
// to `.act/.collisions/` — to behavior. The doc half asserts the claim is
// present; the behavior half is the pair of recovery tests above, which
// this test names so a reader following the registry lands on them.
func TestDocClaim_Sync_ResolvesUntrackedOpCollision(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"Untracked op-file collisions are resolved by act, not by the operator (act-650378)",
		"`.act/.collisions/<timestamp>/<original-path>`",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docs/spec.md missing the act-650378 claim %q", want)
		}
	}

	// Behavioral anchor: the delete-identical / move-differing split the
	// spec promises, asserted here so the doc claim is not pinned to
	// prose alone.
	same := `{"identical":true}`
	g, opRel := collisionFixture(t, same, same)
	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase (identical): %v", err)
	}
	if !g.isTracked(opRel) {
		t.Errorf("identical-content collision did not resolve to origin's tracked copy")
	}

	g2, opRel2 := collisionFixture(t, `{"mine":true}`, `{"theirs":true}`)
	if err := g2.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase (differing): %v", err)
	}
	var preserved bool
	_ = filepath.Walk(filepath.Join(g2.RepoRoot, collisionQuarantineDir),
		func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(filepath.ToSlash(p), opRel2) {
				preserved = true
			}
			return nil
		})
	if !preserved {
		t.Errorf("differing collision was not preserved under %s/", collisionQuarantineDir)
	}
}
