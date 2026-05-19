// Phase 2 ticket 3b (act-4a604d) — `--offline` flag + pending-push retry.
//
// `--offline` is a write-flag knob accepted by every write subcommand
// (`create`, `update`, `close`, `dep add`, `reopen`, `delete`). When
// set, the op write follows the normal commit path locally but skips
// the synchronous `git push` ticket-3a added; instead, the command
// appends a `(timestamp, sha, op_type)` record to `.act/.pending-pushes`
// noting the local commit's SHA. The next non-offline write flushes
// the pending-pushes queue before its own push, so the remote catches
// up automatically.
//
// Distinction from --isolated: --isolated is the spec-§4 "local-only"
// mode that forbids any network operation (no fetch, no push, no
// pending-push tracking). --offline is the lighter "skip push but
// remember to push later" mode. Both result in no network call, but
// only --offline arranges automatic catch-up.
//
// Flag-validation: --offline is incompatible with --push (the latter
// asks for an immediate push, the former asks for the opposite).
// --offline is allowed alongside --no-commit at the flag-parse level
// but the combo is a no-op (no commit, so nothing to defer); the
// command silently treats --no-commit as the effective behavior.
//
// File schema (pinned by ticket-3b "PINNED .act/.pending-pushes
// SCHEMA"): JSON-lines, one record per line, fields
// `timestamp, sha, op_type`. See slowwrites.go's PendingPushRecord
// for the Go shape.
//
// Asserting tests:
//   - TestOffline_CreateLocallyCommitsAndRecordsPending: AC1
//   - TestOffline_NonOfflineFlushesPendingBeforeOwnPush: AC2
//   - TestDocClaim_Offline_FlagHelp: pinned --offline help text in cmd/act
//   - TestDocClaim_PendingPush_Schema: pinned schema in docs/spec-v2.md
package cli

import (
	"fmt"
	"time"

	"github.com/aac/act/internal/gitops"
)

// FlushPendingPushes publishes every pending-push entry in
// `<stateRoot>/.pending-pushes` (in file order) to origin via
// gitops.PushWithRetry, then truncates the file. Called by the
// write-helpers BEFORE their own AutoPushAfterCommit so any
// previously-deferred offline commits land on the remote together
// with the new one.
//
// Semantics: the entries are deferred references to local commits
// that were never pushed. A bare `git push origin <branch>` on the
// nested .act/ repo publishes EVERY local commit reachable from
// HEAD that is not yet on the remote, so we don't need to push each
// SHA individually — the pending-pushes file is a marker for "there
// are local commits we should publish", not a sha-by-sha replay log.
// A single push covers them all.
//
// Concrete sequence:
//  1. Read .act/.pending-pushes. Empty → return nil (no work).
//  2. Invoke AutoPushAfterCommit (which already handles no-origin /
//     retry / exhaustion). On success → step 3. On failure → return
//     the error; the file is NOT cleared (so the next attempt retries
//     the same shas).
//  3. ClearPendingPushes.
//
// gops must be the writer-side handle (nested-.act-rooted ActGitOps);
// stateRoot is gops.RepoRoot.
func FlushPendingPushes(gops *gitops.ActGitOps, stateRoot string) error {
	pending, err := ReadPendingPushes(stateRoot)
	if err != nil {
		return fmt.Errorf("cli: read pending-pushes: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}
	// One push covers all pending shas (and the current HEAD), per the
	// rationale above. If the push fails, the file stays in place so
	// the next attempt retries from the same pending state.
	if err := gops.AutoPushAfterCommit(); err != nil {
		return err
	}
	if err := ClearPendingPushes(stateRoot); err != nil {
		return fmt.Errorf("cli: clear pending-pushes after flush: %w", err)
	}
	return nil
}

// RecordPendingPush appends one entry to `.act/.pending-pushes` noting
// the local HEAD sha (the commit that just landed) and the op_type
// being deferred. Called by write-helpers on the --offline path
// AFTER a successful CommitOp.
func RecordPendingPush(gops *gitops.ActGitOps, stateRoot, opType string) error {
	sha, err := gops.HeadSHA()
	if err != nil {
		return fmt.Errorf("cli: read HEAD for pending-push: %w", err)
	}
	rec := PendingPushRecord{
		Timestamp: FormatSlowWriteTimestamp(time.Now()),
		SHA:       sha,
		OpType:    opType,
	}
	return AppendPendingPush(stateRoot, rec)
}
