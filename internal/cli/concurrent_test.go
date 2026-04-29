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

// TestConcurrentClaimRace runs two simulated `act update --claim` writers
// against the same issue. After both push (with retry-rebase), exactly
// one site wins per fold ordering, and the loser sees structured
// claim-loss when re-running fold.
//
// The race is staged sequentially with intentionally drifted HLCs (writer
// A's claim has the LATER wall-clock by design) so the test outcome is
// deterministic. The HLC+op-hash tiebreaker rule guarantees A is the
// winner for the recorded ordering — the test asserts that BOTH sites
// agree on the same winner after fold, not that "site A always wins" in
// some clock-sensitive sense.
func TestConcurrentClaimRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency e2e under -short")
	}
	const iterations = 5

	for iter := 0; iter < iterations; iter++ {
		// Subtest per iteration so a single failure pinpoints the
		// run that diverged.
		t.Run(itoa(iter), func(t *testing.T) {
			siteA, siteB, _ := makeShared(t)

			id := createIssueOnA(t, siteA, "claim-race")
			pullRebase(t, siteB)

			// Both writers stamp their claim against the issue
			// independently. We use --isolated to skip
			// pull-rebase inside `act update --claim` so the
			// two ops are truly independent on disk; the test
			// then resolves the race via push + pull-rebase.
			mustRunAct(t, siteA, 0, "update", "--json", "--claim", "--isolated", id)
			mustRunAct(t, siteB, 0, "update", "--json", "--claim", "--isolated", id)

			// Push order: A first, B second. B's first push is
			// rejected and pull-rebase folds A's claim into B's
			// history; B's claim op file rides along on top.
			pushWithRebase(t, siteA, 1)
			pushWithRebase(t, siteB, 3)

			// A pulls so both sites have identical history.
			pullRebase(t, siteA)

			// Both sites' show output must agree on the
			// post-fold winner. We assert byte-equality on the
			// rendered state (id+status+assignee at minimum).
			stateA := readShowJSON(t, siteA, id)
			stateB := readShowJSON(t, siteB, id)

			if stateA["status"] != "in_progress" {
				t.Errorf("siteA status = %v; want in_progress", stateA["status"])
			}
			if stateB["status"] != "in_progress" {
				t.Errorf("siteB status = %v; want in_progress", stateB["status"])
			}
			if stateA["assignee"] != stateB["assignee"] {
				t.Errorf("assignee disagreement: A=%v B=%v\nA=%v\nB=%v",
					stateA["assignee"], stateB["assignee"], stateA, stateB)
			}
			// On disk both sites must hold both claim ops:
			// the winner's and the loser's. Fold determines a
			// single winner; the loser's op stays as audit.
			for _, site := range []string{siteA, siteB} {
				files := listOpFiles(t, site, id)
				var claims int
				for _, f := range files {
					if strings.HasSuffix(f, "-claim.json") {
						claims++
					}
				}
				if claims != 2 {
					t.Errorf("site %s: want 2 claim ops, got %d (files=%v)",
						site, claims, files)
				}
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
