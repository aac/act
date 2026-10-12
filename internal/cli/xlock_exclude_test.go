package cli

// act-1d3110 — act's own working files at the top of the nested .act/
// repo (.write.lock, .slow-writes) must not show as untracked there: a
// blanket stage inside .act/ would commit them. `act init` lists them in
// .act/.git/info/exclude and `act doctor --fix` repairs a store that
// predates that.
//
// Both tests assert on `git status --short` run in the nested repo — the
// surface the ticket's premise was observed on — rather than on the
// exclude file's contents alone.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
)

// xlockCheck is the doctor check name under test, spelled as a user
// passes it to --check.
const xlockCheck = "nested-exclude"

// xlockNestedStatus returns `git status --short` for the nested .act/
// repo under host. --untracked-files=all so a file inside an untracked
// directory is named rather than collapsed into its parent.
func xlockNestedStatus(t *testing.T, host string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--short", "--untracked-files=all")
	cmd.Dir = filepath.Join(host, ".act")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in nested repo: %v\n%s", err, out)
	}
	return string(out)
}

// xlockAssertWorkingFilesPresent fails unless both working files exist on
// disk: a status that omits them because they were never written would
// pass for the wrong reason.
func xlockAssertWorkingFilesPresent(t *testing.T, host string) {
	t.Helper()
	for _, name := range []string{gitops.WriteLockFile, ".slow-writes"} {
		if _, err := os.Stat(filepath.Join(host, ".act", name)); err != nil {
			t.Fatalf("precondition: .act/%s should exist on disk: %v", name, err)
		}
	}
}

func xlockAssertStatusOmits(t *testing.T, status string) {
	t.Helper()
	for _, name := range []string{gitops.WriteLockFile, ".slow-writes"} {
		if strings.Contains(status, name) {
			t.Errorf("nested `git status --short` lists %s; want it ignored. status:\n%s", name, status)
		}
	}
}

// TestInit_NestedRepoIgnoresWorkingFiles: after `act init`, a write that
// creates the lock file and a slow write that creates .slow-writes leave
// neither showing in the nested repo's status.
func TestInit_NestedRepoIgnoresWorkingFiles(t *testing.T) {
	root := makeRepo(t)
	if out, code := RunInit(root, InitOptions{MachineID: "m", GitEmail: "u@example.com"}); code != 0 {
		t.Fatalf("RunInit: code=%d out=%+v", code, out)
	}
	// Drive the real slow-write path so .slow-writes is written by act
	// itself; the same create takes (and leaves behind) .write.lock.
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "1200")
	_ = captureStderr(t, func() {
		if out, code := RunCreate(root, CreateOptions{Title: "xlock", Type: "task"}); code != 0 {
			t.Fatalf("RunCreate: code=%d out=%+v", code, out)
		}
	})

	xlockAssertWorkingFilesPresent(t, root)
	xlockAssertStatusOmits(t, xlockNestedStatus(t, root))
}

// xlockSeedStore builds a store the way one made before act-1d3110 looks:
// initialized, holding both working files, with an exclude file that
// carries someone's own line but neither act entry.
func xlockSeedStore(t *testing.T) (host, excludePath string) {
	t.Helper()
	host = makeCreateRepo(t)
	mustCreate(t, host, "xlock seed") // leaves .act/.write.lock behind
	if err := AppendSlowWrite(filepath.Join(host, ".act"), SlowWriteRecord{
		Timestamp: "2026-10-12T00:00:00.000Z", OpID: "x", DurationMs: 1500, OpType: "create",
	}); err != nil {
		t.Fatalf("AppendSlowWrite: %v", err)
	}
	excludePath = filepath.Join(host, ".act", ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	// No trailing newline on purpose: the repair must not glue its first
	// entry onto the operator's last line.
	if err := os.WriteFile(excludePath, []byte("# operator's own\nscratch-notes"), 0o644); err != nil {
		t.Fatalf("seed exclude: %v", err)
	}
	return host, excludePath
}

// TestDoctorFix_RepairsNestedExclude: a pre-existing store whose exclude
// lacks the entries is reported by doctor, repaired by `doctor --fix`, and
// left byte-identical by a second `--fix`.
func TestDoctorFix_RepairsNestedExclude(t *testing.T) {
	host, excludePath := xlockSeedStore(t)
	xlockAssertWorkingFilesPresent(t, host)

	before := xlockNestedStatus(t, host)
	if !strings.Contains(before, gitops.WriteLockFile) || !strings.Contains(before, ".slow-writes") {
		t.Fatalf("precondition: seeded store should show both working files untracked; status:\n%s", before)
	}

	// Without --fix: a finding that names the remedy, and nothing written.
	out, code := RunDoctor(host, DoctorOptions{Check: xlockCheck})
	if code != 0 {
		t.Fatalf("doctor (no fix): exit=%d, want 0 (warn only); out=%+v", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) != 1 || res.Findings[0].Check != xlockCheck || res.Findings[0].Severity != "warn" {
		t.Fatalf("doctor (no fix): want one warn %s finding, got %+v", xlockCheck, res.Findings)
	}
	if !strings.Contains(res.Findings[0].Message, "act doctor --fix") {
		t.Errorf("finding should name the remedy `act doctor --fix`: %q", res.Findings[0].Message)
	}
	if got := xlockNestedStatus(t, host); got != before {
		t.Errorf("doctor without --fix changed the nested status:\nbefore:\n%s\nafter:\n%s", before, got)
	}

	// --fix repairs.
	if out, code := RunDoctor(host, DoctorOptions{Check: xlockCheck, Fix: true}); code != 0 {
		t.Fatalf("doctor --fix: exit=%d out=%+v", code, out)
	}
	xlockAssertStatusOmits(t, xlockNestedStatus(t, host))

	fixed, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if !strings.HasPrefix(string(fixed), "# operator's own\nscratch-notes\n") {
		t.Errorf("existing exclude content not preserved intact:\n%s", fixed)
	}

	// Second --fix: no finding, no byte changed, no entry twice.
	out, code = RunDoctor(host, DoctorOptions{Check: xlockCheck, Fix: true})
	if code != 0 {
		t.Fatalf("second doctor --fix: exit=%d out=%+v", code, out)
	}
	if res := out.(DoctorResult); len(res.Findings) != 0 {
		t.Errorf("second doctor --fix: want no findings, got %+v", res.Findings)
	}
	again, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if string(again) != string(fixed) {
		t.Errorf("second --fix rewrote the exclude file:\nfirst:\n%s\nsecond:\n%s", fixed, again)
	}
	for _, name := range []string{gitops.WriteLockFile, ".slow-writes"} {
		if n := strings.Count(string(again), "/"+name+"\n"); n != 1 {
			t.Errorf("exclude lists /%s %d times, want 1:\n%s", name, n, again)
		}
	}
}
