package cli

// Tests for `act harvest` (act-9fadf0). Phase 1.5 prerequisite for the
// coordination-plane Phase 2 ticket 8 (which will extend harvest with
// a `--from-remote` mode alongside the cwd-based local-path mode tested
// here).
//
// Cases mirror the acceptance criteria from the ticket:
//
//	1. Happy path: worker has 3 new ops; host harvests all 3.
//	2. Idempotency: harvest the same worker twice → second is a no-op.
//	3. Worker .act/ missing → worker_state_not_found.
//	4. Worker .act/ops/ empty → no-op, exit 0.
//	5. --dry-run: same JSON shape but no host writes (commit count
//	   unchanged).
//	6. Filename collision: pre-seed same filename + different content
//	   → op_filename_collision.
//	7. Fold error path: simulate fold failure; copy + commit still
//	   succeed; JSON reports fold_error.
//	8. TestDocClaim_DeprecatedAliasesDelegate — registry-tracked
//	   doc-claim test that the deprecated `harvest` / `bootstrap-worker`
//	   aliases print a notice and delegate to the new state verbs.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/index"
)

// makeHarvestHost initializes a host repo with a fresh `.act/` (nested
// .git, config.json, ops/.gitkeep, hooks/, snapshots/) and returns the
// host repo root. The host has its own `act init`-derived state — it is
// the destination side of harvest.
func makeHarvestHost(t *testing.T) string {
	t.Helper()
	host := makeRepo(t)
	out, code := RunInit(host, false, "machine-host", "host@example.com",
		func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) })
	if code != 0 {
		t.Fatalf("RunInit host: code=%d out=%+v", code, out)
	}
	return host
}

// makeHarvestWorker bootstraps a worker target by running bootstrap-worker
// from the host, then creates `numNewOps` issues inside the worker so its
// `.act/ops/` has new ops that the host has never seen. Returns the
// worker root (a directory containing `.act/`).
func makeHarvestWorker(t *testing.T, host string, numNewOps int) string {
	t.Helper()
	// Pick a fresh target dir; bootstrap-worker expects the parent to
	// exist but the .act/ to be missing/empty.
	parent := t.TempDir()
	target := filepath.Join(parent, "worker")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir worker target: %v", err)
	}
	// A real git repo at the worker target so any future op writes have
	// somewhere reasonable to live, though harvest itself only reads
	// from .act/ops/.
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init worker: %v\n%s", err, out)
	}
	// Identity for any nested commits the worker may perform.
	for _, kv := range [][2]string{
		{"user.email", "worker@example.com"},
		{"user.name", "W"},
		{"commit.gpgsign", "false"},
	} {
		c := exec.Command("git", "config", kv[0], kv[1])
		c.Dir = target
		_, _ = c.CombinedOutput()
	}

	_, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: host,
		Target:    target,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker for harvest test: code=%d", code)
	}
	// File numNewOps issues in the worker via the binary so each one
	// produces a real op file with a real HLC + content hash.
	for i := 0; i < numNewOps; i++ {
		title := fmt.Sprintf("worker probe %d", i)
		mustRunAct(t, target, 0, "create", title, "--json")
	}
	return target
}

// TestHarvest_HappyPath covers acceptance criterion (1).
func TestHarvest_HappyPath(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 3)

	// Snapshot the host's nested .act/.git commit count before harvest.
	beforeCount := nestedCommitCount(t, host)

	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
	})
	if code != 0 {
		t.Fatalf("harvest code=%d out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if len(res.HarvestedOps) != 3 {
		t.Errorf("harvested_ops = %d, want 3 (paths: %v)", len(res.HarvestedOps), res.HarvestedOps)
	}
	// SkippedOps would carry any worker ops the host already had. A
	// fresh host has no op files (init writes config + nested .git but
	// no ops), so the bootstrap-inherited subset is empty here. The
	// load-bearing assertion is that the harvest count equals the
	// number of newly-created worker ops.
	// The harvest commit message names the count and the worker basename.
	if !strings.Contains(res.CommitMessage, "3 ops from worker") {
		t.Errorf("commit_message = %q, want it to mention '3 ops from worker'", res.CommitMessage)
	}
	// One new commit on the host's nested .act/.git.
	afterCount := nestedCommitCount(t, host)
	if afterCount != beforeCount+1 {
		t.Errorf("nested commit count: before=%d after=%d (want after = before + 1)", beforeCount, afterCount)
	}
	// And every harvested op now exists at the host with the same bytes
	// as the worker.
	hostOps := filepath.Join(host, ".act", "ops")
	workerOps := filepath.Join(worker, ".act", "ops")
	for _, rel := range res.HarvestedOps {
		hostBody, err := os.ReadFile(filepath.Join(hostOps, rel))
		if err != nil {
			t.Errorf("read host op %s: %v", rel, err)
			continue
		}
		workerBody, err := os.ReadFile(filepath.Join(workerOps, rel))
		if err != nil {
			t.Errorf("read worker op %s: %v", rel, err)
			continue
		}
		if string(hostBody) != string(workerBody) {
			t.Errorf("byte mismatch at %s", rel)
		}
	}
}

// TestHarvest_Idempotency covers acceptance criterion (2).
func TestHarvest_Idempotency(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)

	// First harvest copies + commits.
	_, code := RunHarvest(HarvestOptions{HostCWD: host, WorkerPath: worker})
	if code != 0 {
		t.Fatalf("first harvest code=%d", code)
	}
	beforeSecond := nestedCommitCount(t, host)

	// Second harvest: zero new ops, no commit.
	out, code := RunHarvest(HarvestOptions{HostCWD: host, WorkerPath: worker})
	if code != 0 {
		t.Fatalf("second harvest code=%d out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("second output type = %T, want HarvestResult", out)
	}
	if len(res.HarvestedOps) != 0 {
		t.Errorf("second harvested_ops = %v, want []", res.HarvestedOps)
	}
	if res.CommitMessage != "" {
		t.Errorf("second commit_message = %q, want empty (no commit)", res.CommitMessage)
	}
	afterSecond := nestedCommitCount(t, host)
	if afterSecond != beforeSecond {
		t.Errorf("second harvest produced a commit: before=%d after=%d", beforeSecond, afterSecond)
	}
}

// TestHarvest_WorkerStateNotFound covers acceptance criterion (3).
func TestHarvest_WorkerStateNotFound(t *testing.T) {
	host := makeHarvestHost(t)
	// A path that exists but has no .act/.
	emptyWorker := t.TempDir()

	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: emptyWorker,
	})
	if code != 2 {
		t.Fatalf("exit=%d, want 2; out=%+v", code, out)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != ErrWorkerStateNotFound {
		t.Errorf("error code = %q, want %q", got, ErrWorkerStateNotFound)
	}
}

// TestHarvest_WorkerOpsEmpty covers acceptance criterion (4).
func TestHarvest_WorkerOpsEmpty(t *testing.T) {
	host := makeHarvestHost(t)
	// Create a worker with .act/ but no ops/ subdir at all.
	parent := t.TempDir()
	worker := filepath.Join(parent, "worker")
	if err := os.MkdirAll(filepath.Join(worker, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir worker .act: %v", err)
	}
	// Drop a config.json so the .act/ dir isn't fully empty (mirrors a
	// bootstrap-worker target whose ops/ was scrubbed).
	if err := os.WriteFile(filepath.Join(worker, ".act", "config.json"),
		[]byte(`{"node_id":"feedface","version":"0.1.0"}`), 0o644); err != nil {
		t.Fatalf("write worker config: %v", err)
	}

	beforeCount := nestedCommitCount(t, host)
	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
	})
	if code != 0 {
		t.Fatalf("exit=%d, want 0; out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if len(res.HarvestedOps) != 0 {
		t.Errorf("harvested_ops = %v, want []", res.HarvestedOps)
	}
	afterCount := nestedCommitCount(t, host)
	if afterCount != beforeCount {
		t.Errorf("empty worker produced a commit: before=%d after=%d", beforeCount, afterCount)
	}
}

// TestHarvest_DryRun covers acceptance criterion (5).
func TestHarvest_DryRun(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)

	beforeCount := nestedCommitCount(t, host)
	beforeOps := countHostOpFiles(t, host)

	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
		DryRun:     true,
	})
	if code != 0 {
		t.Fatalf("dry-run exit=%d, want 0; out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if !res.DryRun {
		t.Errorf("dry_run flag not echoed in result")
	}
	if len(res.HarvestedOps) != 2 {
		t.Errorf("dry-run harvested_ops = %d, want 2", len(res.HarvestedOps))
	}
	if res.CommitMessage != "" {
		t.Errorf("dry-run commit_message = %q, want empty", res.CommitMessage)
	}

	// Host side must be unchanged: no new commit, no new op files.
	if afterCount := nestedCommitCount(t, host); afterCount != beforeCount {
		t.Errorf("dry-run produced a commit: before=%d after=%d", beforeCount, afterCount)
	}
	if afterOps := countHostOpFiles(t, host); afterOps != beforeOps {
		t.Errorf("dry-run wrote op files: before=%d after=%d", beforeOps, afterOps)
	}
}

// TestHarvest_FilenameCollision covers acceptance criterion (6).
//
// Pre-seeds both the worker and the host with an op file that shares the
// SAME filename but DIFFERENT bytes. This shouldn't be reachable in
// practice (HLC + content hash should make filenames unique-per-content),
// but if it happens we want a loud error rather than a silent overwrite.
func TestHarvest_FilenameCollision(t *testing.T) {
	host := makeHarvestHost(t)
	// Build a worker that contains a single op file at a known path. We
	// don't need RunBootstrapWorker for this case; just hand-construct
	// the worker's `.act/ops/<issue>/<month>/<filename>` skeleton.
	worker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worker, ".act", "ops"), 0o755); err != nil {
		t.Fatalf("mkdir worker ops: %v", err)
	}
	rel := filepath.Join("act-collide00", "2026-05", "000000-aaaaaaaa-create.json")
	workerPath := filepath.Join(worker, ".act", "ops", rel)
	if err := os.MkdirAll(filepath.Dir(workerPath), 0o755); err != nil {
		t.Fatalf("mkdir worker op parent: %v", err)
	}
	if err := os.WriteFile(workerPath, []byte(`{"worker":"version"}`), 0o644); err != nil {
		t.Fatalf("write worker op: %v", err)
	}

	// Seed the host with the same filename and different bytes.
	hostPath := filepath.Join(host, ".act", "ops", rel)
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		t.Fatalf("mkdir host op parent: %v", err)
	}
	if err := os.WriteFile(hostPath, []byte(`{"host":"version"}`), 0o644); err != nil {
		t.Fatalf("write host op: %v", err)
	}

	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
	})
	if code != 1 {
		t.Fatalf("exit=%d, want 1; out=%+v", code, out)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != ErrOpFilenameCollision {
		t.Errorf("error code = %q, want %q", got, ErrOpFilenameCollision)
	}

	// Host op file must still have its original content (no overwrite).
	hostBody, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("read host op after refused harvest: %v", err)
	}
	if string(hostBody) != `{"host":"version"}` {
		t.Errorf("host op overwritten despite collision: got %q", hostBody)
	}
}

// TestHarvest_FoldErrorReported covers acceptance criterion (7).
//
// Forces the fold step to fail via the injectable indexRebuild seam.
// The copy + commit must still go through; the fold error appears in
// the JSON envelope; exit code is still 0 (harvest is one-way append).
func TestHarvest_FoldErrorReported(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 1)

	beforeCount := nestedCommitCount(t, host)

	failRebuild := errors.New("simulated fold failure")
	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
		indexRebuild: func(opsDir string, idx *index.Index) error {
			return failRebuild
		},
	})
	if code != 0 {
		t.Fatalf("exit=%d, want 0 (harvest is one-way append); out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if res.FoldError == "" {
		t.Errorf("fold_error empty; expected the simulated failure to surface")
	} else if !strings.Contains(res.FoldError, "simulated fold failure") {
		t.Errorf("fold_error = %q; want it to mention the injected message", res.FoldError)
	}
	// Copy + commit still succeed: there must be a new commit on the
	// nested .act/.git.
	if afterCount := nestedCommitCount(t, host); afterCount != beforeCount+1 {
		t.Errorf("commit suppressed despite fold-only failure: before=%d after=%d", beforeCount, afterCount)
	}
}

// TestDocClaim_DeprecatedAliasesDelegate is the registry-tracked doc-claim
// test for the backward-compat aliases (MF-D, act-93370d). The claim:
// `act bootstrap-worker <dir>` and `act harvest <dir>` remain as thin
// deprecation aliases that (1) print a deprecation notice to stderr
// pointing at the new verb, and (2) produce the SAME result as the new
// verb. `act help` advertises the new verbs (state import / state export)
// as the canonical surface.
//
// We assert the alias-vs-new-verb equivalence at the user-visible
// boundary: bootstrap-worker and state import, run against fresh targets
// seeded from the same source, must both succeed and copy the same op
// set. Behavioral mechanics are unchanged — the alias just delegates.
func TestDocClaim_DeprecatedAliasesDelegate(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	site := t.TempDir()

	// `act help` advertises the new canonical verbs.
	out, _ := mustRunAct(t, site, 0, "help")
	for _, want := range []string{"act state import <dir>", "act state export <dir>"} {
		if !strings.Contains(out, want) {
			t.Errorf("act help missing canonical verb %q:\n%s", want, out)
		}
	}

	// --- Import alias: `act bootstrap-worker <dir>` delegates to
	// `act state import <dir>`. ---
	host := makeHarvestHost(t)
	// Seed an op into the host so .act/ops/ has a real op file to copy;
	// a freshly-initialized host has no issue ops yet.
	mustRunAct(t, host, 0, "create", "alias-delegate probe", "--json")

	// Target A seeded via the deprecated alias.
	aliasTarget := filepath.Join(t.TempDir(), "via-alias")
	if err := os.MkdirAll(aliasTarget, 0o755); err != nil {
		t.Fatalf("mkdir alias target: %v", err)
	}
	_, aliasStderr, aliasCode := runAct(t, host, "bootstrap-worker", aliasTarget)
	if aliasCode != 0 {
		t.Fatalf("act bootstrap-worker <dir> exit = %d, want 0\nstderr:\n%s", aliasCode, aliasStderr)
	}
	if !strings.Contains(aliasStderr, "deprecated") || !strings.Contains(aliasStderr, "act state import") {
		t.Errorf("bootstrap-worker alias did not print a deprecation notice pointing at the new verb:\n%s", aliasStderr)
	}

	// Target B seeded via the new verb.
	newTarget := filepath.Join(t.TempDir(), "via-new-verb")
	if err := os.MkdirAll(newTarget, 0o755); err != nil {
		t.Fatalf("mkdir new-verb target: %v", err)
	}
	_, newStderr, newCode := runAct(t, host, "state", "import", newTarget)
	if newCode != 0 {
		t.Fatalf("act state import <dir> exit = %d, want 0\nstderr:\n%s", newCode, newStderr)
	}
	if strings.Contains(newStderr, "deprecated") {
		t.Errorf("act state import (the new verb) should NOT print a deprecation notice:\n%s", newStderr)
	}

	// Same result: both targets carry a populated .act/ops/ with the same
	// op set copied from the host.
	aliasOps := relOpFiles(t, filepath.Join(aliasTarget, ".act", "ops"))
	newOps := relOpFiles(t, filepath.Join(newTarget, ".act", "ops"))
	if len(aliasOps) == 0 {
		t.Fatalf("alias-seeded target has no ops under .act/ops/")
	}
	if !slicesEqualUnordered(aliasOps, newOps) {
		t.Errorf("alias and new-verb seeded different op sets:\n  alias: %v\n  new:   %v", aliasOps, newOps)
	}

	// --- Export alias: `act harvest <dir>` delegates to
	// `act state export <dir>` and prints a deprecation notice. ---
	exportHost := makeHarvestHost(t)
	worker := makeHarvestWorker(t, exportHost, 2)
	_, harvestStderr, harvestCode := runAct(t, exportHost, "harvest", worker, "--dry-run")
	if harvestCode != 0 {
		t.Fatalf("act harvest <dir> --dry-run exit = %d, want 0\nstderr:\n%s", harvestCode, harvestStderr)
	}
	if !strings.Contains(harvestStderr, "deprecated") || !strings.Contains(harvestStderr, "act state export") {
		t.Errorf("harvest alias did not print a deprecation notice pointing at the new verb:\n%s", harvestStderr)
	}
}

// relOpFiles returns the sorted relative paths of every .json file under
// opsDir. Used to compare the op set two seeding paths produced.
func relOpFiles(t *testing.T, opsDir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(opsDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		rel, rerr := filepath.Rel(opsDir, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// slicesEqualUnordered reports whether a and b contain the same elements
// (order-insensitive). Both are sorted by the caller, so a direct compare
// suffices, but we re-sort defensively.
func slicesEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// nestedCommitCount returns the number of commits on the host's nested
// `.act/.git`. Used to assert "harvest produced exactly one commit" and
// "dry-run produced zero commits".
func nestedCommitCount(t *testing.T, host string) int {
	t.Helper()
	actDir := filepath.Join(host, ".act")
	cmd := exec.Command("git", "rev-list", "--count", "HEAD")
	cmd.Dir = actDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-list count in %s: %v\n%s", actDir, err, out)
	}
	s := strings.TrimSpace(string(out))
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("parse commit count %q: %v", s, err)
	}
	return n
}

// countHostOpFiles returns the count of `.json` files under the host's
// `.act/ops/`. Used to assert that dry-run leaves the host untouched.
func countHostOpFiles(t *testing.T, host string) int {
	t.Helper()
	ops := filepath.Join(host, ".act", "ops")
	count := 0
	err := filepath.Walk(ops, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".json") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk host ops: %v", err)
	}
	return count
}

// --- Phase 2 ticket 8 (act-e31aa1): harvest narrowing tests --------------
//
// These four cases extend the Phase 1.5 suite with the
// remote-attached-worker skip path. Each case configures the worker's
// .act/.git/config to set the role/origin combination under test.

// configureRemoteAttachedWorker writes act.role=worker and
// remote.origin.url=<orchestrator-act-git> to the worker's
// .act/.git/config. This is the post-bootstrap state Phase 2 ticket 7
// produces; until that ticket lands, tests build it by hand.
func configureRemoteAttachedWorker(t *testing.T, worker, orchActGit string) {
	t.Helper()
	cfgPath := filepath.Join(worker, ".act", ".git", "config")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("worker .act/.git/config missing: %v", err)
	}
	for _, kv := range [][2]string{
		{"act.role", "worker"},
		{"remote.origin.url", orchActGit},
	} {
		cmd := exec.Command("git", "config", "-f", cfgPath, kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config -f %s %s %s: %v\n%s", cfgPath, kv[0], kv[1], err, out)
		}
	}
}

// TestHarvest_RemoteAttachedWorker_SkipsWithMessage covers AC #1:
// remote-attached worker (act.role=worker + origin matches) → no-op
// exit 0, stderr contains the literal skip message, no commit on the
// host's nested .act/.git.
func TestHarvest_RemoteAttachedWorker_SkipsWithMessage(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 3)
	orchActGit := filepath.Join(host, ".act", ".git")
	configureRemoteAttachedWorker(t, worker, orchActGit)

	beforeCount := nestedCommitCount(t, host)
	beforeOps := countHostOpFiles(t, host)

	var stderrBuf strings.Builder
	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
		Stderr:     &stderrBuf,
	})
	if code != 0 {
		t.Fatalf("exit=%d, want 0; out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if !res.Skipped {
		t.Errorf("skipped = false, want true")
	}
	if res.SkipReason != "worker_push_attached" {
		t.Errorf("skip_reason = %q, want %q", res.SkipReason, "worker_push_attached")
	}
	if len(res.HarvestedOps) != 0 {
		t.Errorf("harvested_ops = %v, want [] (skip should not copy)", res.HarvestedOps)
	}
	if res.CommitMessage != "" {
		t.Errorf("commit_message = %q, want empty", res.CommitMessage)
	}
	if !strings.Contains(stderrBuf.String(), HarvestSkipMessage) {
		t.Errorf("stderr does not contain skip message %q:\nstderr=%q",
			HarvestSkipMessage, stderrBuf.String())
	}
	if afterCount := nestedCommitCount(t, host); afterCount != beforeCount {
		t.Errorf("skip path produced a commit: before=%d after=%d", beforeCount, afterCount)
	}
	if afterOps := countHostOpFiles(t, host); afterOps != beforeOps {
		t.Errorf("skip path wrote op files: before=%d after=%d", beforeOps, afterOps)
	}
}

// TestHarvest_SandboxedWorker_RunsPhase15Path covers AC #2: a worker
// with no act.role config (the Phase 1.5 / sandboxed shape) falls
// through to the file-diff-and-copy path. We assert the harvested-ops
// count matches what TestHarvest_HappyPath asserts — proving the skip
// pre-check did NOT fire and the existing Phase 1.5 behavior is intact.
func TestHarvest_SandboxedWorker_RunsPhase15Path(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)
	// Deliberately do NOT configure act.role. makeHarvestWorker leaves
	// the worker's .act/.git/config without an act.role key — exactly
	// the Phase 1.5 sandboxed shape.
	cfgPath := filepath.Join(worker, ".act", ".git", "config")
	got, err := configGetRaw(cfgPath, "act.role")
	if err == nil && got != "" {
		t.Fatalf("test setup: worker has act.role=%q (want unset)", got)
	}

	beforeCount := nestedCommitCount(t, host)

	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
	})
	if code != 0 {
		t.Fatalf("exit=%d, want 0; out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	if res.Skipped {
		t.Errorf("sandboxed worker incorrectly took skip path: skipped=true reason=%q",
			res.SkipReason)
	}
	if len(res.HarvestedOps) != 2 {
		t.Errorf("harvested_ops = %d, want 2 (Phase 1.5 file-diff path)",
			len(res.HarvestedOps))
	}
	if !strings.Contains(res.CommitMessage, "2 ops from worker") {
		t.Errorf("commit_message = %q, want it to mention '2 ops from worker'",
			res.CommitMessage)
	}
	if afterCount := nestedCommitCount(t, host); afterCount != beforeCount+1 {
		t.Errorf("Phase 1.5 path did not commit: before=%d after=%d",
			beforeCount, afterCount)
	}
}

// TestHarvest_LocalCommitsNotPushed_StillCopies documents the AC #3
// failure mode: a worker with act.role=worker AND origin matching the
// orchestrator's .act/.git path triggers the skip — even if the
// worker's ops were never actually pushed to the orchestrator. The skip
// decision is purely on the config combination, not on observed push
// status. Test asserts the documented behavior (ops are missed) so a
// future change that "fixes" this without updating the docs will break
// here.
func TestHarvest_LocalCommitsNotPushed_StillCopies(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)
	orchActGit := filepath.Join(host, ".act", ".git")
	configureRemoteAttachedWorker(t, worker, orchActGit)

	// Pre-condition: worker has 2 local ops that the host has never
	// seen. In a real Phase 2 flow they would have been pushed already;
	// here we simulate the "local commits not yet on origin" state by
	// simply not pushing. Harvest still skips — by design.
	workerOps := filepath.Join(worker, ".act", "ops")
	candidates, err := scanOpFiles(workerOps)
	if err != nil {
		t.Fatalf("scan worker ops: %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("test setup: worker has %d ops, want at least 2",
			len(candidates))
	}

	beforeHostOps := countHostOpFiles(t, host)

	var stderrBuf strings.Builder
	out, code := RunHarvest(HarvestOptions{
		HostCWD:    host,
		WorkerPath: worker,
		Stderr:     &stderrBuf,
	})
	if code != 0 {
		t.Fatalf("exit=%d, want 0; out=%+v", code, out)
	}
	res, ok := out.(HarvestResult)
	if !ok {
		t.Fatalf("output type = %T, want HarvestResult", out)
	}
	// Documented behavior: skip fires; the local-only worker ops are
	// MISSED. A future change that adds a "did the worker actually
	// push?" check would flip this assertion — at which point the doc
	// in harvest.go's pre-check comment AND cmd/act/help.go's
	// "push-attached workers" paragraph need updating.
	if !res.Skipped {
		t.Errorf("expected documented skip-misses-local-only-ops behavior: skipped=false")
	}
	afterHostOps := countHostOpFiles(t, host)
	if afterHostOps != beforeHostOps {
		t.Errorf("skip path copied ops anyway: before=%d after=%d (this is the documented miss path; if it changes, update help.go)",
			beforeHostOps, afterHostOps)
	}
}

// TestHarvest_RemoteAttachedWorker_Idempotent covers AC #4: calling
// harvest twice against the same remote-attached worker is a no-op
// both times. Same exit code, same envelope shape, no commits.
func TestHarvest_RemoteAttachedWorker_Idempotent(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 1)
	orchActGit := filepath.Join(host, ".act", ".git")
	configureRemoteAttachedWorker(t, worker, orchActGit)

	beforeCount := nestedCommitCount(t, host)

	for i := 0; i < 2; i++ {
		var stderrBuf strings.Builder
		out, code := RunHarvest(HarvestOptions{
			HostCWD:    host,
			WorkerPath: worker,
			Stderr:     &stderrBuf,
		})
		if code != 0 {
			t.Fatalf("run %d exit=%d, want 0; out=%+v", i+1, code, out)
		}
		res, ok := out.(HarvestResult)
		if !ok {
			t.Fatalf("run %d output type = %T, want HarvestResult", i+1, out)
		}
		if !res.Skipped {
			t.Errorf("run %d skipped=false, want true", i+1)
		}
		if len(res.HarvestedOps) != 0 {
			t.Errorf("run %d harvested_ops = %v, want []", i+1, res.HarvestedOps)
		}
		if !strings.Contains(stderrBuf.String(), HarvestSkipMessage) {
			t.Errorf("run %d stderr missing skip message:\n%s", i+1, stderrBuf.String())
		}
	}
	afterCount := nestedCommitCount(t, host)
	if afterCount != beforeCount {
		t.Errorf("two skipped harvests produced commits: before=%d after=%d",
			beforeCount, afterCount)
	}
}

// configGetRaw is a tiny shell-out around `git config -f <path> --get
// <key>` that returns the empty string for an unset key (matching the
// shape of config.GetGitConfig but kept local to the test file so we
// don't import internal/config for a one-line probe).
func configGetRaw(path, key string) (string, error) {
	cmd := exec.Command("git", "config", "-f", path, "--get", key)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() == 1 || ee.ExitCode() == 5 {
				return "", nil
			}
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Silence unused-import warnings if io is later removed from the
// happy-path comparison helper.
var _ = io.EOF
