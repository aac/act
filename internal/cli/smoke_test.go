package cli

// End-to-end smoke test mirroring .github/scripts/smoke.sh.
//
// The CI matrix workflow (docs/issues/act-2e8d.md, spec-v2 §7.8) drives
// the same flow via bash + jq inside a container. This test gives the
// same coverage on a developer laptop, with no shell or jq dependency,
// using the act binary that TestMain already builds for the package.
//
// Flow:
//   1. `act init`
//   2. `act create "smoke task" --json`        (capture id)
//   3. `act show <id> --json`                  (assert id round-trips)
//   4. `act list --json`                       (assert count == 1)
//   5. `act ready --json`                      (assert >= 1 ready)
//   6. `act update --claim --isolated --json <id>` (assert claimed=true)
//   7. `act close --reason ... --json <id>`    (assert reason)
//   8. `act doctor --fix --json` then `act doctor --json` (0 findings)

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestSmokeWorkflow runs the canonical happy-path flow against a fresh
// repo, asserting both exit codes and JSON shapes per command. Mirrors
// .github/scripts/smoke.sh; failures here pre-date a CI run.
func TestSmokeWorkflow(t *testing.T) {
	if actBinaryPath == "" {
		t.Fatalf("smoke: act binary not built (TestMain did not run?)")
	}

	site := t.TempDir()
	// Fresh git tree: act commands need .git/ to find the repo root.
	// `git -C <site> init -q -b main` initializes <site> in place (no
	// trailing path means current dir from -C).
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "smoke@example.com", "smoke")

	// Step 1: init.
	initOut, _ := mustRunAct(t, site, 0, "init", "--json")
	var initRes struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(initOut), &initRes); err != nil {
		t.Fatalf("init JSON parse: %v\n%s", err, initOut)
	}
	if initRes.NodeID == "" {
		t.Fatalf("init: empty node_id\n%s", initOut)
	}

	// Step 2: create. Capture id.
	createOut, _ := mustRunAct(t, site, 0, "create", "--json", "smoke task")
	var createRes struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(createOut), &createRes); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, createOut)
	}
	if len(createRes.ID) < 4 {
		t.Fatalf("create: short or empty id %q\n%s", createRes.ID, createOut)
	}
	if createRes.Title != "smoke task" {
		t.Fatalf("create: title = %q want \"smoke task\"", createRes.Title)
	}
	id := createRes.ID

	// Step 3: show round-trips id.
	showOut, _ := mustRunAct(t, site, 0, "show", "--json", id)
	var showRes map[string]any
	if err := json.Unmarshal([]byte(showOut), &showRes); err != nil {
		t.Fatalf("show JSON parse: %v\n%s", err, showOut)
	}
	if got, _ := showRes["id"].(string); got != id {
		t.Fatalf("show: id = %q want %q\n%s", got, id, showOut)
	}

	// Step 4: list reports exactly one issue.
	listOut, _ := mustRunAct(t, site, 0, "list", "--json")
	var listRes struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(listOut), &listRes); err != nil {
		t.Fatalf("list JSON parse: %v\n%s", err, listOut)
	}
	if listRes.Count != 1 {
		t.Fatalf("list: count = %d want 1\n%s", listRes.Count, listOut)
	}

	// Step 5: ready reports >= 1 ready issue.
	readyOut, _ := mustRunAct(t, site, 0, "ready", "--json")
	var readyRes struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(readyOut), &readyRes); err != nil {
		t.Fatalf("ready JSON parse: %v\n%s", err, readyOut)
	}
	if readyRes.Count < 1 {
		t.Fatalf("ready: count = %d want >= 1\n%s", readyRes.Count, readyOut)
	}

	// Step 6: claim. Flags must precede the positional id because the
	// stdlib flag package stops parsing at the first non-flag arg.
	claimOut, _ := mustRunAct(t, site, 0, "update", "--claim", "--isolated", "--json", id)
	var claimRes struct {
		OK      bool `json:"ok"`
		Claimed bool `json:"claimed"`
	}
	if err := json.Unmarshal([]byte(claimOut), &claimRes); err != nil {
		t.Fatalf("claim JSON parse: %v\n%s", err, claimOut)
	}
	if !claimRes.OK || !claimRes.Claimed {
		t.Fatalf("claim: ok=%v claimed=%v want both true\n%s", claimRes.OK, claimRes.Claimed, claimOut)
	}

	// Step 7: close with reason.
	closeOut, _ := mustRunAct(t, site, 0, "close", "--reason", "smoke complete", "--json", id)
	var closeRes struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(closeOut), &closeRes); err != nil {
		t.Fatalf("close JSON parse: %v\n%s", err, closeOut)
	}
	if closeRes.ID != id {
		t.Fatalf("close: id = %q want %q\n%s", closeRes.ID, id, closeOut)
	}
	if closeRes.Reason != "smoke complete" {
		t.Fatalf("close: reason = %q want \"smoke complete\"\n%s", closeRes.Reason, closeOut)
	}

	// Step 8: doctor. The first call rebuilds the index after the close
	// op; without --fix it surfaces an index-divergence finding (the
	// reader-side rebuild has not yet caught up to the new op file).
	// With --fix the divergence is remediated to a warn-level finding;
	// a follow-up call must report zero findings.
	mustRunAct(t, site, 0, "doctor", "--fix", "--json")
	doctorOut, _ := mustRunAct(t, site, 0, "doctor", "--json")
	var doctorRes struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(doctorOut), &doctorRes); err != nil {
		t.Fatalf("doctor JSON parse: %v\n%s", err, doctorOut)
	}
	if doctorRes.Count != 0 {
		t.Fatalf("doctor: count = %d want 0\n%s", doctorRes.Count, doctorOut)
	}
}

// TestSmokeScript runs .github/scripts/smoke.sh end-to-end if bash and
// jq are available. This guards that the script stays in lockstep with
// the Go-side TestSmokeWorkflow. Skipped on platforms missing bash/jq
// (e.g. fresh CI containers without the smoke deps installed yet) so
// the test never blocks a clean local run.
func TestSmokeScript(t *testing.T) {
	if actBinaryPath == "" {
		t.Fatalf("smoke: act binary not built (TestMain did not run?)")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; skipping smoke.sh integration")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; skipping smoke.sh integration")
	}

	// Walk up to the repo root so we can locate the script regardless
	// of where `go test` ran from.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getcwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))
	script := filepath.Join(repoRoot, ".github", "scripts", "smoke.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("locate smoke.sh: %v", err)
	}

	cmd := exec.Command("bash", script, actBinaryPath, t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("smoke.sh: %v\n%s", err, out)
	}
}
