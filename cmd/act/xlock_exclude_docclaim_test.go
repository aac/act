package main

// act-1d3110 — doc-claim test for the `nested-exclude` doctor check, run
// against the real binary. Covers the spec's check-table row, the spec's
// `--fix` flag line, and the `--fix` flag help string.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// xlockEntries is the list the spec row prints. Spelled out here rather
// than imported so a change to the code's list without the doc (or the
// reverse) fails this test.
var xlockEntries = []string{
	"/.write.lock", "/.slow-writes", "/.pending-pushes", "/.sync-log", "/.compact.lock",
	"/index.db", "/index.db-wal", "/index.db-shm", "/index.db-journal",
	// act-70569e: the rest of what act can leave at the top of .act/.
	"/index.db.malformed-*", "/.doctor-rebuild.db*", "/fold-checkpoint.json", "/fold-checkpoint.json.tmp",
	"/.bootstrap-meta.json", "/.sync-log.tmp", "/.tmp-*", "/config-*.json.tmp",
}

func xlockHostRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, stderr, code := runActIn(t, dir, "init", "--json"); code != 0 {
		t.Fatalf("act init: exit %d; stderr=%s", code, stderr)
	}
	return dir
}

func xlockNestedUntracked(t *testing.T, host string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(host, ".act"), "status", "--short", "--untracked-files=all").CombinedOutput()
	if err != nil {
		t.Fatalf("git status in nested repo: %v: %s", err, out)
	}
	return string(out)
}

func TestDocClaim_Doctor_NestedExclude(t *testing.T) {
	host := xlockHostRepo(t)
	excludePath := filepath.Join(host, ".act", ".git", "info", "exclude")

	// `act init` writes one line per listed entry.
	body, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude after init: %v", err)
	}
	for _, e := range xlockEntries {
		if !strings.Contains("\n"+string(body), "\n"+e+"\n") {
			t.Errorf("after act init, %s has no line %q:\n%s", excludePath, e, body)
		}
	}

	// A write leaves .write.lock and index.db on disk; neither is listed
	// as untracked.
	if out, stderr, code := runActIn(t, host, "create", "xlock probe", "--json"); code != 0 {
		t.Fatalf("act create: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(host, ".act", ".write.lock")); err != nil {
		t.Fatalf("precondition: .act/.write.lock should exist after a write: %v", err)
	}
	if st := xlockNestedUntracked(t, host); strings.TrimSpace(st) != "" {
		t.Errorf("nested repo status after init + create should be empty, got:\n%s", st)
	}
	if out, _, code := runActIn(t, host, "doctor", "--check", "nested-exclude"); code != 0 || !strings.Contains(out, "0 findings") {
		t.Errorf("doctor on a freshly initialized store: exit %d, out=%s; want 0 findings", code, out)
	}

	// An older store: the exclude carries an operator's line and none of
	// act's.
	const operator = "# mine\nscratch-notes\n"
	if err := os.WriteFile(excludePath, []byte(operator), 0o644); err != nil {
		t.Fatalf("rewrite exclude: %v", err)
	}
	if st := xlockNestedUntracked(t, host); !strings.Contains(st, ".write.lock") {
		t.Fatalf("precondition: with the entries gone, .write.lock should show untracked; status:\n%s", st)
	}

	// Without --fix: one warn finding naming the missing lines; exit 0;
	// nothing written.
	out, _, code := runActIn(t, host, "doctor", "--check", "nested-exclude")
	if code != 0 {
		t.Errorf("doctor without --fix: exit %d, want 0 (warn severity); out=%s", code, out)
	}
	if !strings.Contains(out, "[warn] [nested-exclude]") || !strings.Contains(out, "1 findings") {
		t.Errorf("doctor without --fix: want one warn nested-exclude finding, got:\n%s", out)
	}
	for _, e := range xlockEntries {
		if !strings.Contains(out, e) {
			t.Errorf("finding does not name missing line %q:\n%s", e, out)
		}
	}
	if got, _ := os.ReadFile(excludePath); string(got) != operator {
		t.Errorf("doctor without --fix wrote the exclude file:\n%s", got)
	}

	// --fix appends the missing lines and keeps existing content.
	if out, _, code := runActIn(t, host, "doctor", "--check", "nested-exclude", "--fix"); code != 0 {
		t.Fatalf("doctor --fix: exit %d; out=%s", code, out)
	}
	fixed, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if !strings.HasPrefix(string(fixed), operator) {
		t.Errorf("--fix did not keep existing content at the head of the file:\n%s", fixed)
	}
	for _, e := range xlockEntries {
		if n := strings.Count(string(fixed), "\n"+e+"\n"); n != 1 {
			t.Errorf("after --fix, line %q appears %d times, want 1:\n%s", e, n, fixed)
		}
	}
	if st := xlockNestedUntracked(t, host); strings.TrimSpace(st) != "" {
		t.Errorf("nested repo status after --fix should be empty, got:\n%s", st)
	}

	// A second --fix changes nothing.
	out, _, code = runActIn(t, host, "doctor", "--check", "nested-exclude", "--fix")
	if code != 0 || !strings.Contains(out, "0 findings") {
		t.Errorf("second doctor --fix: exit %d, out=%s; want 0 findings", code, out)
	}
	if again, _ := os.ReadFile(excludePath); string(again) != string(fixed) {
		t.Errorf("second --fix changed the exclude file:\nfirst:\n%s\nsecond:\n%s", fixed, again)
	}

	// The flag help names the check among those --fix drives.
	_, helpErr, _ := runActIn(t, host, "doctor", "-h")
	if !strings.Contains(helpErr, "auto-remediate index-divergence/index-schema/nested-exclude findings") {
		t.Errorf("`act doctor -h` does not carry the --fix help string; stderr:\n%s", helpErr)
	}
}
