package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// dplWarnings runs `act <args>`, wants exit 0, and returns the `warnings`
// array of the JSON result with the raw stdout and stderr.
func dplWarnings(t *testing.T, dir string, args ...string) ([]string, string, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", args...)
	var res struct {
		Error    string   `json:"error"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act %v: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	if res.Error != "" {
		t.Errorf("act %s: error %q, want none\nstdout=%s", strings.Join(args, " "), res.Error, out)
	}
	return res.Warnings, out, stderr
}

// dplWantQueueFailed asserts one `--offline --json` write with an unwritable
// queue: exit 0 (dplWarnings), `push_queue_failed` once in `warnings`, and
// the stderr WARNING block still printed.
func dplWantQueueFailed(t *testing.T, dir string, args ...string) {
	t.Helper()
	what := "act " + strings.Join(args, " ")
	warnings, out, stderr := dplWarnings(t, dir, args...)
	n := 0
	for _, w := range warnings {
		if w == "push_queue_failed" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%s with .act/.pending-pushes unwritable: warnings = %v, want push_queue_failed once\nstdout=%s", what, warnings, strings.TrimSpace(out))
	}
	edgWantQueueWarning(t, what, stderr)
}

// TestDocClaim_Spec_DplOfflineQueueFailureIsInWarnings asserts the
// `push_queue_failed` sentence of "Offline mode" in docs/spec.md: an
// `--offline` write whose append to `.act/.pending-pushes` fails names it in
// the `warnings` array of its `--json` result, on every write command
// (act-18bd72 item 3: it was a stderr WARNING block only, which a caller
// reading the JSON result could not see).
func TestDocClaim_Spec_DplOfflineQueueFailureIsInWarnings(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		dir := sp6Site(t)
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "create", "--json", "--offline", "dpl offline create")
	})

	t.Run("update", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline update target")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "update", "--json", id, "--priority", "1", "--offline")
	})

	t.Run("update --claim", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline claim target")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "update", "--json", id, "--claim", "--offline")
	})

	t.Run("close", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline close target")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "close", "--json", "--no-doctor", "--offline", id)
	})

	t.Run("reopen", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline reopen target")
		pass5Run(t, dir, 0, "", "close", "--no-doctor", id, "--reason", "dpl")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "reopen", "--json", "--offline", id)
	})

	t.Run("dep add", func(t *testing.T) {
		dir := sp6Site(t)
		child := createIssue(t, dir, "dpl offline dep child")
		parent := createIssue(t, dir, "dpl offline dep parent")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "dep", "add", "--json", "--offline", child, parent)
	})

	t.Run("dep add --external", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline external dep subject")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "dep", "add", "--json", "--offline", id, "--external", "linear:DPL-1")
	})

	t.Run("delete", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl offline delete target")
		edgBlockPendingPushes(t, dir)
		dplWantQueueFailed(t, dir, "delete", "--json", "--offline", "--reason", "dpl", id)
	})

	// A queue that can be written carries no such warning.
	t.Run("absent when the queue append works", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "dpl quiet offline target")
		_, out, _ := dplWarnings(t, dir, "update", "--json", id, "--priority", "1", "--offline")
		if strings.Contains(out, "push_queue_failed") || strings.Contains(out, "warnings") {
			t.Errorf("update --offline with a writable queue carries a warning: %s", strings.TrimSpace(out))
		}
		if pnrPendingPushes(t, dir) == "" {
			t.Errorf("update --offline with a writable queue queued nothing")
		}
	})
}
