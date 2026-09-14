package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_BlockedStatus_WritesNoOp pins act-6ef7b0 and the docs/spec.md
// claim that `act update --status blocked` writes no op: with the backing dep
// edge present, no update_field status op lands in .act/ops, nothing is
// committed, the issue still reads blocked, and neither the JSON nor the human
// output claims an op.
func TestDocClaim_BlockedStatus_WritesNoOp(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	outA, _ := mustRunAct(t, site, 0, "create", "blocked no-op probe A", "--json")
	idA := pickIDFromJSON(t, outA)
	outB, _ := mustRunAct(t, site, 0, "create", "blocker B", "--json")
	idB := pickIDFromJSON(t, outB)
	mustRunAct(t, site, 0, "dep", "add", idA, idB, "--type", "blocks")

	headBefore, _ := runGitOut(filepath.Join(site, ".act"), "rev-parse", "HEAD")
	opsBefore := len(capauditReadOpFiles(t, site))

	jsonOut, stderr, code := runAct(t, site, "update", idA, "--status", "blocked", "--json")
	if code != 0 {
		t.Fatalf("--status blocked --json: exit %d; stdout=%s stderr=%s", code, jsonOut, stderr)
	}
	var res struct {
		OpsWritten     int  `json:"ops_written"`
		Committed      bool `json:"committed"`
		AlreadyBlocked bool `json:"already_blocked"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &res); err != nil {
		t.Fatalf("parse update JSON: %v\n%s", err, jsonOut)
	}
	if res.OpsWritten != 0 || res.Committed || !res.AlreadyBlocked {
		t.Errorf("--status blocked JSON = %+v, want ops_written=0 committed=false already_blocked=true", res)
	}

	humanOut, stderr, code := runAct(t, site, "update", idA, "--status", "blocked")
	if code != 0 {
		t.Fatalf("--status blocked (human): exit %d; stderr=%s", code, stderr)
	}
	if want := "Unchanged " + idA + ": already blocked by its dep edge (no ops written)"; !strings.Contains(humanOut, want) {
		t.Errorf("--status blocked human output = %q, want it to contain %q", humanOut, want)
	}

	if got := len(capauditReadOpFiles(t, site)); got != opsBefore {
		t.Errorf(".act/ops file count went %d -> %d; --status blocked must write no op", opsBefore, got)
	}
	if headAfter, _ := runGitOut(filepath.Join(site, ".act"), "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("tracker HEAD moved %s -> %s; --status blocked must not commit", headBefore, headAfter)
	}
	for _, raw := range capauditReadOpFiles(t, site) {
		var head struct {
			OpType  string `json:"op_type"`
			Payload struct {
				Field string `json:"field"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(raw), &head) == nil && head.OpType == "update_field" && head.Payload.Field == "status" {
			t.Errorf("found an update_field status op in .act/ops: %s", raw)
		}
	}

	showOut, _ := mustRunAct(t, site, 0, "show", idA, "--json")
	if !strings.Contains(showOut, idB) {
		t.Errorf("act show %s no longer lists blocker %s: %s", idA, idB, showOut)
	}
	readyOut, _ := mustRunAct(t, site, 0, "ready", "--json")
	if strings.Contains(readyOut, idA) {
		t.Errorf("blocked issue %s appears in act ready: %s", idA, readyOut)
	}
}

// capauditReadOpFiles returns the contents of every op file under site/.act/ops.
func capauditReadOpFiles(t *testing.T, site string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(filepath.Join(site, ".act", "ops"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		raw, rerr := os.ReadFile(p)
		out = append(out, string(raw))
		return rerr
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("capauditReadOpFiles: walk %s/.act/ops: err=%v files=%d", site, err, len(out))
	}
	return out
}
