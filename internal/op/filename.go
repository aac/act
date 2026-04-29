package op

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// isoLayout is the fixed-width 24-char ISO-8601 millisecond UTC layout used
// for op filenames per spec §Op file naming (`YYYY-MM-DDTHH:MM:SS.sssZ`).
const isoLayout = "2006-01-02T15:04:05.000Z"

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
	iso := time.UnixMilli(env.HLC.Wall).UTC().Format(isoLayout)
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

// atomicWrite writes body to a temp file in dir and renames it onto target.
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
	return nil
}

// filenameRe validates the on-disk op filename layout. The hash component
// is permitted to be 8, 12, or 16 lowercase hex chars (the three lengths
// the writer probes). The op_type must be one of the 12 known op types,
// matched as a longest-prefix alternation; we capture it as a generic
// underscore-separated lowercase token and validate against ValidOpTypes.
var filenameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z)-([0-9a-f]+)-([a-z_]+)\.json$`)

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
	t, perr := time.ParseInLocation(isoLayout, iso, time.UTC)
	if perr != nil {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: timestamp: %w", base, perr)
	}
	if !strings.HasSuffix(base, ".json") {
		return time.Time{}, "", "", fmt.Errorf("op: parse %q: missing .json suffix", base)
	}
	return t, hashHex, op, nil
}
