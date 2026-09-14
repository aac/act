package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/op"
)

// failedOpQuarantineDir is the sibling-of-ops directory under the act
// state root where op files whose commit never landed are parked. It
// mirrors the collision quarantine convention in internal/gitops
// (`.collisions/<stamp>/ops/...`) so an operator inspecting `.act/`
// finds one recognisable shape for "act moved your file aside, here it
// is". Like `.collisions/`, it is deliberately OUTSIDE `ops/` — the
// fold reads `ops/` and nothing else, so a quarantined envelope cannot
// be folded back into the issue state by accident.
const failedOpQuarantineDir = ".failed-ops"

// quarantineFailedOp moves an op file out of the op log after the write
// that produced it failed to commit, and returns the path it was moved
// to.
//
// WHY THIS EXISTS (act-94272e). act's write commands report failure by
// exit code and a structured error, and the code's own contract — see
// the act-89a595 comment in close.go — is that every failure path at or
// before the commit means "nothing landed". But the fold reads op files
// from `ops/` on disk regardless of whether git ever committed them, so
// an op file left behind by a failed commit was folded on the next read:
// `act close` said commit_failed / exit 1 and `act show` then said the
// issue was closed. The two surfaces disagreed about the same event.
//
// THE SEMANTICS ACT CHOSE: an op whose commit failed is invisible. The
// alternative — teach the fold to skip uncommitted ops — was rejected
// because uncommitted op files are legitimate in normal operation (the
// --offline / pending-push path commits locally and defers only the
// push, and doctor/harvest work on ops that are staged but not yet
// committed). A blanket "uncommitted ⇒ invisible" fold rule would hide
// ops that really did land. Only the command whose commit just failed
// knows that THIS op did not land, so that command is where the
// correction belongs.
//
// SCOPE: every failure at the stage or commit step (act-a3160b). A
// stage failure used to be the exception — its dominant cause is a
// stale `.act/.git/index.lock`, and README "If a write is interrupted"
// once recovered by committing the op file still sitting in ops/. That
// left `act create` exiting non-zero while `act list` showed the issue.
// The runbook now copies the envelope back from `.act/.failed-ops/`
// instead, so one rule covers both steps. The move is a working-tree
// rename, which a held index.lock does not block.
//
// THE ORIGINAL INTENT IS PRESERVED. The commit-failure paths used to
// leave the file in place explicitly "so the user can retry without
// rebuilding the envelope". The envelope is still not destroyed: it is
// moved intact, and its new path is reported to the caller so a retry
// can restore it verbatim. That is strictly more useful than the old
// behavior, where the retry affordance was an undocumented file on disk
// that silently corrupted every read until someone noticed.
//
// Best-effort by construction: callers are already on an error path, so
// a quarantine failure must not mask the original error. On failure the
// op file is removed instead — an unreadable-but-correct store beats a
// readable-but-lying one — and the error is returned for the caller to
// annotate or ignore.
func quarantineFailedOp(stateRoot, opPath string) (string, error) {
	if stateRoot == "" || opPath == "" {
		return "", fmt.Errorf("cli: quarantine failed op: empty path")
	}
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05.000Z")

	rel, err := filepath.Rel(stateRoot, opPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		// opPath is not under the state root (shouldn't happen; the op
		// log always is). Fall back to the base name so the file still
		// lands somewhere recoverable rather than escaping the root.
		rel = filepath.Base(opPath)
	}
	dst := filepath.Join(stateRoot, failedOpQuarantineDir, stamp, rel)

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		_ = os.Remove(opPath)
		return "", fmt.Errorf("cli: quarantine failed op: create dir: %w", err)
	}
	if err := os.Rename(opPath, dst); err != nil {
		if os.IsNotExist(err) {
			// Already gone (a concurrent healer, or a caller that
			// removed it). Nothing to do and nothing to report.
			return "", nil
		}
		_ = os.Remove(opPath)
		return "", fmt.Errorf("cli: quarantine failed op: move: %w", err)
	}
	return dst, nil
}

// withdrawOpFile takes an op file out of the op log after a write that
// did not land, and guarantees that the working tree and HEAD agree about
// it afterwards. It returns the quarantine path (empty when nothing was
// moved) for the caller to report.
//
// WHY THIS IS NOT JUST quarantineFailedOp (act-8ee085). Moving the file
// out of ops/ is correct only while the file is act's private business.
// It stops being private the moment anything commits it, and something
// does: the act-sync sweep runs `git add -- ops` on its own cadence and
// commits whatever it finds uncommitted. If a sweep committed this op
// while act was still deciding whether to keep it, a bare quarantine
// leaves HEAD advertising an op act refused — the working tree says the
// issue is open, every consumer of committed state says it is closed, and
// the disagreement is silent (act-8ee085). The mirror image was observed
// too (act-cb55ee): a later sweep picks up the dirty deletion and commits
// it as an anonymous "sweep N uncommitted op file(s)", so an op the sweep
// itself had published a moment earlier gets un-published by a commit
// whose message explains nothing.
//
// So when HEAD tracks the path, act commits the removal itself, under a
// message that says what happened. Both halves are best-effort by
// construction: the caller is already on an error path and the original
// error must not be masked. A failure to commit the removal leaves the
// deletion uncommitted — no worse than the old behavior, and visible in
// `git status`.
func withdrawOpFile(gops *gitops.ActGitOps, stateRoot, opPath string, env op.Envelope) string {
	tracked := false
	if gops != nil {
		if t, err := gops.HeadTracksOpFile(opPath); err == nil {
			tracked = t
		}
	}
	q, _ := quarantineFailedOp(stateRoot, opPath)
	if tracked && gops != nil {
		// Deliberately NOT the `act-op: (act-XXXX) <type>` subject every
		// real op commit carries: this commit retracts an op rather than
		// recording one. Doctor's orphan-close check treats a `(act-XXXX`
		// marker as evidence that work exists for an issue, and the
		// compaction pass groups contiguous `act-op:` commits — a
		// retraction belongs in neither.
		msg := fmt.Sprintf("act: withdraw %s op for %s (commit failed)",
			env.OpType, ShortIssueID(env.IssueID))
		_ = gops.CommitOpFileRemoval(opPath, msg)
	}
	return q
}

// quarantineSuffix renders the quarantine path as a trailing clause for
// the plain-error paths (WriteOpAndAutoCommit and friends return a bare
// error, not a structured envelope, so there is no details map to put it
// in). Empty string when nothing was quarantined, so the caller's
// message is unchanged in that case.
func quarantineSuffix(quarantined string) string {
	if quarantined == "" {
		return ""
	}
	return fmt.Sprintf(" (op file preserved at %s; nothing was recorded)", quarantined)
}

// quarantinedOpError carries the quarantine path of a withdrawn op
// alongside the stage/commit error that caused the withdrawal. The
// single-op write helper returns a plain error, and the commands that
// call it classify that error afterwards (StaleLockDetails, write_failed)
// — so the path has to travel on the error itself for the structured
// envelope to report it. Error() keeps the historical plain-text shape
// ("... (op file preserved at <path>; nothing was recorded)").
type quarantinedOpError struct {
	err  error
	path string
}

// newQuarantinedOpError wraps err with the quarantine path. An empty path
// (nothing was moved) returns err unchanged.
func newQuarantinedOpError(err error, path string) error {
	if path == "" {
		return err
	}
	return &quarantinedOpError{err: err, path: path}
}

func (e *quarantinedOpError) Error() string { return e.err.Error() + quarantineSuffix(e.path) }
func (e *quarantinedOpError) Unwrap() error { return e.err }

// quarantinedOpPath returns the quarantine path carried by err, or "".
func quarantinedOpPath(err error) string {
	var qe *quarantinedOpError
	if errors.As(err, &qe) {
		return qe.path
	}
	return ""
}

// failedOpStampDir renders the `.act/.failed-ops/<stamp>` directory that
// holds a quarantined op, relative to the host repo root — the form the
// README recovery runbook and the stale_git_lock remedy copy from. Returns
// "" when path is not under a .failed-ops directory.
func failedOpStampDir(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == failedOpQuarantineDir && i+1 < len(parts) {
			return ".act/" + failedOpQuarantineDir + "/" + parts[i+1]
		}
	}
	return ""
}

// withQuarantineDetail folds the quarantine path into an error-envelope
// details map so the failing command tells the caller where the op file
// went. Returns the map unchanged (possibly nil) when there is nothing
// to report, so callers can pass it straight through.
func withQuarantineDetail(details map[string]any, quarantined string) map[string]any {
	if quarantined == "" {
		return details
	}
	if details == nil {
		details = map[string]any{}
	}
	details["quarantined_op"] = quarantined
	return details
}
