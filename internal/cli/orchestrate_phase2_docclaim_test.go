package cli

// Phase 2 doc-claim regression tests (act-95bc5c).
//
// Three load-bearing doc surfaces describe the Phase 2 push-attached
// dispatch flow:
//
//   1. The orchestrate command at ~/.claude/commands/orchestrate.md
//      (claude-config repo, OUTSIDE this repo) — names the
//      `act bootstrap-worker --from-remote` invocation that dispatchers
//      use when the project's orchestrator has `act.role=orchestrator`.
//   2. internal/skill/SKILL.md "Phase 2 dispatch (push-attached)"
//      section — explains the worker-side picture: push during
//      execution, harvest as fallback.
//   3. docs/migration-runbook.md "Phase 1.5 → Phase 2 cutover" section
//      — names the one-time operator setup (`act remote enable`,
//      `act remote add-upstream`) and the rollback path.
//
// The first claim's docFile lives outside the repo, so the sweep
// harness in docs_sweep_test.go can't index it (the harness walks the
// act repo root only). That claim is enforced by the test below
// directly, reading the symlink target via os.Readlink. The other two
// claims register normally in docs_sweep_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_OrchestratePhase2_FromRemoteFlow pins the orchestrate
// command's promise that Phase 2 dispatch uses `act bootstrap-worker
// --from-remote`. The file is symlinked from
// ~/.claude/commands/orchestrate.md into the claude-config repo; we
// resolve the symlink and read the target so the claim is asserted
// against the actual ground-truth source.
//
// Skips cleanly on CI runners where the symlink isn't present — the
// claim is real, but enforcement only fires on the contributor's
// machine that has the claude-config checkout wired in.
func TestDocClaim_OrchestratePhase2_FromRemoteFlow(t *testing.T) {
	symlinkPath := os.ExpandEnv("$HOME/.claude/commands/orchestrate.md")

	target, err := os.Readlink(symlinkPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Logf("skip: orchestrate.md symlink not present at %s (CI runner or fresh checkout)", symlinkPath)
			t.Skip()
		}
		t.Fatalf("readlink %s: %v", symlinkPath, err)
	}

	// Resolve relative symlink targets against the symlink's directory,
	// matching POSIX semantics.
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(symlinkPath), target)
	}

	body, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			t.Logf("skip: symlink resolved to %s but target does not exist", target)
			t.Skip()
		}
		t.Fatalf("read %s: %v", target, err)
	}

	const want = "act bootstrap-worker --from-remote"
	if !strings.Contains(string(body), want) {
		t.Errorf("orchestrate.md at %s no longer contains claim %q\n"+
			"  Phase 2 dispatch documentation must name the push-attached bootstrap invocation.",
			target, want)
	}
}

// TestDocClaim_Skill_Phase2DispatchSection pins the section header in
// the embedded act skill. Cold-start workers reading the skill must
// land on a section that names the Phase 2 dispatch shape; if the
// header drifts, the section becomes unfindable by a literal scan and
// agents fall back to the Phase 1.5 picture.
func TestDocClaim_Skill_Phase2DispatchSection(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "internal/skill/SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	const want = "Phase 2 dispatch (push-attached)"
	if !strings.Contains(string(body), want) {
		t.Errorf("internal/skill/SKILL.md no longer contains section header %q\n"+
			"  The Phase 2 dispatch picture for cold-start workers depends on this header.",
			want)
	}
}

// TestDocClaim_MigrationRunbook_Phase2Cutover pins the cutover section
// header in the runbook. An operator running through the runbook to
// enable Phase 2 on a project must land on a section with this name;
// drift makes the procedure unfindable from the table-of-contents
// scan.
func TestDocClaim_MigrationRunbook_Phase2Cutover(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs/migration-runbook.md"))
	if err != nil {
		t.Fatalf("read migration-runbook.md: %v", err)
	}
	const want = "Phase 1.5 → Phase 2 cutover"
	if !strings.Contains(string(body), want) {
		t.Errorf("docs/migration-runbook.md no longer contains section header %q\n"+
			"  The cutover procedure for Phase 2 depends on this header.",
			want)
	}
}
