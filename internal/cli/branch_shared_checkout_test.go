package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// branchTestNestedHead returns the branch the nested .act/ repo under root
// has checked out, as `git -C <root>/.act symbolic-ref --short HEAD` prints it.
func branchTestNestedHead(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(root, ".act"), "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("git symbolic-ref in %s/.act: %v", root, err)
	}
	return strings.TrimSpace(string(out))
}

// branchTestNestedTip returns the commit the nested repo's HEAD points at.
func branchTestNestedTip(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(root, ".act"), "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse in %s/.act: %v", root, err)
	}
	return strings.TrimSpace(string(out))
}

// branchTestMessage returns the message field of any command's error output.
func branchTestMessage(out any) string {
	b, _ := json.Marshal(out)
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Message
}

// branchTestWorkerCopy adds a linked worktree of root's host repo and copies
// root's nested .act/ into it — the shape `act bootstrap-worker` leaves in a
// dispatched worker's worktree. Returns the worktree path.
func branchTestWorkerCopy(t *testing.T, root string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, root, "worktree", "add", "-q", "-b", "wt-host", wt)
	if out, err := exec.Command("cp", "-R", filepath.Join(root, ".act"), filepath.Join(wt, ".act")).CombinedOutput(); err != nil {
		t.Fatalf("cp .act into worktree: %v\n%s", err, out)
	}
	return wt
}

// TestDocClaim_BranchFlag_RefusedOnSharedCheckout is act-94181c at the
// RunUpdate boundary: in a scratch tracker whose nested .act/ sits in the
// host repo's main working tree (a shared checkout), `act update --claim <id>
// --branch wt-x` is refused with bad_flag exit 2 naming the checkout, writes
// no op and no commit, and `git -C .act symbolic-ref --short HEAD` still
// prints main. Every other write command refuses the same way.
func TestDocClaim_BranchFlag_RefusedOnSharedCheckout(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	other, code := RunCreate(root, CreateOptions{Title: "other", Type: "task"})
	if code != 0 {
		t.Fatalf("seed other: code %d, out %+v", code, other)
	}
	otherID := other.(CreateResult).ID
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	wantAct := filepath.Join(realRoot, ".act")
	if got := branchTestNestedHead(t, root); got != "main" {
		t.Fatalf("fixture nested HEAD = %q, want main", got)
	}
	tipBefore := branchTestNestedTip(t, root)

	out, code := RunUpdate(root, UpdateOptions{ID: id, Claim: true, Isolated: true, Branch: "wt-x"})
	if code != 2 {
		t.Fatalf("update --claim --branch on a shared checkout: code %d, want 2; out %+v", code, out)
	}
	e, ok := out.(UpdateErrorOutput)
	if !ok || e.Error != "bad_flag" || !strings.Contains(e.Message, wantAct) || !strings.Contains(e.Message, "shared checkout") {
		t.Fatalf("refusal = %+v, want bad_flag naming %s as a shared checkout", out, wantAct)
	}
	if got := branchTestNestedHead(t, root); got != "main" {
		t.Errorf("after the refused --branch claim, nested HEAD = %q, want main", got)
	}
	if got := branchTestNestedTip(t, root); got != tipBefore {
		t.Errorf("nested HEAD moved %s -> %s; a refused write must commit nothing", tipBefore, got)
	}
	if m, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-claim.json")); len(m) != 0 {
		t.Errorf("refused claim left op files: %v", m)
	}
	if _, err := os.Stat(filepath.Join(root, ".act", ".git", "refs", "heads", "wt-x")); err == nil {
		t.Errorf("refused claim created branch wt-x")
	}

	// The other write commands take the same guard.
	type run struct {
		name string
		fn   func() (any, int)
	}
	for _, r := range []run{
		{"update --priority", func() (any, int) {
			return RunUpdate(root, UpdateOptions{ID: id, Priority: intPtr(1), Branch: "wt-x"})
		}},
		{"create", func() (any, int) { return RunCreate(root, CreateOptions{Title: "x", Type: "task", Branch: "wt-x"}) }},
		{"close", func() (any, int) { return RunClose(root, CloseOptions{ID: otherID, Branch: "wt-x"}) }},
		{"dep add", func() (any, int) { return RunDepAdd(root, DepAddOptions{Child: id, Parent: otherID, Branch: "wt-x"}) }},
		{"dep add --external", func() (any, int) {
			return RunDepAddExternal(root, id, []string{"gh:o/r#1"}, DepAddOptions{Branch: "wt-x"})
		}},
		{"reopen", func() (any, int) { return RunReopen(root, ReopenOptions{ID: otherID, Branch: "wt-x"}) }},
		{"delete", func() (any, int) { return RunDelete(root, DeleteOptions{ID: otherID, Branch: "wt-x"}) }},
	} {
		out, code := r.fn()
		if code != 2 || !strings.Contains(branchTestMessage(out), wantAct) {
			t.Errorf("%s --branch on a shared checkout: code %d, out %+v; want exit 2 naming %s", r.name, code, out, wantAct)
		}
	}
	if got := branchTestNestedHead(t, root); got != "main" {
		t.Errorf("after the refused writes, nested HEAD = %q, want main", got)
	}
	if got := branchTestNestedTip(t, root); got != tipBefore {
		t.Errorf("nested HEAD moved %s -> %s across refused writes", tipBefore, got)
	}
}

// TestDocClaim_BranchFlag_WorkerCopyKeepsBranch: a worker's own .act/ copy
// inside a linked worktree is unchanged by act-94181c — the --branch claim
// commits on the named branch and leaves it checked out — and the shared
// checkout it was copied from stays on main.
func TestDocClaim_BranchFlag_WorkerCopyKeepsBranch(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	wt := branchTestWorkerCopy(t, root)

	out, code := RunUpdate(wt, UpdateOptions{ID: id, Claim: true, Isolated: true, Branch: "wt-x"})
	if code != 0 {
		t.Fatalf("update --claim --branch in a worker copy: code %d, want 0; out %+v", code, out)
	}
	if got := branchTestNestedHead(t, wt); got != "wt-x" {
		t.Errorf("worker copy nested HEAD = %q, want wt-x (the branch is left checked out)", got)
	}
	if got := branchTestNestedHead(t, root); got != "main" {
		t.Errorf("shared checkout nested HEAD = %q, want main", got)
	}
}

// TestDocClaim_BranchFlag_SymlinkedActIsShared: a worktree whose .act is a
// symlink into the main checkout writes through the shared checkout, so
// --branch is refused there and names the main checkout's .act/.
func TestDocClaim_BranchFlag_SymlinkedActIsShared(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, root, "worktree", "add", "-q", "-b", "wt-host", wt)
	if err := os.Symlink(filepath.Join(root, ".act"), filepath.Join(wt, ".act")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)

	out, code := RunUpdate(wt, UpdateOptions{ID: id, Claim: true, Isolated: true, Branch: "wt-x"})
	if code != 2 || !strings.Contains(branchTestMessage(out), filepath.Join(realRoot, ".act")) {
		t.Fatalf("--branch through a symlinked .act: code %d, out %+v; want exit 2 naming %s/.act", code, out, realRoot)
	}
	if got := branchTestNestedHead(t, root); got != "main" {
		t.Errorf("shared checkout nested HEAD = %q, want main", got)
	}
}
