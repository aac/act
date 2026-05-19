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
//	8. TestDocClaim_Harvest_HelpListsSubcommand — registry-tracked
//	   doc-claim test that `act help` lists the harvest subcommand.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// TestDocClaim_Harvest_HelpListsSubcommand is the registry-tracked
// doc-claim test. The claim: `act help` includes the harvest subcommand
// listing line ("act harvest <worker-path>") so a cold-start agent can
// discover the surface. docs_sweep_test.go has the matching registry
// entry.
func TestDocClaim_Harvest_HelpListsSubcommand(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "act harvest <worker-path>") {
		t.Errorf("act help missing the canonical harvest invocation line:\n%s", out)
	}
	// The --help-equivalent usage line emitted on a missing positional
	// must also name the subcommand and the documented flags so an
	// agent running `act harvest` blind gets actionable text.
	_, stderr, _ := runAct(t, site, "harvest")
	if !strings.Contains(stderr, "harvest") || !strings.Contains(stderr, "--dry-run") {
		t.Errorf("harvest usage message missing required parts:\n%s", stderr)
	}
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

// Silence unused-import warnings if io is later removed from the
// happy-path comparison helper.
var _ = io.EOF
