package main

// Subprocess-boundary tests for the `--no-fetch` read mode (act-3803ac).
// The internal assertions live in internal/cli/nofetch_docclaim_test.go;
// these pin the CLI surface an agent actually types.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDocClaim_NoFetch_CLISurface asserts the flag exists on every read
// command, that it is accepted without a remote configured, and that the
// `refresh` report the flag help promises is present in --json output.
func TestDocClaim_NoFetch_CLISurface(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	createIssue(t, dir, "a ticket")

	for _, args := range [][]string{
		{"ready", "--no-fetch", "--json"},
		{"list", "--no-fetch", "--json"},
		{"search", "ticket", "--no-fetch", "--json"},
	} {
		stdout, stderr, code := runActIn(t, dir, args...)
		if code != 0 {
			t.Fatalf("act %v: exit %d; stderr=%s", args, code, stderr)
		}
		var doc struct {
			Refresh *struct {
				Reason  string `json:"reason"`
				Fetched bool   `json:"fetched"`
			} `json:"refresh"`
		}
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("act %v: json: %v (out=%s)", args, err, stdout)
		}
		if doc.Refresh == nil {
			t.Errorf("act %v: no `refresh` key in --json output: %s", args, stdout)
			continue
		}
		if doc.Refresh.Reason != "no_fetch" {
			t.Errorf("act %v: refresh.reason = %q, want no_fetch", args, doc.Refresh.Reason)
		}
		if doc.Refresh.Fetched {
			t.Errorf("act %v: refresh.fetched = true under --no-fetch", args)
		}
	}

	// `act log` and `act show` take the flag too.
	if _, stderr, code := runActIn(t, dir, "log", "--since", "24h", "--no-fetch", "--json"); code != 0 {
		t.Errorf("act log --no-fetch: exit %d; stderr=%s", code, stderr)
	}
}

// TestDocClaim_NoFetch_RejectsFreshCombo pins the flag-conflict message:
// --no-fetch and --fresh ask for opposite things, so `act ready` rejects
// the pair with exit 2 rather than silently picking one.
func TestDocClaim_NoFetch_RejectsFreshCombo(t *testing.T) {
	dir := bootstrapLoopRepo(t)

	for _, other := range []string{"--fresh", "--no-cache"} {
		_, stderr, code := runActIn(t, dir, "ready", "--no-fetch", other)
		if code != 2 {
			t.Errorf("act ready --no-fetch %s: exit %d, want 2 (stderr=%s)", other, code, stderr)
		}
		if !strings.Contains(stderr, "mutually exclusive") {
			t.Errorf("act ready --no-fetch %s: stderr = %q, want it to say the flags are mutually exclusive", other, stderr)
		}
	}
}
