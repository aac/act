package cli

// Doc-claim tests for the user-visible `act harvest --json` envelope
// (act-c8028f). The harvest help text in cmd/act/help.go names three
// load-bearing field names that consumers (the orchestrator's harvest
// driver, doctor, and the act skill's recommended postlude) depend on:
//
//	harvested_ops      list of op paths copied this run
//	skipped_ops        list of {path, reason} for ops the host already had
//	fold_diff_summary  index-rebuild counts ({issues_indexed, ops_added})
//
// Each docclaim test below pairs with a docClaimRegistry entry in
// docs_sweep_test.go and asserts:
//
//   1. The help text contains the claimed field name (the doc claim).
//   2. The JSON shape emitted by `act harvest --json` actually carries
//      that key. This is the user-visible boundary — we drive the binary
//      and unmarshal the JSON envelope rather than introspecting the
//      HarvestResult struct directly, because the JSON tag is the part
//      the orchestrator can break against.
//
// The doc claim layer alone is satisfied by the registry sweep
// (TestDocSweep_AllClaimsHaveAssertingTests reads the doc file and looks
// for the substring); the docclaim test exists to assert the boundary
// the doc names. See AGENTS.md "Documentation discipline" for why this
// matters — internal-only assertions miss the kind of drift that ate
// act-6fca and act-ac52.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// harvestJSONEnvelope is the cross-test shape we unmarshal into to
// assert presence-of-keys. We deliberately use json.RawMessage for the
// nested objects so the tests don't couple to internal field names
// beyond the three the registry tracks.
type harvestJSONEnvelope struct {
	HarvestedOps    *json.RawMessage `json:"harvested_ops"`
	SkippedOps      *json.RawMessage `json:"skipped_ops"`
	FoldDiffSummary *json.RawMessage `json:"fold_diff_summary"`
	DryRun          bool             `json:"dry_run"`
}

// runHarvestJSON drives `act harvest <worker> --json --dry-run` in the
// host's working tree and returns the parsed envelope. We use --dry-run
// so the test doesn't have to set up a writable nested .act/.git for the
// commit; the JSON shape is identical to the non-dry-run path (the
// harvest source code returns the same HarvestResult struct).
func runHarvestJSON(t *testing.T, host, worker string) harvestJSONEnvelope {
	t.Helper()
	stdout, _ := mustRunAct(t, host, 0, "harvest", worker, "--dry-run", "--json")
	var env harvestJSONEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("harvest --json --dry-run: invalid JSON: %v\n%s", err, stdout)
	}
	return env
}

// TestDocClaim_Harvest_JSONHarvestedOpsField asserts that the harvest
// help text names the `harvested_ops` key AND that the JSON envelope
// actually carries that key on the wire.
func TestDocClaim_Harvest_JSONHarvestedOpsField(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	// Doc-side check (mirrored by the registry sweep but kept here for
	// fail-loud locality when the assertion itself is read).
	helpStdout, _ := mustRunAct(t, t.TempDir(), 0, "help")
	if !strings.Contains(helpStdout, "harvested_ops") {
		t.Fatalf("act help no longer mentions harvested_ops — registry entry stale")
	}

	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)
	env := runHarvestJSON(t, host, worker)

	if env.HarvestedOps == nil {
		t.Fatalf("harvest JSON envelope missing harvested_ops key")
	}
	// Verify it's an array (the doc says "list of op file paths"). An
	// empty array is valid (no-op case), but the type must be array.
	if !strings.HasPrefix(strings.TrimSpace(string(*env.HarvestedOps)), "[") {
		t.Errorf("harvested_ops is not a JSON array: %s", string(*env.HarvestedOps))
	}
	// Dry-run with a populated worker must surface a non-empty list.
	var paths []string
	if err := json.Unmarshal(*env.HarvestedOps, &paths); err != nil {
		t.Fatalf("harvested_ops did not decode as []string: %v", err)
	}
	if len(paths) == 0 {
		t.Errorf("harvested_ops is empty despite 2 worker ops; envelope: %+v", env)
	}
	// Sanity: entries should look like ops/-relative paths.
	for _, p := range paths {
		if filepath.IsAbs(p) {
			t.Errorf("harvested_ops entry %q is absolute; expected ops/-relative", p)
		}
	}
}

// TestDocClaim_Harvest_JSONSkippedOpsField asserts that `skipped_ops`
// is present in the envelope. We force entries into the list by
// running harvest twice — the second run finds every worker op already
// present at the host (the idempotency path) and records each under
// skipped_ops with reason=already_present.
func TestDocClaim_Harvest_JSONSkippedOpsField(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	helpStdout, _ := mustRunAct(t, t.TempDir(), 0, "help")
	if !strings.Contains(helpStdout, "skipped_ops") {
		t.Fatalf("act help no longer mentions skipped_ops — registry entry stale")
	}

	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 2)
	// First, a real harvest (not dry-run) so the host actually has the
	// worker's ops. We call RunHarvest directly because the binary
	// doesn't accept --dry-run=false and we want the host-side mutation.
	if _, code := RunHarvest(HarvestOptions{HostCWD: host, WorkerPath: worker}); code != 0 {
		t.Fatalf("first harvest failed; code=%d", code)
	}
	// Second pass via the binary — every worker op is now already at
	// the host, so skipped_ops should be non-empty.
	env := runHarvestJSON(t, host, worker)
	if env.SkippedOps == nil {
		t.Fatalf("harvest JSON envelope missing skipped_ops key")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(*env.SkippedOps)), "[") {
		t.Errorf("skipped_ops is not a JSON array: %s", string(*env.SkippedOps))
	}
	// Decode and confirm each entry carries {path, reason}.
	var entries []map[string]any
	if err := json.Unmarshal(*env.SkippedOps, &entries); err != nil {
		t.Fatalf("skipped_ops did not decode as []object: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("skipped_ops is empty after first-pass harvest; expected idempotency entries")
	}
	for _, e := range entries {
		if _, ok := e["path"]; !ok {
			t.Errorf("skipped_ops entry missing 'path' key: %+v", e)
		}
		if _, ok := e["reason"]; !ok {
			t.Errorf("skipped_ops entry missing 'reason' key: %+v", e)
		}
	}
}

// TestDocClaim_Harvest_JSONFoldDiffSummaryField asserts that
// `fold_diff_summary` is present and is an object with the documented
// shape ({issues_indexed, ops_added}).
func TestDocClaim_Harvest_JSONFoldDiffSummaryField(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}
	helpStdout, _ := mustRunAct(t, t.TempDir(), 0, "help")
	if !strings.Contains(helpStdout, "fold_diff_summary") {
		t.Fatalf("act help no longer mentions fold_diff_summary — registry entry stale")
	}

	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 1)
	env := runHarvestJSON(t, host, worker)
	if env.FoldDiffSummary == nil {
		t.Fatalf("harvest JSON envelope missing fold_diff_summary key")
	}
	var summary map[string]any
	if err := json.Unmarshal(*env.FoldDiffSummary, &summary); err != nil {
		t.Fatalf("fold_diff_summary did not decode as object: %v", err)
	}
	// Both documented sub-keys must be present in the envelope shape,
	// even when their values are zero (dry-run). Their absence would
	// be a JSON-tag drift bug, which is exactly what this doc-claim
	// boundary test exists to catch.
	for _, k := range []string{"issues_indexed", "ops_added"} {
		if _, ok := summary[k]; !ok {
			t.Errorf("fold_diff_summary missing sub-key %q: %+v", k, summary)
		}
	}
}
