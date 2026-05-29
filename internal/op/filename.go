package op

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ErrPersistenceUnconfirmed is returned by ProbeAndWrite when the op file
// was written and renamed into place but a post-write read-back could not
// confirm the bytes are durably present and byte-identical to what was
// written. This is the load-bearing fail-loud guarantee (act-40fce0): an op
// write that cannot be confirmed to have landed where `act harvest` will see
// it MUST surface a loud error and a non-zero exit, never a synthetic
// success. Silent success that later vanishes is the worst failure mode —
// the orchestrate-worker silent-data-loss bug (a worker `act create` that
// returned "Created act-XXXXXX" but whose op file was never observable by a
// later `act harvest`) is exactly this class.
//
// Callers translate this to a `write_failed`-class error envelope (exit 1).
var ErrPersistenceUnconfirmed = errors.New("op: write succeeded but read-back could not confirm persistence")

// IsoLayout is the fixed-width 24-char NTFS-safe variant of the ISO-8601
// millisecond UTC layout used for on-disk filenames per spec §Op file
// naming (`YYYY-MM-DDTHH-MM-SS.sssZ`).
//
// The time component uses '-' (not ':') because ':' is reserved in NTFS
// paths, breaking `git checkout` on Windows hosts before any Go code runs
// (act-2f3d). The substitution preserves total width (24), lexical sort
// order, and the surrounding fields; only the three separators between
// hours/minutes/seconds change. The HLC envelope JSON body still uses the
// canonical colon form — only the filename varies.
//
// Old-form names (with ':') remain readable: Parse accepts both layouts
// (forward-only fix; existing ops are append-only and stay valid).
//
// Exported so adjacent writers that need an NTFS-safe filename (the
// importer's `.act/imports/<iso>.json` files from act-561c63 and the
// compact tombstones from act-d5d1ff) reuse the constant rather than
// duplicating the layout string.
const IsoLayout = "2006-01-02T15-04-05.000Z"

// isoLayoutLegacy is the pre-act-2f3d form. Retained for Parse so that
// existing op files (committed before this change) keep folding cleanly.
// Writers no longer emit this layout.
const isoLayoutLegacy = "2006-01-02T15:04:05.000Z"

// shardLayout is the year-month directory format derived from the HLC wall.
const shardLayout = "2006-01"

// ErrOpHashCollision is returned when all three configured filename hash
// lengths (8, 12, 16) collide with existing files in the target shard. Per
// spec §Op file naming this is statistically impossible barring a sha256
// break.
var ErrOpHashCollision = errors.New("op: filename hash collision past 16 hex chars")

// ShardDir returns the on-disk shard directory for an op of the given issue
// and HLC wall (in unix milliseconds UTC). The wall is interpreted as UTC
// per the HLC contract; the resulting path is `<rootOps>/<issueID>/<YYYY-MM>/`.
//
// rootOps is the ops root (typically `<repo>/.act/ops`). The trailing
// separator is included so callers can append a filename directly, but
// filepath.Join callers should not depend on it.
func ShardDir(rootOps string, issueID string, hlcWallMs int64) string {
	t := time.UnixMilli(hlcWallMs).UTC()
	return filepath.Join(rootOps, issueID, t.Format(shardLayout)) + string(filepath.Separator)
}

// Filename returns the op filename `<iso8601-millis>-<8hex>-<op-type>.json`
// for env, using the 8-char prefix of the canonical envelope hash. Callers
// that need the collision-extended form (12 or 16 hex chars) must use
// ProbeAndWrite or build the filename via filenameWithLen.
//
// Filename does not consult the filesystem; ProbeAndWrite is the function
// that picks the final hash length.
func Filename(env Envelope) string {
	name, err := filenameWithLen(env, 8)
	if err != nil {
		// Fall back to a sentinel; Filename has no error return per the
		// caller contract. A malformed envelope (which is what would cause
		// hashing to fail) is the caller's bug, not a runtime path.
		return fmt.Sprintf("INVALID-%s.json", err.Error())
	}
	return name
}

// filenameWithLen constructs an op filename with a hash prefix of exactly
// hashLen hex characters. hashLen must be one of 8, 12, 16.
func filenameWithLen(env Envelope, hashLen int) (string, error) {
	full, err := env.FullHash()
	if err != nil {
		return "", err
	}
	if hashLen <= 0 || hashLen > len(full) {
		return "", fmt.Errorf("op: filename hash length %d out of range", hashLen)
	}
	iso := time.UnixMilli(env.HLC.Wall).UTC().Format(IsoLayout)
	return fmt.Sprintf("%s-%s-%s.json", iso, full[:hashLen], env.OpType), nil
}

// ProbeAndWrite writes env's body bytes to the issue/month shard derived
// from env.HLC.Wall, picking a filename hash length (8, 12, or 16 hex chars)
// that does not collide with an existing file in the shard.
//
// The fsLock callback is invoked once at entry and the returned release
// callback is invoked on return; ProbeAndWrite is agnostic to the locking
// mechanism (typically `.act/.lock`). The shard directory is created via
// os.MkdirAll(0o755) before probing.
//
// The op file is written atomically via a temp file in the same directory
// followed by os.Rename. The temp file is removed on any error path so a
// failed write leaves no garbage in the shard.
//
// Returns the absolute final path and the hash length actually used (8, 12,
// or 16). Returns ErrOpHashCollision if all three lengths collide.
func ProbeAndWrite(rootOps string, env Envelope, body []byte, fsLock func() (release func(), err error)) (path string, finalHashLen int, err error) {
	if fsLock == nil {
		return "", 0, fmt.Errorf("op: ProbeAndWrite: fsLock is nil")
	}
	release, err := fsLock()
	if err != nil {
		return "", 0, fmt.Errorf("op: ProbeAndWrite: lock: %w", err)
	}
	defer release()

	shard := ShardDir(rootOps, env.IssueID, env.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		return "", 0, fmt.Errorf("op: ProbeAndWrite: mkdir: %w", err)
	}

	for _, hashLen := range []int{8, 12, 16} {
		name, err := filenameWithLen(env, hashLen)
		if err != nil {
			return "", 0, fmt.Errorf("op: ProbeAndWrite: filename: %w", err)
		}
		candidate := filepath.Join(shard, name)
		_, statErr := os.Stat(candidate)
		if statErr == nil {
			// Collision: try the next length.
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", 0, fmt.Errorf("op: ProbeAndWrite: stat %s: %w", candidate, statErr)
		}
		// Free slot: write atomically.
		if err := atomicWrite(shard, candidate, body); err != nil {
			return "", 0, err
		}
		return candidate, hashLen, nil
	}
	return "", 0, ErrOpHashCollision
}

// atomicWrite writes body to a temp file in dir and renames it onto target,
// then guarantees the write is durably persisted and observable:
//
//  1. write body to a temp file in the same directory,
//  2. fsync the temp file (data + metadata),
//  3. rename it onto target (atomic on POSIX within a filesystem),
//  4. fsync the containing directory so the rename itself is durable
//     (a rename can otherwise be lost across a crash even though the
//     file's contents were fsynced),
//  5. read the target file back and verify the bytes are byte-identical
//     to body.
//
// Step 5 is the load-bearing fail-loud check (act-40fce0): if the read-back
// fails or the bytes differ, the function returns ErrPersistenceUnconfirmed
// so the calling command surfaces a loud error rather than a synthetic
// success. This closes the orchestrate-worker silent-data-loss class where
// a write "succeeded" but the op never landed where `act harvest` could see
// it.
//
// On any error the temp file is removed so no `.tmp` file is left behind.
func atomicWrite(dir, target string, body []byte) error {
	f, err := os.CreateTemp(dir, ".op-*.json.tmp")
	if err != nil {
		return fmt.Errorf("op: ProbeAndWrite: create temp: %w", err)
	}
	tmpName := f.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("op: ProbeAndWrite: write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("op: ProbeAndWrite: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("op: ProbeAndWrite: close: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("op: ProbeAndWrite: rename: %w", err)
	}
	// Fsync the containing directory so the rename (a directory-entry
	// mutation) is itself durable. Without this a crash after a successful
	// content fsync can still lose the file because the directory block
	// hadn't been flushed. Best-effort: not every platform/filesystem lets
	// you open a directory for fsync, and a failure here is not by itself
	// proof the rename was lost — the read-back below is the authoritative
	// confirmation, so a dir-fsync error is non-fatal.
	syncDir(dir)
	// Read-back verification — the fail-loud guarantee. The op MUST be
	// observable at `target` with exactly the bytes we wrote, or we refuse
	// to claim success. readBackForTest is a test seam (defaults to
	// os.ReadFile) so the unconfirmed-persistence branch is deterministically
	// exercisable.
	got, rerr := readBackForTest(target)
	if rerr != nil {
		// The file we just renamed into place is not readable. This is the
		// silent-loss signature: the write path believed it succeeded but
		// the op is not where a reader (fold / harvest) will find it.
		_ = os.Remove(target)
		return fmt.Errorf("%w: read-back of %s: %v", ErrPersistenceUnconfirmed, target, rerr)
	}
	if !bytes.Equal(got, body) {
		_ = os.Remove(target)
		return fmt.Errorf("%w: read-back of %s returned %d bytes, expected %d",
			ErrPersistenceUnconfirmed, target, len(got), len(body))
	}
	return nil
}

// readBackForTest is the read-back used by atomicWrite's persistence
// confirmation. It defaults to os.ReadFile; tests override it to inject a
// failed or mismatched read-back and exercise the ErrPersistenceUnconfirmed
// branch deterministically. Production code never reassigns it.
var readBackForTest = os.ReadFile

// syncDir best-effort fsyncs a directory so a preceding rename into it is
// durable. Errors are swallowed: directory fsync is unsupported on some
// platforms (notably Windows) and the read-back in atomicWrite is the
// authoritative persistence check, not this.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// filenameRe validates the on-disk op filename layout. The hash component
// is permitted to be 8, 12, or 16 lowercase hex chars (the three lengths
// the writer probes). The op_type must be one of the 12 known op types,
// matched as a longest-prefix alternation; we capture it as a generic
// underscore-separated lowercase token and validate against ValidOpTypes.
//
// The time-component separators may be '-' (current NTFS-safe form per
// act-2f3d) or ':' (legacy pre-2f3d form). Both layouts are accepted so
// existing op files on disk keep parsing; new ops are written in the
// NTFS-safe form only. The date and the seconds/millis separator stay
// fixed (date always uses '-', millis always uses '.').
var filenameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}[:\-]\d{2}[:\-]\d{2}\.\d{3}Z)-([0-9a-f]+)-([a-z_]+)\.json$`)

// Parse is the inverse of Filename. It validates the filename layout and
// returns the parsed wall timestamp, the hash component (8/12/16 hex
// chars), and the op type. Used at fold time to extract the wall and
// op-type without unmarshaling the envelope.
//
// Parse accepts either a bare basename or a path; only the basename is
// inspected.
func Parse(filename string) (timestamp time.Time, opHash string, opType string, err error) {
	base := filepath.Base(filename)
	m := filenameRe.FindStringSubmatch(base)
	if m == nil {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: malformed filename", base)
	}
	iso, hashHex, op := m[1], m[2], m[3]
	switch len(hashHex) {
	case 8, 12, 16:
	default:
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: hash length %d not in {8,12,16}", base, len(hashHex))
	}
	if !ValidOpTypes[op] {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: unknown op_type %q", base, op)
	}
	t, perr := time.ParseInLocation(IsoLayout, iso, time.UTC)
	if perr != nil {
		// Fall back to the legacy colon form. Existing op files on disk
		// (pre-act-2f3d) use ':' in the time component; the layouts are
		// otherwise byte-identical so a single retry covers it.
		t, perr = time.ParseInLocation(isoLayoutLegacy, iso, time.UTC)
	}
	if perr != nil {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: timestamp: %w", base, perr)
	}
	if !strings.HasSuffix(base, ".json") {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: missing .json suffix", base)
	}
	return t, hashHex, op, nil
}
