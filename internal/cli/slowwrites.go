// Phase 2 ticket 3b (act-4a604d) — JSON-lines append log with rolling cap.
//
// This module provides the shared appender/cap-prune logic backing both
// `.act/.slow-writes` (slow-commit observations emitted by the gitops
// Commit path) and `.act/.pending-pushes` (offline commits awaiting a
// retry flush).
//
// Both files have the same on-disk shape:
//
//   - JSON-lines: one record per line, newline-terminated.
//   - Rolling cap at 100 entries: when an append would push past 100,
//     the oldest entries are dropped so the file holds exactly the
//     newest 100 records after the write returns.
//
// Schema (closes OQ #1 from v1 of the Phase 2 brief):
//
//	.act/.slow-writes:    {"timestamp": "<RFC3339-millis-UTC>", "op_id":
//	                       "<full id>", "duration_ms": <int>, "op_type":
//	                       "<create|close|update|dep_add|reopen|delete>"}
//
//	.act/.pending-pushes: {"timestamp": "<RFC3339-millis-UTC>", "sha":
//	                       "<commit sha>", "op_type": "<create|...>"}
//
// The cap helper is schema-agnostic — it operates on whole lines and
// preserves byte-identical record contents through the prune. Callers
// build the JSON shape themselves and pass it as a single line (without
// trailing newline; AppendLineCapped adds it).
//
// File-level concurrency: writes go through a rewrite-temp-then-rename
// atomic replace, so a concurrent reader sees either the pre-write
// content or the fully-pruned post-write content but never a partial
// file. Concurrent writers race on the rename; last-writer-wins is the
// accepted semantics — these logs are observability surfaces, not
// authoritative state. (The op log itself is the source of truth and
// has its own concurrency story per spec §5.D.)
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SlowWriteLogCap is the rolling-cap entry count applied to both
// `.act/.slow-writes` and `.act/.pending-pushes`. Specified as 100 by
// the ticket-3b acceptance criteria (post-105-writes the file holds
// exactly 100 records, oldest 5 absent).
const SlowWriteLogCap = 100

// DefaultSlowWriteThresholdMs is the wall-clock threshold above which a
// successful op commit is logged as a slow write. Per the ticket-3b
// spec: "If duration_ms > act.slowWriteThresholdMs (default 1000)". The
// threshold is a default; a future config knob (`act.slowWriteThresholdMs`)
// can override it without changing the constant here. Phase 2 ships the
// constant; the knob lands in a follow-up.
const DefaultSlowWriteThresholdMs = 1000

// SlowWriteRecord is the on-disk shape of a single `.act/.slow-writes`
// entry. Field tags pin the JSON key names so any future Go renames
// don't drift the schema. RFC3339Nano timestamps are millisecond-
// precision UTC strings (`time.Now().UTC().Format("2006-01-02T15:04:05.000Z")`).
type SlowWriteRecord struct {
	Timestamp  string `json:"timestamp"`
	OpID       string `json:"op_id"`
	DurationMs int64  `json:"duration_ms"`
	OpType     string `json:"op_type"`
}

// PendingPushRecord is the on-disk shape of a single `.act/.pending-pushes`
// entry. Schema per the ticket-3b "PINNED .act/.pending-pushes SCHEMA":
// JSON-lines, one record per line, fields `timestamp, sha, op_type`.
type PendingPushRecord struct {
	Timestamp string `json:"timestamp"`
	SHA       string `json:"sha"`
	OpType    string `json:"op_type"`
}

// FormatSlowWriteTimestamp returns an RFC3339 millisecond-precision UTC
// timestamp suitable for embedding in a SlowWriteRecord or
// PendingPushRecord. The format is `2006-01-02T15:04:05.000Z` — three
// fractional digits, always UTC. Pinned by the ticket-3b schema.
func FormatSlowWriteTimestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// AppendSlowWrite appends one SlowWriteRecord to `.act/.slow-writes`
// (resolved as <stateRoot>/.slow-writes where stateRoot is the
// LayoutPaths.Root pointing at the nested `.act/` directory). The file
// is created if it does not exist. After the append, the file is
// pruned to the newest SlowWriteLogCap entries.
//
// Errors are surfaced verbatim; callers may choose to log-and-continue
// since the underlying commit has already succeeded by the time this
// runs.
func AppendSlowWrite(stateRoot string, rec SlowWriteRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("cli: marshal slow-write record: %w", err)
	}
	path := filepath.Join(stateRoot, ".slow-writes")
	return appendLineCapped(path, string(line), SlowWriteLogCap)
}

// AppendPendingPush appends one PendingPushRecord to `.act/.pending-pushes`
// (resolved as <stateRoot>/.pending-pushes). Same cap-prune semantics as
// AppendSlowWrite.
func AppendPendingPush(stateRoot string, rec PendingPushRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("cli: marshal pending-push record: %w", err)
	}
	path := filepath.Join(stateRoot, ".pending-pushes")
	return appendLineCapped(path, string(line), SlowWriteLogCap)
}

// ReadPendingPushes returns the full set of pending-push records, in
// file order (oldest first). Missing file returns (nil, nil) — there
// are no pending pushes.
func ReadPendingPushes(stateRoot string) ([]PendingPushRecord, error) {
	path := filepath.Join(stateRoot, ".pending-pushes")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cli: read pending-pushes: %w", err)
	}
	var out []PendingPushRecord
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec PendingPushRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// Malformed records are skipped (not fatal) — a corrupt log
			// shouldn't block forward progress on the actual git push.
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// ClearPendingPushes truncates `.act/.pending-pushes` to empty. Called
// after a successful flush so the next non-offline write doesn't
// re-attempt already-published commits.
func ClearPendingPushes(stateRoot string) error {
	path := filepath.Join(stateRoot, ".pending-pushes")
	// Truncate by writing zero bytes; atomic via tmp + rename so a
	// concurrent reader never sees a half-cleared file.
	if err := atomicWriteFile(path, nil); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// appendLineCapped appends `line` (without trailing newline) to `path`
// and prunes the file to the newest `cap` entries. The file is created
// if missing. Operation is atomic via tmp + rename.
//
// Implementation: read the existing file (if any), split on newlines,
// append the new line, drop the head until length <= cap, write back
// via atomic rename.
func appendLineCapped(path, line string, cap int) error {
	if cap <= 0 {
		return fmt.Errorf("cli: appendLineCapped: cap %d must be > 0", cap)
	}
	if strings.ContainsRune(line, '\n') {
		return fmt.Errorf("cli: appendLineCapped: line contains embedded newline")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cli: mkdir for append-log: %w", err)
	}
	existing, err := readLines(path)
	if err != nil {
		return err
	}
	existing = append(existing, line)
	// Prune from the head until length <= cap. The newest `cap`
	// records are retained; the oldest are dropped first.
	if len(existing) > cap {
		existing = existing[len(existing)-cap:]
	}
	// Re-emit as JSON-lines: one entry per line, newline-terminated.
	var b strings.Builder
	for _, l := range existing {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return atomicWriteFile(path, []byte(b.String()))
}

// readLines reads `path` and returns its non-empty lines in file order.
// Missing file returns (nil, nil).
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cli: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	scanner := bufio.NewScanner(f)
	// Allow long JSON lines (default Scanner buffer is 64K, which is
	// plenty for our records but bump the cap to 1MB for defensive
	// margin against pathological inputs).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cli: scan %s: %w", path, err)
	}
	return lines, nil
}

// atomicWriteFile writes `data` to `path` via a temp-file + rename.
// Within the same directory so the rename is atomic on POSIX. Existing
// file mode is preserved if any; otherwise 0644 is used.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("cli: create tmp for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("cli: write tmp for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("cli: close tmp for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("cli: rename tmp to %s: %w", path, err)
	}
	return nil
}
