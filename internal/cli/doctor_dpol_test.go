package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression cover for act-e5d6b4 item 1. RunDoctor started its remote-status
// block from the Go zero value, so every run that never reached
// `phase2-reconciliation` — a fold-failed run, or any `--check <other>` —
// printed `"remote_reachable": false` although no reachability probe ran.

// dpolRemoteReachable runs RunDoctor and returns the remote_reachable value
// as the --json output carries it, so the assertion reads the user-visible
// field and not only the Go struct.
func dpolRemoteReachable(t *testing.T, root string, opts DoctorOptions) bool {
	t.Helper()
	res, _ := folderrRun(t, root, opts)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal doctor result: %v", err)
	}
	var parsed struct {
		RemoteStatus map[string]any `json:"remote_status"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse doctor JSON %s: %v", data, err)
	}
	v, ok := parsed.RemoteStatus["remote_reachable"].(bool)
	if !ok {
		t.Fatalf("doctor JSON has no boolean remote_status.remote_reachable: %s", data)
	}
	return v
}

// TestDocClaim_Doctor_RemoteReachableFalseOnlyWhenProbeFailed asserts the
// spec's `remote_reachable` field line: it is false only when the probe ran
// and failed. A run in which the probe never ran reports the same value a
// healthy --no-fetch run reports.
func TestDocClaim_Doctor_RemoteReachableFalseOnlyWhenProbeFailed(t *testing.T) {
	healthy := makeCreateRepo(t)
	mustCreate(t, healthy, "A")
	want := dpolRemoteReachable(t, healthy, DoctorOptions{NoFetch: true})
	if !want {
		t.Fatalf("baseline: healthy --no-fetch run reports remote_reachable=false, want true")
	}

	failed, _ := folderrSeedStore(t)
	// Path proof: this store's fold really fails, so the runs below take
	// the fold-error path and skip phase2-reconciliation.
	seed, _ := folderrRun(t, failed, DoctorOptions{NoFetch: true})
	if len(foldfailFindingsFor(seed, CheckFoldError)) != 1 {
		t.Fatalf("seed: want one fold-error finding, got %+v", seed.Findings)
	}
	for _, f := range seed.Findings {
		if f.Check == CheckRemoteReachable || strings.HasPrefix(f.Check, "remote-") {
			t.Fatalf("seed: a reachability finding on a fold-failed run: %+v", f)
		}
	}

	cases := []struct {
		name string
		root string
		opts DoctorOptions
	}{
		{"fold-failed plain --no-fetch", failed, DoctorOptions{NoFetch: true}},
		{"fold-failed --check cycle", failed, DoctorOptions{Check: "cycle"}},
		{"fold-failed --check cycle --no-fetch", failed, DoctorOptions{Check: "cycle", NoFetch: true}},
		{"healthy --check cycle", healthy, DoctorOptions{Check: "cycle"}},
		{"healthy --check time-travel", healthy, DoctorOptions{Check: "time-travel"}},
	}
	for _, tc := range cases {
		if got := dpolRemoteReachable(t, tc.root, tc.opts); got != want {
			t.Errorf("%s: remote_reachable = %v with no probe run, want %v (the healthy --no-fetch value)", tc.name, got, want)
		}
	}
}
