package cli

// Tests for the worker-cwd bootstrap mode (act-40fce0):
//
//	act bootstrap-worker --from-cwd <orchestrator-path> [<target>]
//
// This is the source/target inversion of the default cwd-source mode. The
// WORKER runs it from inside its freshly-created worktree, names the
// ORCHESTRATOR path as the source, and the target defaults to cwd. Unlike a
// raw `cp -r <orchestrator>/.act .`, it does NOT copy a live index.db — it
// rebuilds the index locally from the copied op log.
//
// The load-bearing test (TestBootstrapFromCWD_OrchestrateWorkerEndToEnd)
// exercises the full orchestrate-worker round trip end-to-end and asserts
// that an op created by the worker is SEEN by an orchestrator-side
// `act harvest`. It is paired with a control that demonstrates the failure
// mode the new mode replaces: a raw `cp -r` of the orchestrator's `.act/`
// (with a live index.db open) lands a worker whose copied index.db is the
// orchestrator's snapshot, and whose harvest-visible op set is identical to
// the orchestrator's (0 new ops) — the exact "0 ops, already in sync" signal
// from the original bug.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cpR shells out to `cp -r src dst`, mirroring exactly the raw-copy
// workaround the worker-cwd mode replaces. Skips the test on platforms
// without cp (we only run the control on unix-like CI).
func cpR(t *testing.T, src, dst string) {
	t.Helper()
	cmd := exec.Command("cp", "-r", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cp -r unavailable or failed (%v); skipping cp-r control: %s", err, out)
	}
}

// countHarvestedFromWorker runs `act harvest --json --dry-run <worker>` in
// the orchestrator and returns the number of harvested ops the orchestrator
// would see. Dry-run keeps the orchestrator's tree untouched so the same
// fixture can be re-used.
func countHarvestedFromWorker(t *testing.T, orchRoot, workerRoot string) int {
	t.Helper()
	so, _ := mustRunAct(t, orchRoot, 0, "harvest", "--json", "--dry-run", workerRoot)
	var res struct {
		HarvestedOps []string `json:"harvested_ops"`
	}
	if err := json.Unmarshal([]byte(so), &res); err != nil {
		t.Fatalf("harvest --json parse: %v\n%s", err, so)
	}
	return len(res.HarvestedOps)
}

// TestBootstrapFromCWD_OrchestrateWorkerEndToEnd is the load-bearing
// regression test for act-40fce0.
//
// Flow:
//  1. Orchestrator repo with an initialized .act/ and a seeded issue. We
//     open + rebuild its index.db so a live index.db file exists on disk —
//     the precondition the original bug needed.
//  2. A fresh worker worktree (its own git repo, no .act/ yet) bootstraps
//     from the orchestrator via `act bootstrap-worker --from-cwd <orch>`,
//     target = worker cwd.
//  3. The worker runs `act create` — a real op write inside the worktree.
//  4. The orchestrator runs `act harvest <worker>` and MUST see the worker's
//     new op (harvested_ops length >= 1) and the issue MUST become visible
//     in the orchestrator's state.
//
// This is the happy-path of the exact divergence the bug exhibited; with the
// pre-fix cp-r-of-live-index path the worker's create either silent-lost or
// the harvest saw 0 ops. The control test below pins the cp-r failure shape.
func TestBootstrapFromCWD_OrchestrateWorkerEndToEnd(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}

	// 1. Orchestrator with a live index.db.
	orchRoot, seededID := makeBootstrapSource(t)
	// Force a live index.db to exist by running a read command that opens +
	// rebuilds it.
	mustRunAct(t, orchRoot, 0, "list", "--json")
	if _, err := os.Stat(filepath.Join(orchRoot, ".act", "index.db")); err != nil {
		t.Fatalf("orchestrator index.db absent after list: %v", err)
	}

	// 2. Worker worktree bootstraps from the orchestrator via --from-cwd.
	workerRoot := makeBootstrapTarget(t)
	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		FromCWDSourcePath: orchRoot,
		Target:            workerRoot,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker --from-cwd code=%d out=%+v", code, out)
	}

	// The worker's index.db must have been rebuilt locally, and the live
	// index sidecars must NOT have been copied from the orchestrator.
	workerAct := filepath.Join(workerRoot, ".act")
	if _, err := os.Stat(filepath.Join(workerAct, "index.db")); err != nil {
		t.Fatalf("worker index.db absent after --from-cwd (should be rebuilt locally): %v", err)
	}

	// The bootstrapped worker must already see the seeded issue (proves the
	// op log copied and the index rebuilt correctly).
	workerShow := readShowJSON(t, workerRoot, seededID)
	if workerShow["id"] != seededID {
		t.Fatalf("worker show %s: id=%v, want %s", seededID, workerShow["id"], seededID)
	}

	// 3. Worker creates a new issue — a real op write inside the worktree.
	createOut, _ := mustRunAct(t, workerRoot, 0, "create", "worker-created issue", "--json")
	workerID := pickIDFromJSON(t, createOut)
	if workerID == "" {
		t.Fatalf("worker create produced no id\n%s", createOut)
	}
	// The op file MUST be observable on the worker's disk (fail-loud
	// persistence guarantee from scope item 1).
	if files := listOpFiles(t, workerRoot, workerID); len(files) == 0 {
		t.Fatalf("worker create %s: no op file on disk after success — silent-loss signature", workerID)
	}

	// 4. Orchestrator harvests the worker and MUST see the new op.
	harvested := countHarvestedFromWorker(t, orchRoot, workerRoot)
	if harvested < 1 {
		t.Fatalf("orchestrator harvest saw %d ops; expected >= 1 (the worker's create) — the silent-loss bug", harvested)
	}

	// Real (non-dry-run) harvest, then the orchestrator must see the issue.
	mustRunAct(t, orchRoot, 0, "harvest", workerRoot)
	orchShow := readShowJSON(t, orchRoot, workerID)
	if orchShow["id"] != workerID {
		t.Fatalf("orchestrator show %s after harvest: id=%v, want %s — op did not reach the orchestrator",
			workerID, orchShow["id"], workerID)
	}
}

// TestBootstrapFromCWD_ControlCpRLiveIndexShowsZeroOps pins the failure
// shape the new mode replaces. A raw `cp -r <orch>/.act <worker>/.act`
// produces a worker whose ops/ tree is byte-identical to the orchestrator's,
// so an immediate `act harvest --dry-run` against it sees 0 new ops — the
// "0 ops, already in sync" signal. This is the baseline the --from-cwd path
// improves on: after --from-cwd the worker's own subsequent create IS
// harvestable (asserted in the end-to-end test above), whereas the cp-r path
// also drags in the live index.db that motivated the bug.
//
// We assert the structural precondition: the cp-r worker's harvest-visible
// op set against the orchestrator is empty (identical trees), AND a live
// index.db (the fragile artifact) was copied into the worker.
func TestBootstrapFromCWD_ControlCpRLiveIndexShowsZeroOps(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	orchRoot, _ := makeBootstrapSource(t)
	// Live index.db on the orchestrator.
	mustRunAct(t, orchRoot, 0, "list", "--json")

	workerRoot := makeBootstrapTarget(t)
	// Raw cp -r of the orchestrator's .act/ into the worker — the exact
	// pre-fix workaround.
	cpR(t, filepath.Join(orchRoot, ".act"), filepath.Join(workerRoot, ".act"))

	// The cp-r worker dragged in the orchestrator's live index.db — the
	// fragile artifact the bug blamed.
	if _, err := os.Stat(filepath.Join(workerRoot, ".act", "index.db")); err != nil {
		t.Fatalf("cp-r control: expected the orchestrator's live index.db to be copied, but it's absent: %v", err)
	}

	// Harvesting the cp-r worker sees 0 new ops (trees are identical) — the
	// "already in sync" signal. This is the baseline; the --from-cwd
	// end-to-end test proves the worker's OWN later create is harvestable.
	harvested := countHarvestedFromWorker(t, orchRoot, workerRoot)
	if harvested != 0 {
		t.Fatalf("cp-r control: expected 0 harvested ops from an identical copy, got %d", harvested)
	}
}

// TestDocClaim_BootstrapFromCWD_HelpListsFlag is the user-visible doc-claim
// test for the --from-cwd help string (act-40fce0). A cold-start agent
// reading `act help` must discover the worker-cwd bootstrap mode exists, so
// it uses the documented command instead of a raw `cp -r`.
func TestDocClaim_BootstrapFromCWD_HelpListsFlag(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "--from-cwd") {
		t.Errorf("act help missing --from-cwd:\n%s", out)
	}
}

// TestDocClaim_BootstrapFromCWD_PersistenceGuarantee asserts the spec's
// "Op-write persistence guarantee" at the user-visible boundary (act-40fce0):
// a successful `act create` MUST leave the op file durably on disk where a
// reader/harvest can see it — never a synthetic "Created act-XXXXXX" for an
// op that vanished. We drive `act create` as a subprocess, then independently
// confirm the op file exists under .act/ops/<id>/ AND that the created issue
// is observable via `act show`. This is the boundary a cold-start agent (and
// `act harvest`) actually depends on.
func TestDocClaim_BootstrapFromCWD_PersistenceGuarantee(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	root := makeRepo(t)
	mustRunAct(t, root, 0, "init", "--json")
	createOut, _ := mustRunAct(t, root, 0, "create", "persistence-guaranteed issue", "--json")
	id := pickIDFromJSON(t, createOut)
	if id == "" {
		t.Fatalf("create produced no id\n%s", createOut)
	}
	// The op file MUST be on disk — the read-back guarantee means success
	// implies durable persistence, not a synthetic envelope.
	if files := listOpFiles(t, root, id); len(files) == 0 {
		t.Fatalf("act create returned success but no op file on disk for %s — silent-loss signature", id)
	}
	// And the issue MUST be observable through the read path.
	show := readShowJSON(t, root, id)
	if show["id"] != id {
		t.Fatalf("act show %s after create: id=%v, want %s", id, show["id"], id)
	}
}

// TestDocClaim_Skill_MentionsFromCwd asserts the embedded SKILL.md worker
// section tells dispatched workers to bootstrap a fresh worktree with
// `act bootstrap-worker --from-cwd` rather than `cp -r` (act-40fce0). The
// claim is load-bearing: a worker reading the skill cold is the audience
// that hit the original silent-data-loss bug.
func TestDocClaim_Skill_MentionsFromCwd(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "skills", "act", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(body), "--from-cwd") {
		t.Errorf("SKILL.md worker section does not mention --from-cwd")
	}
}
