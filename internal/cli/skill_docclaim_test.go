package cli

// Doc-claim regression tests for the act skill (internal/skill/SKILL.md).
//
// These tests pin user-visible claims made in the skill — specifically the
// "Working in a worktree or sandbox" section added in act-9e7078, which
// tells dispatched sub-agents that the orchestrator pre-seeds .act/ via
// `act bootstrap-worker` at dispatch and harvests their ops via
// `act harvest` at teardown.
//
// The orchestrate slash-command itself (~/.claude/commands/orchestrate.md)
// makes the matching claims on the orchestrator side, but that file lives
// OUTSIDE this repo (claude-config) and the sweep harness can't reach it
// — see the comment block in docs_sweep_test.go for the rationale.
//
// Failure shape: if a maintainer removes or paraphrases the worker-protocol
// section, or drops one of the load-bearing subcommand references,
// `go test ./internal/cli/...` fails with a pointer at the broken claim.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSkillBody returns the on-disk bytes of internal/skill/SKILL.md.
// The file is the canonical source the `act install-skill` command
// embeds and ships into ~/.claude/skills/act/SKILL.md, so asserting on
// it asserts on what real agent sessions actually read.
func readSkillBody(t *testing.T) string {
	t.Helper()
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "internal/skill/SKILL.md"))
	if err != nil {
		t.Fatalf("read internal/skill/SKILL.md: %v", err)
	}
	return string(body)
}

// TestDocClaim_Skill_WorkerProtocolSection asserts the skill carries the
// "Working in a worktree or sandbox" section. The header literal is the
// boundary a cold-start worker would land on when scanning the skill;
// dropping the header silently is the failure mode worth catching.
func TestDocClaim_Skill_WorkerProtocolSection(t *testing.T) {
	body := readSkillBody(t)
	const claim = "Working in a worktree or sandbox"
	if !strings.Contains(body, claim) {
		t.Errorf("internal/skill/SKILL.md no longer contains worker-protocol section header %q.\n"+
			"  This section tells dispatched sub-agents that the orchestrator handles\n"+
			"  bootstrap (at dispatch) and harvest (at teardown). If it's been moved or\n"+
			"  renamed, update the docClaimRegistry entry 'skill-worker-section' to point\n"+
			"  at the new header.", claim)
	}
}

// TestDocClaim_Skill_MentionsBootstrapWorker asserts the skill points
// workers at `act bootstrap-worker` as the orchestrator's pre-dispatch
// seeding step. Without this reference a cold-start worker reading only
// the skill won't know its .act/ was pre-seeded and might try to
// `act init` over the top.
func TestDocClaim_Skill_MentionsBootstrapWorker(t *testing.T) {
	body := readSkillBody(t)
	const claim = "bootstrap-worker"
	if !strings.Contains(body, claim) {
		t.Errorf("internal/skill/SKILL.md no longer mentions %q.\n"+
			"  Workers need to know the orchestrator pre-seeds .act/ via this subcommand;\n"+
			"  if the reference is gone, the worker-protocol section is no longer\n"+
			"  load-bearing. Either re-introduce the reference or drop the registry\n"+
			"  entry 'skill-worker-bootstrap-ref'.", claim)
	}
}

// TestDocClaim_Skill_MentionsHarvest asserts the skill points workers at
// `act harvest` as the orchestrator's at-teardown op-collection step.
// Without this reference workers may invent their own coordination
// (mid-flight pushes, manual rsync) instead of trusting the orchestrator.
func TestDocClaim_Skill_MentionsHarvest(t *testing.T) {
	body := readSkillBody(t)
	const claim = "harvest"
	if !strings.Contains(body, claim) {
		t.Errorf("internal/skill/SKILL.md no longer mentions %q.\n"+
			"  Workers need to know the orchestrator collects their ops via this\n"+
			"  subcommand at teardown; if the reference is gone, the worker-protocol\n"+
			"  section is no longer load-bearing. Either re-introduce the reference\n"+
			"  or drop the registry entry 'skill-worker-harvest-ref'.", claim)
	}
}
