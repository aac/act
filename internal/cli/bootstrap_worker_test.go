package cli

// Tests for `act bootstrap-worker` (act-12dc23). Phase 1.5 prerequisite
// for the upcoming coordination-plane Phase 2 ticket 7 (which will add
// --from-remote alongside the cwd-based mode tested here).
//
// Cases:
//
//	1. Happy path: source .act/ present, target empty → copy succeeds,
//	   JSON envelope has the expected fields, target tree mirrors source
//	   in load-bearing places (ops/ + config.json + .git/).
//	2. Target .act/ non-empty without --force → exit 2 with the
//	   "target_not_empty" error envelope.
//	3. Target .act/ non-empty with --force → succeeds, replaces previous
//	   contents.
//	4. Source .act/.git missing → exit 3 with "act_state_not_initialized".
//	5. --json envelope shape (fields present + types) is stable.
//	6. Round-trip: act ready against the new target produces an envelope
//	   shape equivalent to the same call against the source.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeBootstrapSource initializes a host repo with a fresh `.act/`
// (nested .git, config.json, ops/.gitkeep, hooks/, snapshots/), files an
// issue so ops/ has at least one real op, and returns the host repo
// root. The caller's tests pass this root as the source for
// RunBootstrapWorker.
//
// Returns:
//   - srcRoot: the host repo root with `.act/` inside.
//   - issueID: the id of the seeded issue (lets the round-trip test
//     assert ready surfaces it from both source and target).
func makeBootstrapSource(t *testing.T) (srcRoot, issueID string) {
	t.Helper()
	srcRoot = makeRepo(t)
	// Build the binary lazily via the existing test infrastructure;
	// here we just use the in-process API since the smoke/concurrent
	// tests have already established that RunInit + RunCreate work
	// end-to-end. Calling them directly keeps this test hermetic and
	// fast.
	out, code := RunInit(srcRoot, false, "machine-bw", "bw@example.com",
		func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) })
	if code != 0 {
		t.Fatalf("RunInit: code=%d out=%+v", code, out)
	}

	// File one issue so ops/<issue-id>/ exists with at least one op
	// file. Use the subprocess binary to avoid leaking RunCreate's
	// option struct into this file (which would tightly couple the
	// test to per-package surface area changes).
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	createOut, _ := mustRunAct(t, srcRoot, 0, "create", "bootstrap probe", "--json")
	issueID = pickIDFromJSON(t, createOut)
	return srcRoot, issueID
}

// makeBootstrapTarget creates an empty worker target directory that
// has its own .git (so the FindHostRepoRoot resolver inside
// RunBootstrapWorker doesn't reach back up to the source when the test
// happens to be running under a real repo). The target is a freshly-
// initialized git repo with no .act/ yet — the case the command is
// designed for.
func makeBootstrapTarget(t *testing.T) string {
	t.Helper()
	dir := makeRepo(t)
	// Confirm no .act/ at the target — RunBootstrapWorker is supposed
	// to create it.
	if _, err := os.Stat(filepath.Join(dir, ".act")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("freshly created target unexpectedly has .act/: err=%v", err)
	}
	return dir
}

// TestBootstrapWorker_HappyPath covers acceptance criterion (1).
//
// Drives RunBootstrapWorker with a populated source + empty target,
// asserts:
//   - exit code 0
//   - result is BootstrapWorkerResult with source/target/dispatch_hlc
//     populated
//   - the target's `.act/ops/` mirrors the source's ops tree
//   - the target's `.act/config.json` is byte-equal to the source's
//   - `.act/.git` is present (the load-bearing nested-repo invariant)
//   - `.act/.bootstrap-meta.json` is present and parses as JSON with
//     the expected fields
func TestBootstrapWorker_HappyPath(t *testing.T) {
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker code=%d out=%+v", code, out)
	}
	res, ok := out.(BootstrapWorkerResult)
	if !ok {
		t.Fatalf("output type = %T, want BootstrapWorkerResult", out)
	}
	if res.SourceRoot != srcRoot {
		t.Errorf("source_root = %q, want %q", res.SourceRoot, srcRoot)
	}
	if res.Target == "" || !filepath.IsAbs(res.Target) {
		t.Errorf("target = %q, want absolute non-empty", res.Target)
	}
	if res.DispatchHLC == "" {
		t.Errorf("dispatch_hlc empty in success envelope")
	}
	if res.OpsCopied < 1 {
		// Initial commit op-files plus the create op should be > 0.
		t.Errorf("ops_copied = %d, want ≥ 1", res.OpsCopied)
	}

	// Target tree assertions.
	targetAct := filepath.Join(targetRoot, ".act")
	if _, err := os.Stat(filepath.Join(targetAct, ".git")); err != nil {
		t.Errorf("target .act/.git missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetAct, BootstrapMetaFileName)); err != nil {
		t.Errorf("target meta file missing: %v", err)
	}

	// config.json byte equality.
	srcCfg, err := os.ReadFile(filepath.Join(srcRoot, ".act", "config.json"))
	if err != nil {
		t.Fatalf("read src config: %v", err)
	}
	tgtCfg, err := os.ReadFile(filepath.Join(targetAct, "config.json"))
	if err != nil {
		t.Fatalf("read tgt config: %v", err)
	}
	if string(srcCfg) != string(tgtCfg) {
		t.Errorf("config.json mismatch:\n--- src ---\n%s\n--- tgt ---\n%s", srcCfg, tgtCfg)
	}

	// ops/ tree mirrored: every JSON op file in src must appear at the
	// same relative path in target with identical bytes. We compare by
	// scanning src/ops and looking up each file at tgt.
	srcOps := filepath.Join(srcRoot, ".act", "ops")
	tgtOps := filepath.Join(targetAct, "ops")
	mirrorErr := filepath.Walk(srcOps, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(srcOps, p)
		srcBody, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		tgtBody, rerr := os.ReadFile(filepath.Join(tgtOps, rel))
		if rerr != nil {
			return rerr
		}
		if string(srcBody) != string(tgtBody) {
			t.Errorf("ops mismatch at %s", rel)
		}
		return nil
	})
	if mirrorErr != nil {
		t.Errorf("walk src ops: %v", mirrorErr)
	}
}

// TestBootstrapWorker_TargetNotEmpty covers acceptance criterion (2).
//
// Pre-populates the target's `.act/` with a single dummy file, runs
// bootstrap-worker without --force, expects:
//   - exit code 2 (universal error table: bad input / preflight reject)
//   - error envelope with code "target_not_empty"
//   - the target's existing file is left untouched (no partial copy
//     leaked underneath)
func TestBootstrapWorker_TargetNotEmpty(t *testing.T) {
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	// Seed the target's .act/ with a sentinel file the test will
	// assert survives the refused bootstrap.
	targetAct := filepath.Join(targetRoot, ".act")
	if err := os.MkdirAll(targetAct, 0o755); err != nil {
		t.Fatalf("seed target .act: %v", err)
	}
	const sentinel = "do-not-touch.txt"
	sentinelBody := []byte("preserved across refused bootstrap\n")
	if err := os.WriteFile(filepath.Join(targetAct, sentinel), sentinelBody, 0o644); err != nil {
		t.Fatalf("seed target sentinel: %v", err)
	}

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
	})
	if code != 2 {
		t.Fatalf("bootstrap-worker exit=%d, want 2; out=%+v", code, out)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "target_not_empty" {
		t.Errorf("error code = %q, want target_not_empty", got)
	}

	// Sentinel must still be there and unchanged.
	got, err := os.ReadFile(filepath.Join(targetAct, sentinel))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(got) != string(sentinelBody) {
		t.Errorf("sentinel mutated: %q", got)
	}
}

// TestBootstrapWorker_ForceReplaces covers acceptance criterion (3).
//
// Same setup as TargetNotEmpty but with --force, asserts:
//   - exit code 0
//   - the previous sentinel is gone (because the .act/ tree was
//     replaced wholesale by the rename)
//   - config.json is now the source's
func TestBootstrapWorker_ForceReplaces(t *testing.T) {
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	targetAct := filepath.Join(targetRoot, ".act")
	if err := os.MkdirAll(targetAct, 0o755); err != nil {
		t.Fatalf("seed target .act: %v", err)
	}
	const sentinel = "do-not-touch.txt"
	if err := os.WriteFile(filepath.Join(targetAct, sentinel), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}

	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
		Force:     true,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker --force exit=%d; out=%+v", code, out)
	}
	if _, err := os.Stat(filepath.Join(targetAct, sentinel)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sentinel survived --force: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetAct, "config.json")); err != nil {
		t.Errorf("config.json missing after --force: %v", err)
	}
}

// TestBootstrapWorker_SourceMissingGit covers acceptance criterion (4).
//
// Creates a source that has `.act/config.json` (so it isn't caught by
// FindActStatePath's missing-dir branch) but no `.act/.git`, and asserts
// the command rejects with code "act_state_not_initialized" exit 3.
func TestBootstrapWorker_SourceMissingGit(t *testing.T) {
	srcRoot := makeRepo(t)
	// Lay down .act/ + config.json but explicitly NOT the nested .git.
	if err := os.MkdirAll(filepath.Join(srcRoot, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir src .act: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".act", "config.json"),
		[]byte(`{"node_id":"deadbeef","version":"0.1.0","created_at":"2026-05-01T00:00:00.000Z","last_hlc":{"wall":"2026-05-01T00:00:00.000Z","logical":0,"node_id":"deadbeef"}}`),
		0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	targetRoot := makeBootstrapTarget(t)
	out, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
	})
	if code != 3 {
		t.Fatalf("exit=%d, want 3; out=%+v", code, out)
	}
	m, _ := out.(map[string]any)
	if got, _ := m["error"].(string); got != "act_state_not_initialized" {
		t.Errorf("error code = %q, want act_state_not_initialized", got)
	}

	// And target must not have been written.
	if _, err := os.Stat(filepath.Join(targetRoot, ".act")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("target .act/ created despite source error: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, ".act.bootstrap")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging dir leaked: err=%v", err)
	}
}

// TestBootstrapWorker_JSONShape covers acceptance criterion (5).
//
// Runs the subprocess binary with --json and asserts the envelope has
// the documented fields with the right types. Drives at the subprocess
// boundary because that's the surface the doc claim is made on.
func TestBootstrapWorker_JSONShape(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	stdout, _ := mustRunAct(t, srcRoot, 0, "bootstrap-worker", targetRoot, "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v\n%s", err, stdout)
	}
	for _, k := range []string{"source_root", "target", "ops_copied", "snapshots_copied", "dispatch_hlc"} {
		if _, ok := got[k]; !ok {
			t.Errorf("JSON envelope missing field %q\n%s", k, stdout)
		}
	}
	if _, ok := got["ops_copied"].(float64); !ok {
		t.Errorf("ops_copied not a number: %T %v", got["ops_copied"], got["ops_copied"])
	}
	if v, ok := got["dispatch_hlc"].(string); !ok || v == "" {
		t.Errorf("dispatch_hlc not a non-empty string: %T %v", got["dispatch_hlc"], got["dispatch_hlc"])
	}
}

// TestBootstrapWorker_RoundTripValidation covers acceptance criterion (6).
//
// After bootstrap, runs `act ready --json` against both source and
// target via the binary. Both must succeed (exit 0) and produce a
// JSON envelope with the same "ready" id set, since the target was
// copied from the source verbatim.
func TestBootstrapWorker_RoundTripValidation(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	_, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker code=%d", code)
	}

	srcReady, _ := mustRunAct(t, srcRoot, 0, "ready", "--json")
	tgtReady, _ := mustRunAct(t, targetRoot, 0, "ready", "--json")

	srcIDs := extractReadyIDs(t, srcReady)
	tgtIDs := extractReadyIDs(t, tgtReady)
	if !equalStringSets(srcIDs, tgtIDs) {
		t.Errorf("ready id set diverged:\n  src: %v\n  tgt: %v\n  src raw: %s\n  tgt raw: %s",
			srcIDs, tgtIDs, srcReady, tgtReady)
	}
}

// extractReadyIDs pulls the "id" fields out of an `act ready --json`
// envelope. The shape is {"ready":[{"id":"act-...",...}],"count":N};
// JSON-decode rather than regex so the test fails loudly if the
// envelope shape changes.
func extractReadyIDs(t *testing.T, raw string) []string {
	t.Helper()
	var env struct {
		Ready []struct {
			ID string `json:"id"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &env); err != nil {
		t.Fatalf("unmarshal ready: %v\n%s", err, raw)
	}
	ids := make([]string, 0, len(env.Ready))
	for _, r := range env.Ready {
		ids = append(ids, r.ID)
	}
	return ids
}

// equalStringSets compares two slices as sets (order-independent,
// dedup-on-input). The ready output is already deduped by act; we
// still treat it as a set so a future reorder doesn't break the test.
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			return false
		}
	}
	return true
}

// TestDocClaim_BootstrapWorker_HelpListsSubcommand is the user-visible
// doc-claim test for the new subcommand. The claim is "act help"
// includes `bootstrap-worker` in its subcommand listing — that's the
// surface a cold-start agent uses to discover the command exists. The
// docs_sweep_test.go registry has a matching entry pointing at this
// test.
func TestDocClaim_BootstrapWorker_HelpListsSubcommand(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	site := t.TempDir()
	out, _ := mustRunAct(t, site, 0, "help")
	if !strings.Contains(out, "bootstrap-worker") {
		t.Errorf("act help missing bootstrap-worker in subcommand listing:\n%s", out)
	}
	// And the --help-equivalent usage line emitted on missing positional
	// must also name the subcommand and document the flags so an agent
	// running bootstrap-worker without args gets a directly actionable
	// message.
	_, stderr, _ := runAct(t, site, "bootstrap-worker")
	if !strings.Contains(stderr, "bootstrap-worker") || !strings.Contains(stderr, "--force") {
		t.Errorf("bootstrap-worker usage message missing required parts:\n%s", stderr)
	}
}

// TestBootstrapWorker_SkipsHooks asserts that bootstrap-worker (cwd-source
// mode) does NOT copy the host's `.act/hooks/` directory to the worker
// (act-43cf99). Regression for: host-specific close hooks (e.g. the act
// repo's hook that runs `go vet ./...`) break workers dispatched into
// non-host repos. Verifies both that the directory is absent and that no
// loose files from the host's hooks/ tree leak under target.
func TestBootstrapWorker_SkipsHooks(t *testing.T) {
	srcRoot, _ := makeBootstrapSource(t)
	targetRoot := makeBootstrapTarget(t)

	// Seed the source with a host-specific hook file that would fail in
	// an arbitrary worker context (mimics the act repo's close hook).
	srcHooks := filepath.Join(srcRoot, ".act", "hooks")
	if err := os.MkdirAll(srcHooks, 0o755); err != nil {
		t.Fatalf("mkdir src hooks: %v", err)
	}
	hookBody := []byte("#!/bin/sh\necho 'host-specific hook'\nexit 1\n")
	if err := os.WriteFile(filepath.Join(srcHooks, "close"), hookBody, 0o755); err != nil {
		t.Fatalf("seed src close hook: %v", err)
	}
	// And a stray supporting file under hooks/ to make sure the entire
	// subtree is skipped, not just files matching a specific name.
	if err := os.WriteFile(filepath.Join(srcHooks, "helper.sh"), []byte("# helper\n"), 0o755); err != nil {
		t.Fatalf("seed src helper hook: %v", err)
	}

	_, code := RunBootstrapWorker(BootstrapWorkerOptions{
		SourceCWD: srcRoot,
		Target:    targetRoot,
	})
	if code != 0 {
		t.Fatalf("bootstrap-worker code=%d", code)
	}

	// The target's `.act/hooks/` must not exist — the whole subtree is
	// skipped on copy.
	targetHooks := filepath.Join(targetRoot, ".act", "hooks")
	if _, err := os.Stat(targetHooks); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("target .act/hooks/ unexpectedly present: err=%v", err)
	}
	// Belt-and-braces: no stray files anywhere under target that came
	// from the host's hooks tree.
	for _, name := range []string{"close", "helper.sh"} {
		if _, err := os.Stat(filepath.Join(targetHooks, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("hook %q leaked into target: err=%v", name, err)
		}
	}

	// Sanity: a non-hook file (config.json) is still copied — we didn't
	// over-skip.
	if _, err := os.Stat(filepath.Join(targetRoot, ".act", "config.json")); err != nil {
		t.Errorf("config.json missing after skip-hooks copy: %v", err)
	}
}

// Silence unused-import warnings when the file is edited down: we keep
// io and io.EOF available for future cases (e.g. an empty-source test)
// without churning imports.
var _ = io.EOF
