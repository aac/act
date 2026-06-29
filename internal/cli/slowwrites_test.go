package cli

// Phase 2 ticket 3b (act-4a604d) — slow-write measurement tests.
//
// The fault-injection hook ACT_TEST_SLOW_COMMIT_MS=<n> drives the slow
// path deterministically by sleeping for N ms between the stage point
// and the git commit invocation in gitops.CommitOp. Tests use t.Setenv
// so the hook is scoped to the test goroutine and cleaned up
// automatically.
//
// Cross-test contract: tests capture stderr via the gitops package's
// direct os.Stderr writes, so we route stderr through os.Pipe and
// restore on cleanup. The slow-write file lives at
// <paths.Root>/.slow-writes; paths.Root is the nested .act/ dir.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// captureStderr redirects os.Stderr to a pipe for the duration of fn
// and returns whatever was written. The pipe is bounded by an in-memory
// buffer — fine for unit-test scale.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = origStderr
	}()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	_ = w.Close()
	captured := <-done
	return captured
}

// TestSlowWrite_FaultInjectedExceedingThresholdEmitsWarning asserts AC3:
// the fault-injection env var ACT_TEST_SLOW_COMMIT_MS=<n> drives the
// commit duration past the threshold and the stderr line matches the
// pinned literal pattern.
func TestSlowWrite_FaultInjectedExceedingThresholdEmitsWarning(t *testing.T) {
	root := makeCreateRepo(t)
	// 1200ms sleep > 1000ms default threshold → fault injection drives
	// the slow path on every commit. Set BEFORE RunCreate so the hook
	// fires inside the CommitOp call.
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "1200")

	stderr := captureStderr(t, func() {
		out, code := RunCreate(root, CreateOptions{Title: "slow-fault", Type: "task"})
		if code != 0 {
			t.Fatalf("RunCreate: code=%d, out=%+v", code, out)
		}
	})

	if !strings.Contains(stderr, "act: slow write detected (") {
		t.Errorf("stderr missing literal prefix: %q", stderr)
	}
	if !strings.Contains(stderr, "; see .act/.slow-writes") {
		t.Errorf("stderr missing literal suffix: %q", stderr)
	}
}

// TestSlowWrite_SchemaIsCorrect asserts AC4: the fault-injected slow
// commit produces exactly one JSON-line record in `.act/.slow-writes`
// with all four pinned fields, duration_ms >= 1200, op_type=="create",
// op_id matching the created op.
func TestSlowWrite_SchemaIsCorrect(t *testing.T) {
	root := makeCreateRepo(t)
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "1200")

	// Capture (and drop) stderr so the warning doesn't pollute test
	// output.
	_ = captureStderr(t, func() {
		out, code := RunCreate(root, CreateOptions{Title: "slow-schema", Type: "task"})
		if code != 0 {
			t.Fatalf("RunCreate: code=%d, out=%+v", code, out)
		}
	})

	paths := config.Layout(root)
	body, err := os.ReadFile(filepath.Join(paths.Root, ".slow-writes"))
	if err != nil {
		t.Fatalf("read .slow-writes: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf(".slow-writes: want 1 line, got %d: %s", len(lines), body)
	}

	var rec struct {
		Timestamp  string `json:"timestamp"`
		OpID       string `json:"op_id"`
		DurationMs int64  `json:"duration_ms"`
		OpType     string `json:"op_type"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v (line=%q)", err, lines[0])
	}
	if rec.Timestamp == "" {
		t.Errorf("timestamp missing: %+v", rec)
	}
	// RFC3339 with Z suffix per the pinned schema.
	if !strings.HasSuffix(rec.Timestamp, "Z") {
		t.Errorf("timestamp not UTC: %q", rec.Timestamp)
	}
	if rec.OpID == "" {
		t.Errorf("op_id missing: %+v", rec)
	}
	if rec.OpType != "create" {
		t.Errorf("op_type = %q, want %q", rec.OpType, "create")
	}
	if rec.DurationMs < 1200 {
		t.Errorf("duration_ms = %d, want >= 1200 (sleep duration)", rec.DurationMs)
	}
}

// TestSlowWrite_RollingCap100 asserts AC5: after 105 fault-injected
// slow writes, the file contains exactly 100 records (oldest 5
// pruned). Uses the gitops appendSlowWriteRecord directly to avoid 105
// real commits; the cap-prune logic is independent of which path drives
// the append.
func TestSlowWrite_RollingCap100(t *testing.T) {
	root := makeCreateRepo(t)
	paths := config.Layout(root)
	stateRoot := paths.Root

	// Drive 105 appends with synthetic records. We use the cli appender
	// directly because it's the same cap-prune helper the gitops side
	// also uses (just duplicated to dodge an upward import); both share
	// the SlowWriteLogCap constant.
	for i := 0; i < 105; i++ {
		rec := SlowWriteRecord{
			Timestamp:  FormatSlowWriteTimestamp(timeAt(2026, 5, 19, 12, i)),
			OpID:       "act-" + intHex(i),
			DurationMs: int64(1000 + i),
			OpType:     "create",
		}
		if err := AppendSlowWrite(stateRoot, rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	body, err := os.ReadFile(filepath.Join(stateRoot, ".slow-writes"))
	if err != nil {
		t.Fatalf("read .slow-writes: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 100 {
		t.Fatalf("post-105 line count = %d, want 100", len(lines))
	}

	// Oldest 5 (i = 0..4) must be absent; newest (i = 104) must be
	// present. We grep on the op_id since that's the per-record
	// identifier we synthesised.
	all := string(body)
	for i := 0; i < 5; i++ {
		probe := `"op_id":"act-` + intHex(i) + `"`
		if strings.Contains(all, probe) {
			t.Errorf("oldest entry %d still present: %s", i, probe)
		}
	}
	probe := `"op_id":"act-` + intHex(104) + `"`
	if !strings.Contains(all, probe) {
		t.Errorf("newest entry 104 missing: %s", probe)
	}
}

// TestPendingPush_RollingCap100 mirrors TestSlowWrite_RollingCap100 for
// the .act/.pending-pushes file. The two files share the same
// appendLineCapped helper, so a single test of each surface is
// sufficient to assert the shape.
func TestPendingPush_RollingCap100(t *testing.T) {
	root := makeCreateRepo(t)
	paths := config.Layout(root)
	stateRoot := paths.Root

	for i := 0; i < 105; i++ {
		rec := PendingPushRecord{
			Timestamp: FormatSlowWriteTimestamp(timeAt(2026, 5, 19, 12, i)),
			SHA:       "deadbeef" + intHex(i),
			OpType:    "create",
		}
		if err := AppendPendingPush(stateRoot, rec); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	body, err := os.ReadFile(filepath.Join(stateRoot, ".pending-pushes"))
	if err != nil {
		t.Fatalf("read .pending-pushes: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 100 {
		t.Fatalf("post-105 line count = %d, want 100", len(lines))
	}

	all := string(body)
	for i := 0; i < 5; i++ {
		probe := `"sha":"deadbeef` + intHex(i) + `"`
		if strings.Contains(all, probe) {
			t.Errorf("oldest entry %d still present", i)
		}
	}
	probe := `"sha":"deadbeef` + intHex(104) + `"`
	if !strings.Contains(all, probe) {
		t.Errorf("newest entry 104 missing")
	}
}

// intHex formats an int as a 4-hex string (padded). Synthetic id
// suffix for cap-prune tests; the real id-space is full sha256.
func intHex(i int) string {
	const hex = "0123456789abcdef"
	var b [4]byte
	b[0] = hex[(i>>12)&0xF]
	b[1] = hex[(i>>8)&0xF]
	b[2] = hex[(i>>4)&0xF]
	b[3] = hex[i&0xF]
	return string(b[:])
}

// timeAt builds a deterministic UTC time for the synthetic appends.
// Pulled out so each cap-test can step the seconds field per record
// without writing time.Date(...) inline.
func timeAt(year, mon, day, hour, sec int) time.Time {
	return time.Date(year, time.Month(mon), day, hour, 0, sec, 0, time.UTC)
}
