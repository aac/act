package cli

// End-to-end concurrency / rebase-contention tests that exercise the
// multi-writer scenarios from spec-v2 §7.4 and the brief's "Multi-writer
// semantics" section.
//
// Each test builds a fresh fixture via makeShared (a bare repo + two or
// three site clones) and drives real `act` and `git` subprocesses. The
// tests are slow by design — every `act` invocation forks a new binary
// and every push / pull-rebase round-trips through the bare repo on disk
// — so we honor `testing.Short()` and skip when -short is passed.
//
// The shape of the assertions follows the issue's acceptance criteria:
//
//   - TestConcurrentDistinctOps      — disjoint-field updates co-survive.
//   - TestConcurrentClaimRace        — exactly one claim winner per race.
//   - TestRebaseContention           — same-field LWW deterministic
//                                      across all sites.
//   - TestConcurrentDistinctOpsBidirectional — two-way pull eventually
//                                      converges on both sides.
//
// Iteration counts are dialed down from the spec's 100 / 50 to keep CI
// runtime sane; the loops still exercise the race window enough to catch
// regressions. Bumping the counts via a future env var is a follow-up.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConcurrentDistinctOps verifies that two writers updating different
// fields of the same issue both have their ops survive after a
// `git pull --rebase` cycle. The op-files merge with no conflicts because
// their filenames differ (HLC + payload-hash + node_id).
func TestConcurrentDistinctOps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency e2e under -short")
	}
	// Phase 1 (docs/coordination-plane-design.md): act state lives in
	// a nested .act/ git repo, gitignored from the host. Multi-machine
	// op-sync against a shared remote is Phase 2 territory ("act
	// sync"). The fixture this test uses (siteA + siteB sharing host
	// origin) pushes/pulls op files through the host remote, which
	// the host's .gitignore now blocks. Re-enable this test when
	// Phase 2 ships and the nested repo gains its own remote-sync.
	t.Skip("Phase 1: multi-machine act-state sync is Phase 2 work (docs/coordination-plane-design.md)")
	siteA, siteB, _ := makeShared(t)

	// 1. A creates the issue and pushes it.
	id := createIssueOnA(t, siteA, "shared-issue")

	// 2. B pulls so it sees the new issue.
	pullRebase(t, siteB)

	// 3. A updates the description (no push yet).
	mustRunAct(t, siteA, 0, "update", "--json", "--description", "from-A", id)
	// 4. B updates the assignee (no push yet).
	mustRunAct(t, siteB, 0, "update", "--json", "--assignee", "alice", id)

	// 5. A pushes; succeeds (clean fast-forward).
	pushWithRebase(t, siteA, 1)
	// 6. B pushes; the first attempt is rejected, retry pulls --rebase.
	pushWithRebase(t, siteB, 3)

	// 7. A pulls B's commit so both sites have identical history.
	pullRebase(t, siteA)

	// 8. Both sites must show two distinct update_field op files.
	for _, site := range []string{siteA, siteB} {
		files := listOpFiles(t, site, id)
		var creates, updates int
		for _, f := range files {
			switch {
			case strings.HasSuffix(f, "-create.json"):
				creates++
			case strings.HasSuffix(f, "-update_field.json"):
				updates++
			}
		}
		if creates != 1 {
			t.Errorf("site %s: want 1 create op, got %d (files=%v)", site, creates, files)
		}
		if updates != 2 {
			t.Errorf("site %s: want 2 update_field ops (different fields), got %d (files=%v)",
				site, updates, files)
		}
	}
}

// TestConcurrentClaimRace verifies the last-write-wins claim documented in
// README.md ("atomic; concurrent claimers resolve last-write-wins") and
// spec-v2.md §7.4 ("concurrent_claim_two_writers").
//
// The test drives two sequential `act update --claim --isolated` invocations
// that share the same .act/ops/ directory but carry different node_ids (via
// a config.json swap between invocations). Sequential invocation is
// equivalent to truly concurrent for this assertion because the mechanism
// under test — fold winner-selection from two on-disk claim ops — is
// independent of subprocess launch order. True parallelism would require
// git index-lock contention which is a separate concern (claim_failed vs
// claim_lost). The fold ordering is deterministic: the first subprocess
// writes an op with an earlier wall-clock HLC and wins; the second writes a
// later HLC and loses. This matches spec §7.4's "two child processes, exactly
// one exits 0 with claimed:true, the other receives the claim_lost outcome."
//
// The single-machine approach avoids flakiness that would arise from relying
// on subprocess scheduling to produce a deterministic HLC ordering across
// truly concurrent processes.
//
// Spec §7.4: the loser exits 5 with error code claim_lost. The implementation
// (reconciled in act-a373bb) emits exit 5 with envelope
// {"ok":false,"claimed":false,"winner":...,"error":"claim_lost","reason":"lost-race"}.
// The test asserts that subprocess-boundary behavior (exit 5 + claimed:false +
// error:claim_lost).
func TestConcurrentClaimRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency e2e under -short")
	}
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set (TestMain did not run)")
	}

	// Run the race 5 times; each iteration gets a fresh issue in the same
	// repo so HLC drift doesn't accumulate across iterations. Spec §7.4
	// asks for 100 iterations; we use 5 to keep CI runtime bounded while
	// still exercising the fold ordering on multiple issues.
	//
	// Note: we intentionally do NOT reset the repo between iterations
	// because the HLC clock advances naturally — each new issue create
	// bumps the wall clock — ensuring the first invocation of each pair
	// always has the earlier HLC and thus wins.
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "alice@example.com", "Alice")
	mustRunAct(t, site, 0, "init", "--json")

	const iterations = 5
	for iter := 0; iter < iterations; iter++ {
		t.Run(itoa(iter), func(t *testing.T) {
			// Create a fresh issue for this iteration.
			createOut, _ := mustRunAct(t, site, 0, "create", "claim-race-probe", "--json")
			id := pickIDFromJSON(t, createOut)

			// Step 1: read the current node_id (winner's identity).
			cfgPath := filepath.Join(site, ".act", "config.json")
			cfgData, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatalf("read .act/config.json: %v", err)
			}
			var cfg map[string]any
			if err := json.Unmarshal(cfgData, &cfg); err != nil {
				t.Fatalf("unmarshal config.json: %v", err)
			}
			winnerNodeID, _ := cfg["node_id"].(string)
			if winnerNodeID == "" {
				t.Fatalf("node_id missing from config.json")
			}

			// Step 2: invocation A (winner). Uses --isolated so pull-rebase
			// is skipped; only this process's op file lands on disk.
			winOut, _, winCode := runAct(t, site, "update", "--claim", "--isolated", "--json", id)
			if winCode != 0 {
				t.Fatalf("winner claim: exit %d\n%s", winCode, winOut)
			}
			// Verify winner envelope shape.
			var winResult map[string]any
			if err := json.Unmarshal([]byte(winOut), &winResult); err != nil {
				t.Fatalf("winner claim: invalid JSON: %v\n%s", err, winOut)
			}
			if winResult["claimed"] != true {
				t.Errorf("iter %d: winner claim: claimed=%v, want true", iter, winResult["claimed"])
			}
			if winResult["winner"] != winnerNodeID {
				t.Errorf("iter %d: winner claim: winner=%v, want %v", iter, winResult["winner"], winnerNodeID)
			}

			// Step 3: change config.json to a different node_id so the
			// second invocation runs as a different "agent". The loser's
			// op will have a later wall-clock HLC (it runs after the
			// winner) and thus loses the fold ordering.
			const loserNodeID = "deadbeef"
			cfg["node_id"] = loserNodeID
			newCfg, err := json.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal modified config: %v", err)
			}
			if err := os.WriteFile(cfgPath, newCfg, 0o600); err != nil {
				t.Fatalf("write modified config: %v", err)
			}
			t.Cleanup(func() {
				// Restore winner's config.json so subsequent
				// iterations see consistent state.
				if wErr := os.WriteFile(cfgPath, cfgData, 0o600); wErr != nil {
					t.Logf("restore config.json: %v (test isolation may be affected)", wErr)
				}
			})

			// Step 4: invocation B (loser). Runs after A with a later
			// wall-clock HLC. After committing its own op, fold sees
			// both ops; A's earlier HLC wins; B exits 5 (claim_lost) with
			// claimed:false.
			loseOut, _, loseCode := runAct(t, site, "update", "--claim", "--isolated", "--json", id)
			if loseCode != 5 {
				t.Errorf("iter %d: loser claim: exit %d, want 5 (claim_lost); output:\n%s", iter, loseCode, loseOut)
			}
			// Verify loser envelope shape at the subprocess boundary.
			var loseResult map[string]any
			if err := json.Unmarshal([]byte(loseOut), &loseResult); err != nil {
				t.Fatalf("iter %d: loser claim: invalid JSON: %v\n%s", iter, err, loseOut)
			}
			if loseResult["claimed"] != false {
				t.Errorf("iter %d: loser claim: claimed=%v, want false", iter, loseResult["claimed"])
			}
			if loseResult["error"] != "claim_lost" {
				t.Errorf("iter %d: loser claim: error=%v, want claim_lost", iter, loseResult["error"])
			}
			// winner field must be the actual winner's node_id, not the loser's.
			if loseResult["winner"] != winnerNodeID {
				t.Errorf("iter %d: loser claim: winner=%v, want %v (not loser %s)",
					iter, loseResult["winner"], winnerNodeID, loserNodeID)
			}
			// id field must match the contested issue.
			if loseResult["id"] != id {
				t.Errorf("iter %d: loser claim: id=%v, want %v", iter, loseResult["id"], id)
			}
			// Exactly two claim op files must exist for this issue (one per invocation).
			opFiles := listOpFiles(t, site, id)
			var claims int
			for _, f := range opFiles {
				if strings.HasSuffix(f, "-claim.json") {
					claims++
				}
			}
			if claims != 2 {
				t.Errorf("iter %d: want 2 claim ops on disk, got %d (files=%v)",
					iter, claims, opFiles)
			}
		})
	}
}

// TestRebaseContention exercises three writers updating the same issue
// concurrently. After all pushes settle, fold output must be identical
// across all three sites — the LWW + op-hash tiebreaker yields a single
// deterministic winner that every site agrees on.
//
// Spec asks for 50 iterations; we run a small number per CI run to keep
// runtime bounded while still exercising the race window. Every
// iteration uses a fresh fixture.
func TestRebaseContention(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency e2e under -short")
	}
	t.Skip("Phase 1: multi-machine act-state sync is Phase 2 work (docs/coordination-plane-design.md)")
	const iterations = 3

	for iter := 0; iter < iterations; iter++ {
		t.Run(itoa(iter), func(t *testing.T) {
			siteA, siteB, _ := makeShared(t)
			// Add a third site by cloning from the bare repo
			// next to siteA/siteB.
			siteC := siteA + "_C"
			runGit(t, siteA, "clone", "-q", "../remote.git", siteC)
			configureSite(t, siteC, "carol@example.com", "Carol")
			mustRunAct(t, siteC, 0, "init", "--force", "--json")
			runGit(t, siteC, "add", ".act")
			if !workingTreeClean(t, siteC) {
				runGit(t, siteC, "commit", "-q", "--no-verify", "-m", "act init C")
				pushWithRebase(t, siteC, 3)
			}
			// A and B sync up with C's init commit.
			pullRebase(t, siteA)
			pullRebase(t, siteB)

			id := createIssueOnA(t, siteA, "rebase-contention")
			pushWithRebase(t, siteA, 1)
			pullRebase(t, siteB)
			pullRebase(t, siteC)

			// All three writers issue isolated claim ops on
			// the same issue. Each site has a distinct
			// node_id, so the three ops have distinct
			// filenames. The winner is determined by LWW on
			// (HLC, op-hash); whichever it is, all sites must
			// agree.
			mustRunAct(t, siteA, 0, "update", "--json", "--claim", "--isolated", id)
			mustRunAct(t, siteB, 0, "update", "--json", "--claim", "--isolated", id)
			mustRunAct(t, siteC, 0, "update", "--json", "--claim", "--isolated", id)

			// Push order arbitrary; each retry-rebases until
			// it succeeds. We push in (A, C, B) order so B is
			// last and has to rebase past two predecessors —
			// the harshest schedule.
			pushWithRebase(t, siteA, 1)
			pushWithRebase(t, siteC, 3)
			pushWithRebase(t, siteB, 3)

			// All three sites pull final history.
			pullRebase(t, siteA)
			pullRebase(t, siteC)

			// Fold output must be identical across all three.
			stateA := readShowJSON(t, siteA, id)
			stateB := readShowJSON(t, siteB, id)
			stateC := readShowJSON(t, siteC, id)
			if !sameWinner(stateA, stateB, stateC) {
				t.Errorf("rebase contention: sites disagree\nA=%v\nB=%v\nC=%v",
					stateA, stateB, stateC)
			}

			// Op-file count is exactly three on every site.
			for _, site := range []string{siteA, siteB, siteC} {
				files := listOpFiles(t, site, id)
				var claims int
				for _, f := range files {
					if strings.HasSuffix(f, "-claim.json") {
						claims++
					}
				}
				if claims != 3 {
					t.Errorf("site %s: want 3 claim ops, got %d (files=%v)",
						site, claims, files)
				}
			}
		})
	}
}

// TestConcurrentDistinctOpsBidirectional verifies the bidirectional pull
// scenario: A writes; B writes; A pulls; B pulls. After both pulls,
// both sites must observe both ops on disk and produce identical fold
// output. Repeated pulls are idempotent (no new commits the second time).
func TestConcurrentDistinctOpsBidirectional(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency e2e under -short")
	}
	t.Skip("Phase 1: multi-machine act-state sync is Phase 2 work (docs/coordination-plane-design.md)")
	siteA, siteB, _ := makeShared(t)

	id := createIssueOnA(t, siteA, "bidir")
	pullRebase(t, siteB)

	// A writes status=blocked; B writes assignee=bob. Each writer
	// pushes after committing. (Push order doesn't matter here; both
	// retry-rebase.)
	mustRunAct(t, siteA, 0, "update", "--json", "--status", "blocked", id)
	mustRunAct(t, siteB, 0, "update", "--json", "--assignee", "bob", id)
	pushWithRebase(t, siteA, 1)
	pushWithRebase(t, siteB, 3)

	// Both sides pull. After this, both sites see both ops.
	pullRebase(t, siteA)
	// B already pulled implicitly during pushWithRebase rebase; pull
	// again as a no-op to assert idempotence.
	headBefore, err := runGitOut(siteB, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD on siteB: %v", err)
	}
	pullRebase(t, siteB)
	headAfter, err := runGitOut(siteB, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD on siteB: %v", err)
	}
	if headBefore != headAfter {
		t.Errorf("idempotent pull on siteB moved HEAD: %s -> %s", headBefore, headAfter)
	}

	// Both sites must hold the same set of op files (same names).
	filesA := listOpFiles(t, siteA, id)
	filesB := listOpFiles(t, siteB, id)
	if !sameStringSet(filesA, filesB) {
		t.Errorf("op-file disagreement after bidirectional sync\nA=%v\nB=%v", filesA, filesB)
	}
	// Two update_field ops + one create.
	var creates, updates int
	for _, f := range filesA {
		switch {
		case strings.HasSuffix(f, "-create.json"):
			creates++
		case strings.HasSuffix(f, "-update_field.json"):
			updates++
		}
	}
	if creates != 1 || updates != 2 {
		t.Errorf("op file shape: creates=%d updates=%d (want 1, 2); files=%v",
			creates, updates, filesA)
	}
}

// TestConcurrentClaimRace_TwoSiteConvergence is the faithful two-site
// convergence variant of TestConcurrentClaimRace: two independent .act/.git
// repos (siteA and siteB) each claim the same issue, then push/pull-rebase
// to converge, and both sites must end up agreeing on exactly one HLC winner
// (claimed:true) with the loser op retained in the fold output.
//
// This test is SKIPPED until Phase 2 multi-machine sync lands
// (docs/coordination-plane-design.md, §"Phase 2 — act sync"). The
// Phase 1 nested-.act/.git design has no mechanism for two independent
// .act remotes to exchange op files via push/pull-rebase; once Phase 2
// ships the nested repo gains its own `act remote` and `act remote sync`,
// which provides exactly the two-site push/pull-rebase surface this test
// requires.
//
// When Phase 2 lands:
//  1. Remove the t.Skip call below.
//  2. Build the two-site fixture via makeShared (or a new Phase 2 variant
//     that wires two nested .act remotes instead of sharing the host origin).
//  3. Assert that after push/pull-rebase convergence:
//     - Both sites produce identical fold output for the contested issue.
//     - Exactly one site reports claimed:true (winner); the other
//     reports claimed:false, error:claim_lost (loser op retained).
//     - Op-file counts on both sites are exactly 2 (one per claimant).
//
// Do NOT remove this placeholder — it documents the deferred Phase 2 work
// that TestConcurrentClaimRace's single-site approximation cannot cover.
func TestConcurrentClaimRace_TwoSiteConvergence(t *testing.T) {
	t.Skip("Phase 2 dependency: faithful two-site convergence requires Phase 2 multi-machine sync (docs/coordination-plane-design.md, §Phase 2 — act sync); the single-site approximation in TestConcurrentClaimRace covers fold winner-selection until then")
}

// itoa is a tiny non-fmt int-to-string for subtest names.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// sameWinner reports whether all three rendered-state maps agree on the
// fold-determined winner fields (status + assignee). Other fields can
// legitimately differ when ops carry distinct payloads beyond the
// contended one, but the LWW field set must converge.
func sameWinner(a, b, c map[string]any) bool {
	pick := func(m map[string]any) [2]any { return [2]any{m["status"], m["assignee"]} }
	return pick(a) == pick(b) && pick(b) == pick(c)
}

// sameStringSet reports whether the two slices contain the same elements
// (multiset semantics). Used to compare op-file listings.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
		if counts[s] < 0 {
			return false
		}
	}
	return true
}
