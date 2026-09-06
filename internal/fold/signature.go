package fold

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// signatureVersion is mixed into every OpsSignature. Bump it whenever the
// entry format below changes so an index built by an older binary can never
// match a signature computed by a newer one.
const signatureVersion = "act-ops-sig-v1"

// OpsSignature returns a cheap staleness key for the op tree at opsRoot: a
// hash over each regular file's (relative path, size, mtime) triple, with no
// file contents read.
//
// It exists because the read path needs to answer "has `.act/ops/` changed
// since the index was built?" far more often than it needs to fold. On a
// 4,300-op store the metadata walk costs ~40ms against ~800ms for a full
// fold-and-rebuild and ~140ms for a content hash of the same tree
// (act-43d11f).
//
// # Why metadata is sound here, where it usually is not
//
// The generic objection to stat-based cache keys is in-place rewrites: a file
// whose contents change while its size and mtime do not. act's op files are
// immutable once written and their basenames embed the op hash
// (`<iso>-<hash8>-<optype>.json`, see op.FileName), so a given path carries
// one and only one op body. Every way the op log actually moves therefore
// changes the path set or a file's stat data:
//
//   - a writer appends an op — new path;
//   - a rollback removes an op (the act-fec192 shape) — path disappears;
//   - act-sync pulls, rebases, or resets the nested `.act/` repo — git writes
//     through a temp file and renames, so both mtime and size follow the new
//     content.
//
// The one shape it cannot see is a same-size rewrite landing inside the
// filesystem's mtime resolution. Nothing in act produces that, and the
// consequence of the design is bounded in the safe direction everywhere else:
// see Index.EnsureCurrent, which computes the signature BEFORE folding, so a
// concurrent write racing the fold can only cause an extra rebuild, never a
// skipped one.
//
// A missing opsRoot yields the stable empty-tree signature, so cold start is
// well defined.
func OpsSignature(opsRoot string) (string, error) {
	info, err := os.Stat(opsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return hashOf(signatureVersion + "\x00EMPTY"), nil
		}
		return "", fmt.Errorf("fold: stat %s: %w", opsRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fold: %s: not a directory", opsRoot)
	}

	var entries []string
	walkErr := filepath.WalkDir(opsRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			// A file that vanished mid-walk is a concurrent writer at
			// work, not a broken tree. Skip it: the entry set we end up
			// hashing simply describes a different moment, and the next
			// read recomputes.
			if errors.Is(ierr, fs.ErrNotExist) {
				return nil
			}
			return ierr
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(opsRoot, path)
		if rerr != nil {
			return rerr
		}
		entries = append(entries, filepath.ToSlash(rel)+
			"\x00"+strconv.FormatInt(fi.Size(), 10)+
			"\x00"+strconv.FormatInt(fi.ModTime().UnixNano(), 10))
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("fold: signature walk %s: %w", opsRoot, walkErr)
	}

	sort.Strings(entries)
	h := sha256.New()
	h.Write([]byte(signatureVersion))
	h.Write([]byte{'\n'})
	for _, e := range entries {
		h.Write([]byte(e))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
