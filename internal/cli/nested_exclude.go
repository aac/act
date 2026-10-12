package cli

// act-1d3110 — keep act's own working files out of the nested repo's
// untracked list.
//
// `.act/` is a git repo of its own, and act writes machine-local working
// files at its top level beside the tracked op log. Nothing told the
// nested repo to ignore them, so `git -C .act status` listed them as
// untracked and a blanket stage there (`git add -A`) would have committed
// a lock file or a SQLite cache into the tracker's history.
//
// The entries go in `.act/.git/info/exclude`, not a tracked
// `.act/.gitignore`: the exclude file is per-clone and never travels, so
// repairing an existing store needs no commit and cannot conflict with a
// peer's copy of the tracker.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/gitops"
)

// CheckNestedExclude is the doctor check name for the repair below.
const CheckNestedExclude = "nested-exclude"

// nestedExcludeEntries are the patterns act keeps in the nested repo's
// info/exclude, one per act-owned working file at the top of `.act/`.
// Each is anchored with a leading slash so it matches only the top-level
// file, never an op file that happens to share the name.
//
// Every name here is machine-local state that act never stages: the two
// the ticket named (.write.lock, .slow-writes), their siblings written
// the same way (.pending-pushes, .sync-log, .compact.lock), and the
// derived SQLite index with its sidecars. A store that already tracks one
// of these (older layouts tracked index.db) is unaffected — exclude
// patterns never apply to a tracked path.
var nestedExcludeEntries = []string{
	"/" + gitops.WriteLockFile,
	"/.slow-writes",
	"/.pending-pushes",
	"/" + SyncLogFilename,
	"/.compact.lock",
	"/index.db",
	"/index.db-wal",
	"/index.db-shm",
	"/index.db-journal",
}

// nestedExcludeRel is the exclude file's path as shown to the user.
const nestedExcludeRel = ".act/.git/info/exclude"

func nestedExcludePath(actDir string) string {
	return filepath.Join(actDir, ".git", "info", "exclude")
}

// missingNestedExcludeEntries returns the entries from nestedExcludeEntries
// that the nested repo's exclude file does not already carry as a line of
// its own, plus the file's current content. A missing file reads as empty.
func missingNestedExcludeEntries(actDir string) (missing []string, existing []byte, err error) {
	existing, err = os.ReadFile(nestedExcludePath(actDir))
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	for _, e := range nestedExcludeEntries {
		if !have[e] {
			missing = append(missing, e)
		}
	}
	return missing, existing, nil
}

// EnsureNestedExclude appends whichever of act's working-file patterns the
// nested repo's info/exclude lacks, and returns the ones it added.
// Existing content is kept byte-for-byte; a second call adds nothing and
// does not rewrite the file. A `.act/` with no `.git` directory is left
// alone (nil, nil) — there is no nested repo to configure.
func EnsureNestedExclude(actDir string) (added []string, err error) {
	if fi, serr := os.Stat(filepath.Join(actDir, ".git")); serr != nil || !fi.IsDir() {
		return nil, nil
	}
	missing, existing, err := missingNestedExcludeEntries(actDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", nestedExcludeRel, err)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("# act: machine-local working files (never committed)\n")
	for _, e := range missing {
		b.WriteString(e)
		b.WriteByte('\n')
	}
	path := nestedExcludePath(actDir)
	// git creates info/ on init, but a template dir can omit it.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(nestedExcludeRel), err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", nestedExcludeRel, err)
	}
	return missing, nil
}

// checkNestedExclude reports a nested repo whose info/exclude is missing
// any of act's working-file patterns. With fix it adds them. Warn in both
// cases: the state is untidy, not broken — act stages only op paths, so
// the files reach history only through someone's blanket stage.
func checkNestedExclude(actDir string, fix bool) []Finding {
	if fi, err := os.Stat(filepath.Join(actDir, ".git")); err != nil || !fi.IsDir() {
		return nil
	}
	missing, _, err := missingNestedExcludeEntries(actDir)
	if err != nil {
		return []Finding{{
			Check: CheckNestedExclude, Severity: "error",
			Message: fmt.Sprintf("read %s: %v", nestedExcludeRel, err),
		}}
	}
	if len(missing) == 0 {
		return nil
	}
	list := strings.Join(missing, " ")
	if fix {
		if _, err := EnsureNestedExclude(actDir); err != nil {
			return []Finding{{
				Check: CheckNestedExclude, Severity: "error",
				Message: fmt.Sprintf("could not repair %s: %v", nestedExcludeRel, err),
			}}
		}
		return []Finding{{
			Check: CheckNestedExclude, Severity: "warn",
			Message: fmt.Sprintf("added act's working files to %s: %s", nestedExcludeRel, list),
		}}
	}
	return []Finding{{
		Check: CheckNestedExclude, Severity: "warn",
		Message: fmt.Sprintf(
			"the nested .act repo does not ignore act's working files (%s): they show as untracked in `git -C .act status` and a blanket stage there would commit them. "+
				"Remedy: act doctor --fix (adds them to %s)", list, nestedExcludeRel),
	}}
}
