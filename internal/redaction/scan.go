package redaction

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// derivedPatterns are the machine-local files derived from the op log that a
// redaction deletes and rebuilds rather than rewrites
// (docs/redaction-design.md §7): the index and its sidecars, doctor's
// scratch index, the fold checkpoint and the slow-write log. They are
// matched against a top-level name under the state root only.
//
// The plan does not count a hit in one of these as a residual — they are
// removed after the pass — but Scan, the final verification, reads them
// like any other file. This list is the same set index.ScrubDerived removes;
// see the package follow-up note on sharing one list once that exists.
var derivedPatterns = []string{
	"index.db",
	"index.db-wal",
	"index.db-shm",
	"index.db-journal",
	"index.db.malformed-*",
	".doctor-rebuild.db*",
	"fold-checkpoint.json",
	"fold-checkpoint.json.tmp",
	".slow-writes",
}

// isDerived reports whether rel (slash-separated, relative to the state
// root) is one of the derived files.
func isDerived(rel string) bool {
	if strings.Contains(rel, "/") {
		return false
	}
	for _, p := range derivedPatterns {
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// walkFiles calls fn for every regular file under stateRoot outside the
// top-level .git, with its slash-separated path relative to stateRoot.
// Symlinks and other non-regular entries are skipped: they are not files
// act writes, and following one could read outside the state root.
func walkFiles(stateRoot string, fn func(rel, abs string) error) error {
	info, err := os.Stat(stateRoot)
	if err != nil {
		return fmt.Errorf("redaction: state root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("redaction: state root %s: not a directory", stateRoot)
	}
	return filepath.WalkDir(stateRoot, func(abs string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("redaction: walk: %w", werr)
		}
		if abs == stateRoot {
			return nil
		}
		relOS, err := filepath.Rel(stateRoot, abs)
		if err != nil {
			return fmt.Errorf("redaction: walk: %w", err)
		}
		rel := filepath.ToSlash(relOS)
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		return fn(rel, abs)
	})
}

// Scan is the raw verification scan (docs/redaction-design.md §5.6 step 8):
// every regular file under stateRoot outside the top-level .git, read as raw
// bytes and searched for every raw form of every pattern and, when the file
// is JSON, for every pattern inside every decoded string and key — so the
// answer does not depend on how a file escapes the text. It returns the
// paths that hold one, slash-separated, relative to stateRoot, sorted. An
// empty result is the guarantee a clean run makes.
//
// Unlike the plan, Scan excludes nothing else: derived files and
// quarantined files are read like the rest.
func Scan(stateRoot string, patterns []string) (paths []string, err error) {
	if len(patterns) == 0 {
		return nil, errors.New("redaction: scan: no patterns")
	}
	forms := rawForms(patterns)
	err = walkFiles(stateRoot, func(rel, abs string) error {
		b, rerr := os.ReadFile(abs)
		if rerr != nil {
			return fmt.Errorf("redaction: scan: read %s: %w", rel, rerr)
		}
		if holds(b, patterns, forms) {
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}
