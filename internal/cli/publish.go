// act-89a595 — fail-soft publish: a write whose op is DURABLY COMMITTED
// must not report failure just because the publish leg couldn't reach
// origin.
//
// The exit code is the only signal most agent callers read. Before this
// change, `act close` / `act update` on an unreachable origin committed
// the op locally and then exited non-zero, so a caller applying the
// universal `rc != 0 → the write didn't happen` rule retried and
// duplicated the op. That is a lie in the expensive direction: the op is
// on disk, in a commit, readable by the very next `act show`.
//
// THE BOUNDARY THIS FILE ENFORCES (read before changing anything here):
//
//	commit failed          → non-zero, loudly. Nothing landed.
//	commit ok, push failed → exit 0 + an unmissable stderr WARNING +
//	                         the commit queued in .act/.pending-pushes.
//
// Exit 0 is correct ONLY on the second line. Every caller of
// PublishCommittedOp is therefore positioned strictly after a successful
// CommitOp; do not move a call site earlier, and do not extend the
// fail-soft treatment to any error raised before the commit lands.
//
// Publication is not lost, only deferred: the queued entry means the
// next non-offline act write (or `act remote sync`) pushes every local
// commit reachable from HEAD, this one included.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/aac/act/internal/gitops"
)

// PublishResult reports what the post-commit publish leg did. The zero
// value means "pushed cleanly" — the overwhelmingly common case.
type PublishResult struct {
	// Deferred is true when the push did not reach origin and the
	// commit was left for a later push instead. The command still
	// succeeds (exit 0): the op is durably committed.
	Deferred bool

	// Reason is the underlying push/flush failure. Non-nil iff
	// Deferred. Surfaced to the caller for JSON envelopes; it is
	// already written to stderr by PublishCommittedOp.
	Reason error

	// Queued is true when the deferral was recorded in
	// `.act/.pending-pushes`. False means even the queue append
	// failed — the op is still durable and a later push still
	// publishes it (push is not sha-scoped), but nothing on disk
	// notes that a push is owed.
	Queued bool
}

// PublishCommittedOp runs the publish legs — flush any deferred backlog,
// then push this commit — for an op whose commit has ALREADY landed in
// the nested .act/ repo.
//
// It never returns an error. A publish failure is degraded, not
// propagated: the commit is queued to `.act/.pending-pushes`, a warning
// is written to stderr, and the result reports Deferred so callers can
// mirror the fact into a JSON envelope. Callers MUST NOT translate a
// deferred publish into a non-zero exit (act-89a595).
//
// stderr may be nil, in which case os.Stderr is used.
func PublishCommittedOp(gops *gitops.ActGitOps, opType, branch string, stderr io.Writer) PublishResult {
	// Flush first: prior --offline commits publish alongside this one.
	// A flush failure is the same class of event as a push failure —
	// origin is unreachable or rejecting — so it degrades identically
	// rather than aborting before this commit's own push attempt.
	if err := FlushPendingPushes(gops, gops.RepoRoot); err != nil {
		return deferPublish(gops, opType, stderr, fmt.Errorf("flush deferred pushes: %w", err))
	}
	if err := gops.AutoPushAfterCommitToBranch(branch); err != nil {
		return deferPublish(gops, opType, stderr, err)
	}
	return PublishResult{}
}

// deferPublish records the unpushed commit, warns, and reports the
// deferral. Always returns Deferred=true.
func deferPublish(gops *gitops.ActGitOps, opType string, stderr io.Writer, reason error) PublishResult {
	res := PublishResult{Deferred: true, Reason: reason}
	if err := RecordPendingPush(gops, gops.RepoRoot, opType); err == nil {
		res.Queued = true
	}
	emitPublishDeferredWarning(stderr, res)
	return res
}

// emitPublishDeferredWarning writes the operator-facing warning. It is
// deliberately loud and deliberately explicit about the exit code: the
// whole point of act-89a595 is that a zero exit here means "committed,
// not published", and an agent that skims the tail of stderr has to be
// able to tell that apart from an ordinary success.
func emitPublishDeferredWarning(stderr io.Writer, res PublishResult) {
	dst := stderr
	if dst == nil {
		dst = os.Stderr
	}
	fmt.Fprintf(dst, "act: WARNING: the op was COMMITTED LOCALLY but NOT PUSHED to origin.\n")
	fmt.Fprintf(dst, "act: WARNING:   push failed: %v\n", res.Reason)
	if res.Queued {
		fmt.Fprintf(dst, "act: WARNING:   queued in .act/.pending-pushes; the next act write (or 'act remote sync') retries the push.\n")
	} else {
		fmt.Fprintf(dst, "act: WARNING:   the queue append ALSO failed; run 'act remote sync' once origin is reachable.\n")
	}
	fmt.Fprintf(dst, "act: WARNING:   the write itself landed and is durable — exit status is 0 on purpose. Do NOT re-run this command; re-running duplicates the op.\n")
}
