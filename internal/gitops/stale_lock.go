package gitops

import (
	"path/filepath"
	"regexp"
	"strings"
)

// staleLockPathRE captures the lock-file path from git's canonical
// lock-collision stderr, e.g.
//
//	fatal: Unable to create '/repo/.git/index.lock': File exists.
//	error: cannot lock ref 'HEAD': Unable to create '/repo/.git/HEAD.lock': File exists
//
// The captured group is the full path; callers basename it to the lock file
// name (index.lock / HEAD.lock).
var staleLockPathRE = regexp.MustCompile(`Unable to create '([^']*\.lock)': File exists`)

// StaleGitLockError wraps a git failure caused by a stale lock file in the
// repo's git dir — the wedge documented in the README "If a write is
// interrupted" section (act-8fe6eb). An interrupted auto-commit (session
// death, Ctrl-C, a reaped worktree agent, a full disk, or a sandbox that
// denies git's temp-file unlink) can leave index.lock or HEAD.lock behind;
// from then on every act write fails with git's "File exists" collision until
// the lock is removed. The append-only op file is written before the commit,
// so nothing is lost — recovery is: remove the lock, commit the stranded ops,
// `act doctor --fix`.
//
// Recognising this failure class lets the write path emit a first-class,
// structured signal (which file, the remedy) instead of passing git's raw
// stderr through buried inside a generic write_failed message.
type StaleGitLockError struct {
	// LockFile is the bare lock file name: "index.lock" or "HEAD.lock".
	LockFile string
	// Err is the underlying wrapped git error (carries git's stderr).
	Err error
}

func (e *StaleGitLockError) Error() string { return e.Err.Error() }
func (e *StaleGitLockError) Unwrap() error { return e.Err }

// classifyStaleLock inspects a git error message (as produced by GitOps.run,
// which appends `(stderr: ...)`) for the canonical stale-lock signature and
// returns the offending lock file's basename. ok is false when the message is
// not a lock collision.
func classifyStaleLock(msg string) (lockFile string, ok bool) {
	if m := staleLockPathRE.FindStringSubmatch(msg); m != nil {
		return filepath.Base(m[1]), true
	}
	// Fallback: some git versions phrase the ref-lock case without the
	// "Unable to create '<path>'" prefix but still name the lock and the
	// "File exists" collision. Match on the pair.
	if strings.Contains(msg, "File exists") {
		for _, name := range []string{"index.lock", "HEAD.lock"} {
			if strings.Contains(msg, name) {
				return name, true
			}
		}
	}
	return "", false
}

// wrapIfStaleLock returns a *StaleGitLockError wrapping err when err's message
// carries git's stale-lock signature; otherwise it returns err unchanged. The
// GitOps.run failure path routes every git error through here so the typed
// error is available to the write helpers (via errors.As) regardless of which
// git subcommand tripped the lock.
func wrapIfStaleLock(err error) error {
	if err == nil {
		return nil
	}
	if lock, ok := classifyStaleLock(err.Error()); ok {
		return &StaleGitLockError{LockFile: lock, Err: err}
	}
	return err
}
