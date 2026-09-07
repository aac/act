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
	"strings"
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
	sigs, err := OpsSignatures(opsRoot)
	if err != nil {
		return "", err
	}
	return sigs.Tree, nil
}

// Signatures is one metadata walk of the op tree, reported at both
// granularities the read path needs.
//
// Tree is exactly what OpsSignature returns: the whole-tree key that answers
// "has anything changed at all?". PerIssue answers the finer question
// act-50d2e2 needs — "which issues changed?" — so a read that follows a write
// can refold the one issue whose ops moved instead of the whole log. Both are
// derived from the same entry list in the same walk, so they can never
// describe different moments in a way that lets the coarse key say "unchanged"
// while a per-issue key disagrees.
//
// PerIssue is keyed by the top-level directory name under opsRoot — the issue
// id, since op.ShardDir writes every op to `<opsRoot>/<issue_id>/<yyyy-mm>/`.
//
// Decomposable reports whether every regular file in the tree lives under such
// a directory. A file sitting directly in opsRoot belongs to no issue, so the
// per-issue map would not account for the whole tree; callers must fall back
// to a whole-tree rebuild when this is false rather than trust a partial map.
type Signatures struct {
	Tree         string
	PerIssue     map[string]string
	Decomposable bool
}

// OpsSignatures performs the metadata walk described on OpsSignature once and
// returns both the whole-tree key and a per-issue key for each issue
// directory. See Signatures for the contract, and OpsSignature for why
// (path, size, mtime) is a sound staleness key for act's immutable op files.
func OpsSignatures(opsRoot string) (Signatures, error) {
	out := Signatures{PerIssue: map[string]string{}, Decomposable: true}

	info, err := os.Stat(opsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			out.Tree = hashOf(signatureVersion + "\x00EMPTY")
			return out, nil
		}
		return Signatures{}, fmt.Errorf("fold: stat %s: %w", opsRoot, err)
	}
	if !info.IsDir() {
		return Signatures{}, fmt.Errorf("fold: %s: not a directory", opsRoot)
	}

	var entries []string
	perIssue := map[string][]string{}
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
		rel = filepath.ToSlash(rel)
		entry := rel +
			"\x00" + strconv.FormatInt(fi.Size(), 10) +
			"\x00" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
		entries = append(entries, entry)
		if i := strings.Index(rel, "/"); i > 0 {
			id := rel[:i]
			perIssue[id] = append(perIssue[id], entry)
		} else {
			// A regular file directly under opsRoot: no issue owns it.
			out.Decomposable = false
		}
		return nil
	})
	if walkErr != nil {
		return Signatures{}, fmt.Errorf("fold: signature walk %s: %w", opsRoot, walkErr)
	}

	out.Tree = hashEntries(entries)
	for id, ents := range perIssue {
		out.PerIssue[id] = hashEntries(ents)
	}
	// An issue directory holding no regular files still exists as a state the
	// index has to agree with (a fold of it yields no row), so record it with
	// the stable empty key rather than letting it look like a deletion.
	dirs, derr := os.ReadDir(opsRoot)
	if derr != nil {
		return Signatures{}, fmt.Errorf("fold: read %s: %w", opsRoot, derr)
	}
	for _, e := range dirs {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, ok := out.PerIssue[e.Name()]; !ok {
			out.PerIssue[e.Name()] = hashEntries(nil)
		}
	}
	return out, nil
}

// hashEntries hashes a set of walk entries under the signature version. The
// entries are sorted here so callers need not; the same function produces the
// whole-tree key and each per-issue key, which is what keeps the two
// granularities in the same format.
func hashEntries(entries []string) string {
	sorted := append([]string(nil), entries...)
	sort.Strings(sorted)
	h := sha256.New()
	h.Write([]byte(signatureVersion))
	h.Write([]byte{'\n'})
	for _, e := range sorted {
		h.Write([]byte(e))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
