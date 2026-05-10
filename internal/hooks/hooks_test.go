package hooks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeScript writes a shell script to dir/name with mode and returns its
// absolute path. Skips the test on Windows because the embedded
// `#!/bin/sh` shebang is not honored there.
func writeScript(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script hooks not supported on windows")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestResolveHookMapsOpType(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not honored on windows")
	}
	dir := t.TempDir()
	// Hook filenames mirror the op-type 1:1 (act-8277 renamed from
	// the historical post-<op> form so it matches every doc and the
	// existing .act/hooks/close gate in the act repo).
	for opType, fname := range map[string]string{
		"create": "create",
		"close":  "close",
		"claim":  "claim",
	} {
		want := writeScript(t, dir, fname, "#!/bin/sh\nexit 0\n", 0o755)
		got, ok := ResolveHook(dir, opType)
		if !ok || got != want {
			t.Errorf("ResolveHook(%q) = (%q, %t); want (%q, true)", opType, got, ok, want)
		}
	}
}

// TestResolveHookMatchesDocs is the regression test for act-8277. The
// docs (CLAUDE.md, the act skill, `act help workflow`) all promise that
// an executable `.act/hooks/close` runs on every close. The original
// recognized map used `post-close` and silently no-op'd. This test
// pins the contract at the doc-quoted name so the bug can't return.
func TestResolveHookMatchesDocs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not honored on windows")
	}
	dir := t.TempDir()
	want := writeScript(t, dir, "close", "#!/bin/sh\nexit 0\n", 0o755)
	got, ok := ResolveHook(dir, "close")
	if !ok || got != want {
		t.Fatalf("ResolveHook(\"close\") = (%q, %t); want (%q, true) — the doc-quoted .act/hooks/close filename must resolve", got, ok, want)
	}
}

func TestResolveHookUnknownOpType(t *testing.T) {
	dir := t.TempDir()
	// Even if a file exists, unknown op-types should not resolve.
	writeScript(t, dir, "create", "#!/bin/sh\nexit 0\n", 0o755)
	for _, opType := range []string{"update_field", "redact", "import", "", "post-fold"} {
		if path, ok := ResolveHook(dir, opType); ok {
			t.Errorf("ResolveHook(%q) = (%q, true); want (\"\", false)", opType, path)
		}
	}
}

func TestResolveHookNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits not honored on windows")
	}
	dir := t.TempDir()
	writeScript(t, dir, "create", "#!/bin/sh\nexit 0\n", 0o644)
	if path, ok := ResolveHook(dir, "create"); ok {
		t.Fatalf("ResolveHook returned (%q, true) for non-executable hook", path)
	}
}

func TestResolveHookAbsent(t *testing.T) {
	dir := t.TempDir()
	if path, ok := ResolveHook(dir, "create"); ok {
		t.Fatalf("ResolveHook returned (%q, true) for absent hook", path)
	}
}

func TestRunSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("`true` unavailable on windows")
	}
	// Resolve `true` via $PATH so the test works across distros: macOS
	// puts it at /usr/bin/true; Linux at /bin/true. Hardcoding either
	// fails on the other (this regressed silently when origin verification
	// ran on Linux).
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not on PATH: %v", err)
	}
	ctx := HookContext{
		OpID:    "op123",
		OpType:  "create",
		IssueID: "act-deadbeef",
		Phase:   PhasePreCommitOp,
		OpJSON:  []byte(`{"hello":"world"}`),
	}
	if err := Run(ctx, truePath, 5*time.Second); err != nil {
		t.Fatalf("Run(%s): %v", truePath, err)
	}
}

func TestRunNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "fail.sh", "#!/bin/sh\nexit 1\n", 0o755)
	err := Run(HookContext{Phase: PhasePreCommitOp}, script, 5*time.Second)
	if err == nil {
		t.Fatalf("Run: want error, got nil")
	}
	var herr *HookFailedError
	if !errors.As(err, &herr) {
		t.Fatalf("Run: want *HookFailedError, got %T (%v)", err, err)
	}
	if herr.Code != 1 {
		t.Errorf("Code = %d; want 1", herr.Code)
	}
	if herr.Cause != "exit" {
		t.Errorf("Cause = %q; want \"exit\"", herr.Cause)
	}
}

func TestRunStderrCaptured(t *testing.T) {
	dir := t.TempDir()
	// Print exactly 100 bytes to stderr, then exit 1 so the tail is
	// surfaced. The body is 100 'a' chars.
	body := "#!/bin/sh\nprintf '%0.s' " + strings.Repeat("a", 0) + "\n" // placeholder
	_ = body
	hundred := strings.Repeat("a", 100)
	script := writeScript(t, dir, "stderr.sh",
		"#!/bin/sh\nprintf '%s' '"+hundred+"' 1>&2\nexit 1\n", 0o755)
	err := Run(HookContext{Phase: PhasePreCommitOp}, script, 5*time.Second)
	var herr *HookFailedError
	if !errors.As(err, &herr) {
		t.Fatalf("Run: want *HookFailedError, got %T (%v)", err, err)
	}
	if herr.StderrTail != hundred {
		t.Errorf("StderrTail = %q (len %d); want 100 a's", herr.StderrTail, len(herr.StderrTail))
	}
	if herr.Truncated {
		t.Errorf("Truncated = true; want false (only 100 bytes of stderr)")
	}
}

func TestRunStderrTruncated(t *testing.T) {
	dir := t.TempDir()
	// Write 70KB of 'b' to stderr to force overflow past the 64KB cap
	// and then exit 1.
	script := writeScript(t, dir, "big.sh",
		"#!/bin/sh\nyes b | tr -d '\\n' | head -c 71680 1>&2\nexit 1\n", 0o755)
	err := Run(HookContext{Phase: PhasePreCommitOp}, script, 10*time.Second)
	var herr *HookFailedError
	if !errors.As(err, &herr) {
		t.Fatalf("Run: want *HookFailedError, got %T (%v)", err, err)
	}
	if len(herr.StderrTail) != stderrTailMax {
		t.Errorf("StderrTail len = %d; want %d", len(herr.StderrTail), stderrTailMax)
	}
	if !herr.Truncated {
		t.Errorf("Truncated = false; want true (>64KB written)")
	}
	// Tail should be all 'b'.
	if strings.Trim(herr.StderrTail, "b") != "" {
		t.Errorf("StderrTail contains non-'b' bytes")
	}
}

func TestRunTimeout(t *testing.T) {
	dir := t.TempDir()
	// sleep 30s — well past the 200ms timeout.
	script := writeScript(t, dir, "slow.sh", "#!/bin/sh\nsleep 30\n", 0o755)
	start := time.Now()
	err := Run(HookContext{Phase: PhasePreCommitOp}, script, 200*time.Millisecond)
	elapsed := time.Since(start)
	var herr *HookFailedError
	if !errors.As(err, &herr) {
		t.Fatalf("Run: want *HookFailedError, got %T (%v)", err, err)
	}
	if herr.Cause != "timeout" {
		t.Errorf("Cause = %q; want \"timeout\"", herr.Cause)
	}
	// Must have killed before the 30s natural exit.
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v; expected fast kill", elapsed)
	}
}

func TestRunEnvVars(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	// Hook writes the four ACT_* vars to a file we then read.
	script := writeScript(t, dir, "env.sh",
		"#!/bin/sh\n"+
			"{\n"+
			"  printf 'OP_ID=%s\\n' \"$ACT_OP_ID\"\n"+
			"  printf 'OP_TYPE=%s\\n' \"$ACT_OP_TYPE\"\n"+
			"  printf 'ISSUE_ID=%s\\n' \"$ACT_ISSUE_ID\"\n"+
			"  printf 'PHASE=%s\\n' \"$ACT_HOOK_PHASE\"\n"+
			"} > "+out+"\n"+
			"exit 0\n", 0o755)
	ctx := HookContext{
		OpID:    "op-abc",
		OpType:  "create",
		IssueID: "act-feedface",
		Phase:   PhasePreCommitOp,
	}
	if err := Run(ctx, script, 5*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	want := "OP_ID=op-abc\nOP_TYPE=create\nISSUE_ID=act-feedface\nPHASE=pre-commit-op\n"
	if string(got) != want {
		t.Errorf("env output:\n got: %q\nwant: %q", got, want)
	}
}

func TestRunStdinReceived(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdin.bin")
	script := writeScript(t, dir, "stdin.sh",
		"#!/bin/sh\ncat > "+out+"\nexit 0\n", 0o755)
	payload := []byte(`{"a":1,"b":"two"}`)
	ctx := HookContext{
		Phase:  PhasePreCommitOp,
		OpJSON: payload,
	}
	if err := Run(ctx, script, 5*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("stdin payload: got %q want %q", got, payload)
	}
}

func TestStderrTailUTF8Trim(t *testing.T) {
	// Build a buffer ending with a partial multi-byte rune; the trim
	// should drop the orphaned continuation bytes.
	b := []byte("hello, ")
	b = append(b, 0xE4, 0xB8) // first 2 bytes of a 3-byte rune (U+4E2D)
	got, _ := stderrTail(b)
	if string(got) != "hello, " {
		t.Errorf("stderrTail trim: got %q want %q", got, "hello, ")
	}
}
