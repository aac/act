package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/op"
)

// badBranch is a ref name git refuses (`..` is illegal in a ref), so
// EnsureBranch's `git checkout -b` fails deterministically — the fault
// injection for act-642a86, with no machine-wide resource touched.
const badBranch = "bad..name"

// untrackedOps returns `git status --porcelain -- ops` in the nested repo
// at actDir: anything listed is what a later `git add -- ops` would sweep.
func untrackedOps(t *testing.T, actDir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", actDir, "status", "--porcelain", "--", "ops").Output()
	if err != nil {
		t.Fatalf("git status in %s: %v", actDir, err)
	}
	return strings.TrimSpace(string(out))
}

// TestWriteOpAndAutoCommit_EnsureBranchFailureLeavesNoOp is act-642a86 at
// the single-op and batch write helpers: when the --branch switch fails,
// the write returns an error and no op file is left in ops/ for a later
// `git add -- ops` to sweep into another write's commit.
func TestWriteOpAndAutoCommit_EnsureBranchFailureLeavesNoOp(t *testing.T) {
	dir, paths := makeWriteRepo(t)
	g := gitops.NewActGitOps(dir)
	env, body := fixedEnvelope(t)
	before := opFilesUnder(t, dir)

	if err := WriteOpAndAutoCommit(env, body, paths, g, WriteOpts{Branch: badBranch}); err == nil {
		t.Fatal("WriteOpAndAutoCommit with a failing --branch switch: want error, got nil")
	}
	if got := opFilesUnder(t, dir); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("op files after failed single write = %v, want %v (op left behind)", got, before)
	}

	env2 := env
	env2.HLC.Logical = 1
	body2, err := env2.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	err = WriteOpsAndAutoCommit(
		[]op.Envelope{env, env2}, [][]byte{body, body2}, paths, g,
		WriteOpts{Branch: badBranch}, "act-block: test")
	if err == nil {
		t.Fatal("WriteOpsAndAutoCommit with a failing --branch switch: want error, got nil")
	}
	if got := opFilesUnder(t, dir); strings.Join(got, ",") != strings.Join(before, ",") {
		t.Fatalf("op files after failed batch write = %v, want %v (ops left behind)", got, before)
	}
}

// TestRunWrites_EnsureBranchFailureLeavesNoOp is act-642a86 at the command
// boundary, in a worker's own .act/ copy (the only place --branch is
// allowed): create, close and update --claim each fail when the branch
// switch fails, leave ops/ exactly as it was with nothing untracked, and a
// following plain write commits only its own op.
func TestRunWrites_EnsureBranchFailureLeavesNoOp(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	wt := branchTestWorkerCopy(t, root)
	actDir := filepath.Join(wt, ".act")
	before := opFilesUnder(t, wt)
	tipBefore := branchTestNestedTip(t, wt)

	type run struct {
		name string
		fn   func() (any, int)
	}
	for _, r := range []run{
		{"create", func() (any, int) { return RunCreate(wt, CreateOptions{Title: "x", Type: "task", Branch: badBranch}) }},
		{"close", func() (any, int) { return RunClose(wt, CloseOptions{ID: id, Branch: badBranch}) }},
		{"update --claim", func() (any, int) {
			return RunUpdate(wt, UpdateOptions{ID: id, Claim: true, Isolated: true, Branch: badBranch})
		}},
	} {
		out, code := r.fn()
		if code == 0 {
			t.Errorf("%s with a failing --branch switch: exit 0, want non-zero; out %+v", r.name, out)
		}
		if got := opFilesUnder(t, wt); strings.Join(got, ",") != strings.Join(before, ",") {
			t.Errorf("%s: op files after the failed write = %v, want %v", r.name, got, before)
		}
		if u := untrackedOps(t, actDir); u != "" {
			t.Errorf("%s: ops/ has uncommitted entries a later `git add -- ops` would sweep: %q", r.name, u)
		}
	}
	if got := branchTestNestedTip(t, wt); got != tipBefore {
		t.Fatalf("nested HEAD moved %s -> %s across failed writes", tipBefore, got)
	}

	// The next write's commit carries its own op and nothing else.
	if out, code := RunUpdate(wt, UpdateOptions{ID: id, Priority: intPtr(1)}); code != 0 {
		t.Fatalf("follow-up update: code %d, out %+v", code, out)
	}
	files, err := exec.Command("git", "-C", actDir, "show", "--name-only", "--format=", "HEAD").Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	var added []string
	for _, f := range strings.Fields(string(files)) {
		if strings.HasPrefix(f, "ops/") {
			added = append(added, f)
		}
	}
	if len(added) != 1 || !strings.Contains(added[0], id) {
		t.Fatalf("follow-up commit's op files = %v, want exactly one op for %s", added, id)
	}
}
