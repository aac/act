package cli

// Doc-claim tests for the user-visible `act state export --json` envelope
// (act-d0a0a9). The --help text in cmd/act/state.go (stateExportHelpText)
// documents the JSON output shape for two branches:
//
//  success (exit 0):
//    harvested_ops is a JSON array of op-path strings relative to .act/ops/
//
//  no-.act/ error (exit 2):
//    {"error":"worker_state_not_found","message":"...","details":{}}
//
// Each test below pairs with a docClaimRegistry entry in docs_sweep_test.go
// and asserts the documented behavior at the CLI subprocess boundary — the
// same boundary the rwt consumer hits — rather than at an internal Go API.
//
// Why subprocess boundary matters: the prefix-ok bug (act-6fca) and the
// harvest-drift bug (act-ac52) both had passing internal-state tests because
// the failure lived upstream of the assertion point. Driving the binary and
// reading its exit code + stdout is the only guarantee that matches the
// documented contract.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_StateExport_JSONSuccessEnvelope asserts:
//  1. The --help text (stateExportHelpText in cmd/act/state.go) contains the
//     claim "JSON array of op-path strings".
//  2. When a dir is seeded via act state import and then exported, the JSON
//     envelope has harvested_ops as a JSON array, and the command exits 0.
//
// "Seeded" means the dir was produced by RunBootstrapWorker (act state import
// equivalent). We then run RunHarvest (act state export) against it via the
// binary, so the test asserts at the subprocess boundary.
func TestDocClaim_StateExport_JSONSuccessEnvelope(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}

	// 1. Doc-side check: the --help text must document the JSON array claim.
	root := repoRootForDocClaim(t)
	statePath := filepath.Join(root, "cmd/act/state.go")
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read cmd/act/state.go: %v", err)
	}
	const successClaim = "JSON array of op-path strings"
	if !strings.Contains(string(body), successClaim) {
		t.Fatalf("cmd/act/state.go stateExportHelpText no longer contains %q\n"+
			"  The --help text must document the success envelope shape.",
			successClaim)
	}

	// 2. Boundary check: set up a host with act state, seed a worker dir via
	//    act state import, then act state export --json from that worker dir.
	host := makeHarvestHost(t)
	worker := makeHarvestWorker(t, host, 1)

	// Run act state export --json on the seeded worker dir.
	stdout, stderr, code := runAct(t, host, "state", "export", worker, "--json")
	if code != 0 {
		t.Fatalf("act state export --json on seeded dir: exit %d (want 0)\nstdout=%s\nstderr=%s",
			code, stdout, stderr)
	}

	// Unmarshal the envelope and assert harvested_ops is a JSON array.
	var env struct {
		HarvestedOps *json.RawMessage `json:"harvested_ops"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("act state export --json: invalid JSON: %v\nstdout=%s", err, stdout)
	}
	if env.HarvestedOps == nil {
		t.Fatalf("act state export --json: envelope missing harvested_ops key\nstdout=%s", stdout)
	}
	raw := strings.TrimSpace(string(*env.HarvestedOps))
	if !strings.HasPrefix(raw, "[") {
		t.Errorf("harvested_ops is not a JSON array: %s", raw)
	}
	// Decode as []string — the claim is specifically "op-path strings".
	var paths []string
	if err := json.Unmarshal(*env.HarvestedOps, &paths); err != nil {
		t.Fatalf("harvested_ops did not decode as []string: %v\nraw=%s", err, raw)
	}
	// All entries must be relative paths (not absolute), as documented.
	for _, p := range paths {
		if filepath.IsAbs(p) {
			t.Errorf("harvested_ops entry %q is absolute; expected relative to .act/ops/", p)
		}
	}
}

// TestDocClaim_StateExport_JSONNoActError asserts:
//  1. The --help text (stateExportHelpText in cmd/act/state.go) contains the
//     claim "worker_state_not_found".
//  2. When a dir with no .act/ is passed to act state export --json, the
//     command exits 2 and emits an error envelope with
//     "error":"worker_state_not_found".
func TestDocClaim_StateExport_JSONNoActError(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("actBinaryPath not set; build is required (TestMain ran)")
	}

	// 1. Doc-side check: the --help text must document the error code.
	root := repoRootForDocClaim(t)
	statePath := filepath.Join(root, "cmd/act/state.go")
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read cmd/act/state.go: %v", err)
	}
	const errorClaim = "worker_state_not_found"
	if !strings.Contains(string(body), errorClaim) {
		t.Fatalf("cmd/act/state.go stateExportHelpText no longer contains %q\n"+
			"  The --help text must document the no-.act/ error code.",
			errorClaim)
	}

	// 2. Boundary check: set up a host with act state, then run act state
	//    export --json against a dir that has no .act/ at all.
	host := makeHarvestHost(t)
	emptyDir := t.TempDir()

	stdout, stderr, code := runAct(t, host, "state", "export", emptyDir, "--json")
	if code != 2 {
		t.Fatalf("act state export --json on dir-with-no-.act: exit %d (want 2)\nstdout=%s\nstderr=%s",
			code, stdout, stderr)
	}

	// The envelope must be on stdout (not stderr) when --json is passed.
	var env struct {
		Error   string          `json:"error"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		t.Fatalf("act state export --json: expected JSON on stdout, got empty\nstderr=%s", stderr)
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		t.Fatalf("act state export --json: stdout not valid JSON: %v\nstdout=%s", err, stdout)
	}
	if env.Error != errorClaim {
		t.Errorf("envelope error = %q, want %q\nfull stdout: %s", env.Error, errorClaim, stdout)
	}
	// details must be a JSON object (possibly empty), never null, never absent.
	if env.Details == nil {
		t.Errorf("envelope missing 'details' key\nstdout=%s", stdout)
	} else {
		trimmedDetails := strings.TrimSpace(string(env.Details))
		if !strings.HasPrefix(trimmedDetails, "{") {
			t.Errorf("envelope details is not a JSON object: %s", trimmedDetails)
		}
	}
}
