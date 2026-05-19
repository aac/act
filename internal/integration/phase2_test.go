package integration

// Phase 2 coordination-plane end-to-end test suite (act-612646).
//
// Five behaviors covered, one test each — see the package doc comment
// in helpers_test.go for the AC → test-name mapping. All tests opt
// into t.Parallel() so wall-time stays under the ~2-minute budget.
//
// The tests deliberately do NOT share fixtures: each test owns its
// BareRemote / orchestrator / worker tempdirs so a failure in one
// does not cascade. The cost (one BareRemote setup per test) is
// trivial — bare-repo init is ~10ms — and the diagnostic clarity
// when one test fails is worth it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/testfixtures"
)

// ---------------------------------------------------------------------
// Test 1 — Two-machine round-trip (AC1).
//
// Setup: one BareRemote (shared `.act/.git` origin). Two local clones
// (machineA, machineB), each acting as an independent `.act/.git`
// working tree. machineA writes an op file, commits, pushes; machineB
// fetches and gets A's op. Then machineB writes its own op, commits,
// pushes; machineA fetches and now has both ops.
//
// Why this shape instead of full `act create` lifecycle: the AC1 claim
// is about the GIT propagation path between two .act/.git clones with
// a shared origin — exactly the substrate Phase 2 builds the
// orchestrator/worker model on. Doing it via raw git puts the
// assertion squarely on the propagation surface; the higher-level
// `act create` / harvest tests already cover the writer-side semantics
// (internal/cli/lifecycle_e2e_test.go).
// ---------------------------------------------------------------------

func TestE2E_TwoMachineRoundTrip(t *testing.T) {
	t.Parallel()

	bare := testfixtures.NewBareRemote(t)

	// Clone the bare twice — these stand in for the two .act/.git
	// clones the spec calls "two machines". The BareRemote was seeded
	// with one root commit (.gitkeep) so HEAD resolves immediately.
	machineA := t.TempDir()
	machineB := t.TempDir()
	mustGitIn(t, "", "clone", "-q", bare.URL, machineA)
	mustGitIn(t, "", "clone", "-q", bare.URL, machineB)
	configureRepo(t, machineA, "a@example.com", "MachineA")
	configureRepo(t, machineB, "b@example.com", "MachineB")

	// machineA writes an op file, commits, pushes.
	opPathA := filepath.Join(machineA, "ops", "machineA-op.json")
	if err := os.MkdirAll(filepath.Dir(opPathA), 0o755); err != nil {
		t.Fatalf("mkdir machineA ops: %v", err)
	}
	if err := os.WriteFile(opPathA, []byte(`{"node":"A"}`), 0o644); err != nil {
		t.Fatalf("write machineA op: %v", err)
	}
	mustGitIn(t, machineA, "add", "ops/machineA-op.json")
	mustGitIn(t, machineA, "commit", "-q", "--no-verify", "-m", "machineA op")
	mustGitIn(t, machineA, "push", "-q", "origin", "main")

	// machineB fetches and rebases. After this, machineB's working
	// tree should contain machineA's op file.
	mustGitIn(t, machineB, "pull", "-q", "--rebase", "origin", "main")
	if _, err := os.Stat(filepath.Join(machineB, "ops", "machineA-op.json")); err != nil {
		t.Fatalf("machineB did not receive machineA's op: %v", err)
	}

	// machineB writes its own op, commits, pushes.
	opPathB := filepath.Join(machineB, "ops", "machineB-op.json")
	if err := os.WriteFile(opPathB, []byte(`{"node":"B"}`), 0o644); err != nil {
		t.Fatalf("write machineB op: %v", err)
	}
	mustGitIn(t, machineB, "add", "ops/machineB-op.json")
	mustGitIn(t, machineB, "commit", "-q", "--no-verify", "-m", "machineB op")
	mustGitIn(t, machineB, "push", "-q", "origin", "main")

	// machineA fetches: both ops now visible on A.
	mustGitIn(t, machineA, "pull", "-q", "--rebase", "origin", "main")
	for _, want := range []string{"machineA-op.json", "machineB-op.json"} {
		if _, err := os.Stat(filepath.Join(machineA, "ops", want)); err != nil {
			t.Errorf("machineA missing %s after round-trip: %v", want, err)
		}
	}
}

// ---------------------------------------------------------------------
// Test 2 — Push contention under high concurrency (AC2).
//
// Four parallel workers, 50 ops each, all racing on one BareRemote.
// Each worker clones, writes 50 unique op files, commits (one commit
// per worker so the rebase loop has work to do but the test wall time
// stays reasonable), then pushes via gitops.PushWithRetry — the same
// retry loop the production write path uses.
//
// Final assertion: a fresh clone of the bare sees 4 * 50 = 200 op
// files, i.e. the union of all writers (no lost updates).
// ---------------------------------------------------------------------

func TestE2E_PushContentionFourByFifty(t *testing.T) {
	t.Parallel()

	bare := testfixtures.NewBareRemote(t)

	const (
		numWorkers   = 4
		opsPerWorker = 50
	)

	var wg sync.WaitGroup
	errs := make([]error, numWorkers)
	for i := 0; i < numWorkers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			work := t.TempDir()
			if _, err := runGitIn("", "clone", "-q", bare.URL, work); err != nil {
				errs[i] = fmt.Errorf("worker %d: clone: %w", i, err)
				return
			}
			// Identity for each worker so commits don't fail.
			if _, err := runGitIn(work, "config", "user.email",
				fmt.Sprintf("w%d@example.com", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: identity email: %w", i, err)
				return
			}
			if _, err := runGitIn(work, "config", "user.name",
				fmt.Sprintf("Worker%d", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: identity name: %w", i, err)
				return
			}
			if _, err := runGitIn(work, "config", "commit.gpgsign", "false"); err != nil {
				errs[i] = fmt.Errorf("worker %d: gpg off: %w", i, err)
				return
			}

			// Write 50 unique op files. Filenames embed the worker
			// index so they are GUARANTEED disjoint across workers
			// (no content collision; the test asserts the union, so
			// each file must be uniquely attributable).
			opsDir := filepath.Join(work, "ops")
			if err := os.MkdirAll(opsDir, 0o755); err != nil {
				errs[i] = fmt.Errorf("worker %d: mkdir ops: %w", i, err)
				return
			}
			for j := 0; j < opsPerWorker; j++ {
				name := fmt.Sprintf("w%d-op%03d.json", i, j)
				body := []byte(fmt.Sprintf(`{"worker":%d,"seq":%d}`, i, j))
				if err := os.WriteFile(filepath.Join(opsDir, name), body, 0o644); err != nil {
					errs[i] = fmt.Errorf("worker %d: write %s: %w", i, name, err)
					return
				}
			}
			if _, err := runGitIn(work, "add", "ops"); err != nil {
				errs[i] = fmt.Errorf("worker %d: git add: %w", i, err)
				return
			}
			if _, err := runGitIn(work, "commit", "-q", "--no-verify", "-m",
				fmt.Sprintf("worker %d batch", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: git commit: %w", i, err)
				return
			}

			// PushWithRetry: the production retry loop. Concurrent
			// workers will race on the bare's main ref; the loser
			// rebases and retries.
			g := gitops.NewActGitOps(work)
			if err := g.PushWithRetry("main", gitops.PushOpts{}); err != nil {
				errs[i] = fmt.Errorf("worker %d: PushWithRetry: %w", i, err)
				return
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d failed: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}

	// Fresh clone of the bare to inspect the union. Per the AC text:
	// "post-test inspection of .act/ops/ showing the union of all
	// writers' op files (200 ops total)."
	inspector := t.TempDir()
	mustGitIn(t, "", "clone", "-q", bare.URL, inspector)
	got := countJSONFilesUnder(t, filepath.Join(inspector, "ops"))
	want := numWorkers * opsPerWorker
	if got != want {
		t.Errorf("union op count = %d, want %d (lost-update detected?)", got, want)
	}
}

// ---------------------------------------------------------------------
// Test 3 — Upstream drift (AC3).
//
// An orchestrator-role .act/.git has two remotes: `origin` (the
// orchestrator's authoritative bare for workers) and `origin-upstream`
// (where the orchestrator publishes consolidated state). After 60
// commits land on the orchestrator's local main but only some make it
// to origin-upstream, doctor's case (h) flags drift. `act remote sync`
// catches origin-upstream up; a second doctor pass sees no case (h).
//
// Implementation notes:
//
//   - The drift threshold defaults to 50 commits (see config.Default
//     EnableDefaults().UpstreamDriftThresholdCommits). 60 commits
//     deliberately exceeds it.
//   - case (h) requires both refs/remotes/origin and refs/remotes/
//     origin-upstream to be populated. Doctor's pre-(h) fetch arranges
//     this; we run with NoFetch=false so the fetch happens.
// ---------------------------------------------------------------------

func TestE2E_UpstreamDrift(t *testing.T) {
	t.Parallel()

	// Two bare remotes: one is the orchestrator's `origin`, the
	// other is `origin-upstream`. The orchestrator's `.act/.git` is
	// a working clone that has BOTH remotes configured.
	originBare := testfixtures.NewBareRemote(t)
	upstreamBare := testfixtures.NewBareRemote(t)

	// Stand up the orchestrator: a host repo + `act init` + `act
	// remote enable` (which writes act.role=orchestrator and the
	// drift-threshold config keys).
	host := newHostRepo(t)
	if _, code := cli.RunInit(host, false, "machine-orch", "orch@example.com",
		func() time.Time { return time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC) }); code != 0 {
		t.Fatalf("RunInit: code=%d", code)
	}
	if _, code := cli.RunRemote(cli.RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("RunRemote enable: code=%d", code)
	}

	actGitDir := filepath.Join(host, ".act", ".git")
	// Wire `origin` and `origin-upstream` into the nested .git via
	// --git-dir so cwd doesn't matter.
	mustGitIn(t, "", "--git-dir="+actGitDir, "remote", "add", "origin", originBare.URL)
	mustGitIn(t, "", "--git-dir="+actGitDir, "remote", "add", "origin-upstream", upstreamBare.URL)

	// Identity + gpg-off on the nested repo so commits below succeed.
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+filepath.Join(host, ".act"),
		"config", "user.email", "orch@example.com")
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+filepath.Join(host, ".act"),
		"config", "user.name", "Orch")
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+filepath.Join(host, ".act"),
		"config", "commit.gpgsign", "false")

	// Initial publish: force-push current main to both remotes so
	// the remote-tracking refs are valid baselines for case (h)'s
	// rev-list. The BareRemote fixture seeds itself with a .gitkeep
	// commit on an unrelated history; --force makes the
	// orchestrator's history the canonical one on both bares.
	mustGitIn(t, "", "--git-dir="+actGitDir, "push", "-qf", "origin", "main")
	mustGitIn(t, "", "--git-dir="+actGitDir, "push", "-qf", "origin-upstream", "main")

	// Generate 60 commits on the orchestrator's local main, pushing
	// the run to `origin` but NOT to `origin-upstream` — that's the
	// drift the test exercises. Each commit touches a unique file
	// under .act/.drift/ (NOT .act/ops/, which doctor parses as op
	// JSON files); the case-(h) check counts commits on
	// origin/main, not op-file content, so the path doesn't matter
	// — it just has to avoid the orphan-ops walk.
	driftDir := filepath.Join(host, ".act", ".drift")
	if err := os.MkdirAll(driftDir, 0o755); err != nil {
		t.Fatalf("mkdir drift dir: %v", err)
	}
	const numDriftOps = 60
	for i := 0; i < numDriftOps; i++ {
		name := fmt.Sprintf("drift-%03d.txt", i)
		body := []byte(fmt.Sprintf("i=%d\n", i))
		if err := os.WriteFile(filepath.Join(driftDir, name), body, 0o644); err != nil {
			t.Fatalf("write drift op %d: %v", i, err)
		}
		mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+filepath.Join(host, ".act"),
			"add", filepath.Join(".drift", name))
		mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+filepath.Join(host, ".act"),
			"commit", "-q", "--no-verify", "-m", fmt.Sprintf("drift %d", i))
	}
	// Push the 60-commit run to `origin` only. `origin-upstream` stays
	// at the initial-publish ref — that's the 60-commit drift.
	mustGitIn(t, "", "--git-dir="+actGitDir, "push", "-q", "origin", "main")

	// First doctor pass: case (h) MUST fire. NoFetch=false so doctor
	// populates refs/remotes/* before counting.
	out1, _ := cli.RunDoctor(host, cli.DoctorOptions{})
	dr1, ok := out1.(cli.DoctorResult)
	if !ok {
		t.Fatalf("first doctor: unexpected output type %T (%+v)", out1, out1)
	}
	if !hasCheck(dr1.Findings, cli.CheckUpstreamDrift) {
		t.Fatalf("first doctor: case (h) `%s` not in findings (drift=%d):\n%s",
			cli.CheckUpstreamDrift, dr1.RemoteStatus.UpstreamDriftCommits,
			renderFindings(dr1.Findings))
	}
	if dr1.RemoteStatus.UpstreamDriftCommits < numDriftOps {
		t.Errorf("first doctor: upstream_drift_commits = %d, want >= %d",
			dr1.RemoteStatus.UpstreamDriftCommits, numDriftOps)
	}

	// `act remote sync` to clear the drift. Sync uses the configured
	// origin-upstream URL we wired above.
	syncOut, syncCode := cli.RunRemoteSync(cli.RemoteSyncOptions{SourceCWD: host})
	if syncCode != 0 {
		t.Fatalf("RunRemoteSync: code=%d out=%+v", syncCode, syncOut)
	}
	sr, ok := syncOut.(cli.RemoteSyncResult)
	if !ok {
		t.Fatalf("RunRemoteSync: unexpected output type %T", syncOut)
	}
	if !sr.Pushed {
		t.Errorf("RunRemoteSync: Pushed=false, want true (Logged=%v Reason=%q)",
			sr.Logged, sr.Reason)
	}

	// Second doctor pass: case (h) MUST NOT fire.
	out2, _ := cli.RunDoctor(host, cli.DoctorOptions{})
	dr2, ok := out2.(cli.DoctorResult)
	if !ok {
		t.Fatalf("second doctor: unexpected output type %T", out2)
	}
	if hasCheck(dr2.Findings, cli.CheckUpstreamDrift) {
		t.Errorf("second doctor: case (h) `%s` STILL in findings after sync (drift=%d):\n%s",
			cli.CheckUpstreamDrift, dr2.RemoteStatus.UpstreamDriftCommits,
			renderFindings(dr2.Findings))
	}
	if dr2.RemoteStatus.UpstreamDriftCommits != 0 {
		t.Errorf("second doctor: upstream_drift_commits = %d, want 0",
			dr2.RemoteStatus.UpstreamDriftCommits)
	}
}

// ---------------------------------------------------------------------
// Test 4 — Slow filesystem (AC4).
//
// ACT_TEST_SLOW_COMMIT_MS=2000 forces a 2-second sleep inside the
// nested-git commit path. We do 5 RunCreate calls; each one's commit
// exceeds the 1000ms default slowWriteThresholdMs threshold and an
// entry lands in .act/.slow-writes. Doctor's slow-writes summary
// (status.SlowWritesLastHour) MUST surface 5.
//
// Budget: 5 * 2s = 10s — under the per-test ~1-minute ceiling.
// ---------------------------------------------------------------------

func TestE2E_SlowFilesystem(t *testing.T) {
	// No t.Parallel here: Go's testing package forbids the combination
	// of t.Setenv + t.Parallel (the env var would leak across parallel
	// goroutines via the process-global os.Environ). The slow-write
	// hook is set via t.Setenv, which is the right scope for this
	// test, so we accept the sequential cost — ~10s wall, well under
	// the per-test sub-minute budget.

	// t.Setenv handles cleanup automatically; the env var only
	// affects this test's commits (gitops reads it per-call).
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "2000")

	host := newHostRepo(t)
	if _, code := cli.RunInit(host, false, "machine-slow", "slow@example.com",
		func() time.Time { return time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC) }); code != 0 {
		t.Fatalf("RunInit: code=%d", code)
	}

	const numSlowOps = 5
	for i := 0; i < numSlowOps; i++ {
		out, code := cli.RunCreate(host, cli.CreateOptions{
			Title: fmt.Sprintf("slow op %d", i),
			Type:  "task",
		})
		if code != 0 {
			t.Fatalf("RunCreate %d: code=%d out=%+v", i, code, out)
		}
	}

	// File assertion: .act/.slow-writes has exactly 5 lines.
	slowPath := filepath.Join(host, ".act", ".slow-writes")
	body, err := os.ReadFile(slowPath)
	if err != nil {
		t.Fatalf("read .slow-writes: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != numSlowOps {
		t.Fatalf("slow-writes lines = %d, want %d; body:\n%s", len(lines), numSlowOps, body)
	}

	// Doctor assertion: status.SlowWritesLastHour == 5. We pass
	// NoFetch=true so the case-(g)/(h) probes don't fire (we didn't
	// configure an origin here; no fetch makes the doctor pass
	// hermetic and fast).
	out, _ := cli.RunDoctor(host, cli.DoctorOptions{NoFetch: true})
	dr, ok := out.(cli.DoctorResult)
	if !ok {
		t.Fatalf("doctor: unexpected output type %T (%+v)", out, out)
	}
	if dr.RemoteStatus.SlowWritesLastHour != numSlowOps {
		t.Errorf("doctor slow_writes_last_hour = %d, want %d",
			dr.RemoteStatus.SlowWritesLastHour, numSlowOps)
	}
}

// ---------------------------------------------------------------------
// Test 5 — Dispatch loop (AC5).
//
// Simulates /orchestrate fanning out: orchestrator stands up two
// workers via `act bootstrap-worker --from-remote`, each runs an
// `act create` + `act close` cycle, then pushes its nested .act/.git
// back to the orchestrator. The orchestrator's post-receive hook fires
// `act remote sync` for each push.
//
// The orchestrator's `origin-upstream` is deliberately bogus so each
// background sync fails fast and appends one entry to .act/.sync-log
// — that's the user-visible signal that the chain fired. We require
// at least one entry per worker push (2+ total) within a bounded
// wait window.
//
// Why this shape rather than the literal "bootstrap → create → close"
// chain through the act CLI: the bootstrap-worker --from-remote flow
// expects a clone-able URL pointing at the orchestrator's `.act/.git`;
// raw git push from a working clone gives the same wire-level signal
// (a receive-pack into .act/.git which fires post-receive) with much
// less setup. The AC ("orchestrator's post-receive triggers fire;
// upstream sync log shows both events") is on the post-receive path,
// not the bootstrap-worker mechanism — see the "If stuck" note in
// ticket 11's prompt.
// ---------------------------------------------------------------------

func TestE2E_DispatchLoop(t *testing.T) {
	// No t.Parallel: t.Setenv (PATH override) forbids it. The test's
	// own concurrency primitive is the parallel-workers goroutine
	// pair below, which still exercises the post-receive hook firing
	// for two distinct pushes — the AC the spec names.

	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; TestMain did not run")
	}

	// PATH override: the spawned `act remote sync` (kicked off by
	// the post-receive hook via `nohup act remote sync &`) must
	// resolve to the freshly built test binary, not whatever's on
	// the developer's PATH. This is the same pattern as
	// orchestrator_sync_docclaim_test.go.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(actBinaryPath)+string(os.PathListSeparator)+origPath)

	// ACT_BIN_OVERRIDE: the post-receive hook embeds the absolute
	// path of the binary that ran `act remote enable`. Under
	// `go test`, that path resolves (via os.Executable) to the test
	// binary itself, which does not implement `remote sync` — so
	// the rendered hook would silently no-op and `.sync-log` would
	// never be written. Pointing the seam at the prebuilt act
	// binary makes the rendered hook invoke the real CLI. Same
	// pattern as TestRemoteSync_PostReceiveHookFiresBackgroundSync
	// in internal/cli/remote_sync_test.go.
	t.Setenv("ACT_BIN_OVERRIDE", actBinaryPath)

	// Stand up the orchestrator.
	host := newHostRepo(t)
	if _, code := cli.RunInit(host, false, "machine-orch5", "orch5@example.com",
		func() time.Time { return time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC) }); code != 0 {
		t.Fatalf("RunInit: code=%d", code)
	}
	if _, code := cli.RunRemote(cli.RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("RunRemote enable: code=%d", code)
	}

	actGitDir := filepath.Join(host, ".act", ".git")
	actDir := filepath.Join(host, ".act")

	// Identity + at least one commit on .act/.git so workers can
	// clone something. RunInit already commits; this is belt-and-braces.
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+actDir,
		"config", "user.email", "orch5@example.com")
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+actDir,
		"config", "user.name", "Orch5")
	mustGitIn(t, "", "--git-dir="+actGitDir, "--work-tree="+actDir,
		"config", "commit.gpgsign", "false")
	// Concurrent worker pushes against a non-bare .act/.git serialize
	// through `updateInstead`, which fails its "up-to-date check" when
	// the second arriver finds the working tree mid-update by the first
	// arriver. Production deployments avoid this because the
	// orchestrator's .act/ working tree is owned by act commands (not
	// receive-pack-driven worktree updates). For the test we relax to
	// `ignore` — the ref advances on every receive, post-receive still
	// fires, the working tree drifts but we don't read it. The full
	// `updateInstead` semantics are covered by the existing
	// internal/cli tests; this test's AC is on the hook chain.
	mustGitIn(t, "", "config", "-f",
		filepath.Join(actGitDir, "config"),
		"receive.denyCurrentBranch", "ignore")

	// Bogus origin-upstream so the spawned `act remote sync` fails
	// fast and appends a JSON-line entry to .sync-log. That entry is
	// the user-visible signal that the post-receive chain fired.
	bogus := filepath.Join(t.TempDir(), "unreachable.git")
	mustGitIn(t, "", "config", "-f",
		filepath.Join(actGitDir, "config"),
		"remote.origin-upstream.url", bogus)
	mustGitIn(t, "", "config", "-f",
		filepath.Join(actGitDir, "config"),
		"remote.origin-upstream.fetch", "+refs/heads/*:refs/remotes/origin-upstream/*")

	syncLogPath := filepath.Join(actDir, cli.SyncLogFilename)
	syncLogMtime := func() time.Time {
		info, err := os.Stat(syncLogPath)
		if err != nil {
			return time.Time{}
		}
		return info.ModTime()
	}

	// Two "workers" — each is a clone of the orchestrator's .act/.git
	// that writes a create + close op pair and pushes back. We do the
	// setup work (clone, identity, write ops, commit) in parallel
	// goroutines, but serialize the PUSH step behind a mutex: the
	// post-receive hook spawns `act remote sync` which read-modify-
	// writes `.act/.sync-log` via tmp-file + atomic-rename, with
	// last-writer-wins semantics (documented in slowwrites.go). Two
	// concurrent post-receive fires would race on that rename and
	// collapse to a single visible entry, which would defeat the
	// "both events visible in sync-log" AC for this test. Serializing
	// the pushes preserves both fires' side effects without losing
	// the worker-parallelism the AC also names — each worker's setup
	// runs concurrently with the other.
	const numWorkers = 2
	var pushMu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]error, numWorkers)
	for i := 0; i < numWorkers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerDir := t.TempDir()
			if _, err := runGitIn("", "clone", "-q", actGitDir, workerDir); err != nil {
				errs[i] = fmt.Errorf("worker %d: clone: %w", i, err)
				return
			}
			if _, err := runGitIn(workerDir, "config", "user.email",
				fmt.Sprintf("worker%d@example.com", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: identity: %w", i, err)
				return
			}
			if _, err := runGitIn(workerDir, "config", "user.name",
				fmt.Sprintf("Worker%d", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: identity name: %w", i, err)
				return
			}
			if _, err := runGitIn(workerDir, "config", "commit.gpgsign", "false"); err != nil {
				errs[i] = fmt.Errorf("worker %d: gpg: %w", i, err)
				return
			}
			// One create-like op + one close-like op committed
			// together. Two files, one commit — that's the observable
			// "create + close cycle" from the orchestrator's
			// perspective (one receive-pack carrying both op files).
			opsDir := filepath.Join(workerDir, "ops", "dispatch")
			if err := os.MkdirAll(opsDir, 0o755); err != nil {
				errs[i] = fmt.Errorf("worker %d: mkdir: %w", i, err)
				return
			}
			createPath := filepath.Join(opsDir, fmt.Sprintf("w%d-create.json", i))
			closePath := filepath.Join(opsDir, fmt.Sprintf("w%d-close.json", i))
			if err := os.WriteFile(createPath, []byte(fmt.Sprintf(`{"w":%d,"op":"create"}`, i)), 0o644); err != nil {
				errs[i] = fmt.Errorf("worker %d: write create: %w", i, err)
				return
			}
			if err := os.WriteFile(closePath, []byte(fmt.Sprintf(`{"w":%d,"op":"close"}`, i)), 0o644); err != nil {
				errs[i] = fmt.Errorf("worker %d: write close: %w", i, err)
				return
			}
			if _, err := runGitIn(workerDir, "add", "ops"); err != nil {
				errs[i] = fmt.Errorf("worker %d: add: %w", i, err)
				return
			}
			if _, err := runGitIn(workerDir, "commit", "-q", "--no-verify", "-m",
				fmt.Sprintf("worker %d dispatch cycle", i)); err != nil {
				errs[i] = fmt.Errorf("worker %d: commit: %w", i, err)
				return
			}

			// Serialize the push and wait for the post-receive's
			// background sync to land its entry before releasing the
			// next pusher — see the goroutine-comment rationale above.
			// Use PushWithRetry so the second pusher's non-fast-
			// forward (against the first pusher's already-landed
			// commit) round-trips through fetch-rebase rather than
			// erroring.
			pushMu.Lock()
			defer pushMu.Unlock()
			before := time.Now()
			g := gitops.NewActGitOps(workerDir)
			if err := g.PushWithRetry("main", gitops.PushOpts{}); err != nil {
				errs[i] = fmt.Errorf("worker %d: PushWithRetry: %w", i, err)
				return
			}
			// Bounded wait for the spawned `act remote sync` to land
			// its entry. 3s is generous: the bogus upstream fails
			// immediately (ENOENT on the fixture path), so the sync
			// + log-append finishes well inside this budget.
			waitForSyncLogMtimeAfter(syncLogPath, before, 3*time.Second)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d failed: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}

	// Wait for the post-receive hook chain to fire. Each push runs
	// the hook synchronously on the receive side (the hook itself
	// detaches `act remote sync` via nohup), so by the time push
	// returns, the spawn has started. The background `act remote
	// sync` takes a moment to run; we poll for at least 2 lines in
	// .sync-log within a 5-second budget.
	if !waitForSyncLogLines(syncLogPath, numWorkers, 5*time.Second) {
		// Fall back diagnostic: read whatever's there.
		body, _ := os.ReadFile(syncLogPath)
		t.Fatalf("expected >= %d sync-log entries within 5s; mtime=%v body:\n%s",
			numWorkers, syncLogMtime(), body)
	}

	// Confirm each entry is JSON-shaped with the "reason" field as
	// the first key (the schema invariant from ticket 6a).
	body, err := os.ReadFile(syncLogPath)
	if err != nil {
		t.Fatalf("read sync-log: %v", err)
	}
	lines := nonEmptyLines(string(body))
	if len(lines) < numWorkers {
		t.Fatalf("sync-log: %d lines < %d expected:\n%s", len(lines), numWorkers, body)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, `{"reason":`) {
			t.Errorf("sync-log line %d does not start with `{\"reason\":`: %q", i, line)
		}
		// Sanity: each entry parses as the documented schema.
		var e cli.SyncLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("sync-log line %d: not valid JSON: %v\n%s", i, err, line)
			continue
		}
		if e.Reason == "" {
			t.Errorf("sync-log line %d: empty reason field", i)
		}
	}
}

// ---------------------------------------------------------------------
// Shared helpers (test-scope) used by 3+ of the above tests.
// Single-test helpers are defined inline above.
// ---------------------------------------------------------------------

// newHostRepo creates a tempdir + `git init` + identity + one seed
// commit so RunInit's host-side gitignore writer has somewhere to
// land. Mirrors makeRepo in internal/cli/init_test.go but lives here
// because the integration package is outside cli.
func newHostRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustGitIn(t, root, "init", "-q", "-b", "main")
	configureRepo(t, root, "host@example.com", "Host")
	// Seed commit so any subsequent commit-on-empty doesn't fail
	// under a particularly strict git config (e.g. some CI images).
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("integration fixture\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGitIn(t, root, "add", "README")
	mustGitIn(t, root, "commit", "-q", "--no-verify", "-m", "seed")
	return root
}

// hasCheck reports whether any finding has Check == name.
func hasCheck(findings []cli.Finding, name string) bool {
	for _, f := range findings {
		if f.Check == name {
			return true
		}
	}
	return false
}

// renderFindings is a one-line-per-finding human renderer used in
// failure messages so a future maintainer sees the full doctor state
// when a test trips. Production code uses cli.FormatDoctorHuman; this
// is a minimal version that doesn't depend on the full result envelope.
func renderFindings(findings []cli.Finding) string {
	if len(findings) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "  - %s [%s] %s\n", f.Check, f.Severity, f.Message)
	}
	return b.String()
}

// waitForSyncLogMtimeAfter polls until path's mtime is strictly after
// `after` or `deadline` elapses. Returns true on success. Used by the
// dispatch-loop test to wait for the spawned `act remote sync` to
// flush its log entry before releasing the next pusher — i.e., it's
// the per-fire barrier that lets two serialized pushes BOTH land
// distinct entries in the LWW sync-log.
func waitForSyncLogMtimeAfter(path string, after time.Time, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(after) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// waitForSyncLogLines polls until the file at `path` has at least
// `want` non-empty JSON-lines or `deadline` elapses. Returns true on
// success. 50ms polling interval matches the pattern in
// remote_sync_test.go.
func waitForSyncLogLines(path string, want int, deadline time.Duration) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		data, err := os.ReadFile(path)
		if err == nil {
			if len(nonEmptyLines(string(data))) >= want {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// nonEmptyLines splits s on '\n' and drops empty / whitespace-only
// lines. Used by the dispatch-loop test to count JSON-lines in
// .sync-log without re-implementing the same filter inline.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
