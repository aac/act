// Package claim implements the atomic claim protocol per spec-v2.md
// §Op-fold and concurrency (atomic claim protocol), §5.B.3, §5.C.3.
//
// The protocol's job is to acquire (assignee, status=in_progress) for an
// issue in the face of concurrent writers. Resolution rule: the EARLIEST
// claim op (smallest (HLC.Wall, HLC.Logical, op_hash)) wins. Losing claim
// ops remain in history but are suppressed at fold time per §5.B.3.
//
// This package is intentionally git-agnostic: the GitOps interface
// abstracts the actual `git commit / pull --rebase / push` so the protocol
// is unit-testable without shelling out.
package claim

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// Result is the outcome of a single RunClaim invocation (or one attempt
// inside a --wait loop; the final returned Result reflects the last attempt).
type Result struct {
	// Claimed is true iff our op_hash is the winner.
	Claimed bool
	// Winner is the assignee of the winning claim op (set on both win and
	// loss paths; empty only when the issue had no claim ops at all, which
	// is impossible after we wrote our own).
	Winner string
	// YourOpHash is the 8-hex op_hash of the claim op WE wrote on the most
	// recent attempt.
	YourOpHash string
	// IssueID is the issue we attempted to claim.
	IssueID string
	// HLC is the HLC stamped on our most recent claim op.
	HLC hlc.HLC
}

// Options configures a RunClaim invocation.
type Options struct {
	// Assignee becomes the ClaimPayload.assignee. Required.
	Assignee string
	// Wait, when true, retries on loss with exponential backoff.
	Wait bool
	// WaitTimeout caps the cumulative sleep budget across retries.
	WaitTimeout time.Duration
	// Isolated, when true, skips the post-write `git pull --rebase`. This
	// is the offline / single-machine path. Spec §3.4 step 3.
	Isolated bool
	// Push, when true, runs `git push` IFF the result is a win. Spec §3.4
	// step 5.
	Push bool
}

// GitOps abstracts the side-effecting git operations performed by RunClaim.
// Real callers wire this to a thin shellout; tests inject a recording fake.
type GitOps interface {
	// Commit stages the .act/ops subtree and creates a single commit with
	// the given message. Implementations typically use --no-verify per the
	// protocol (act-9824 acceptance criteria).
	Commit(message string) error
	// PullRebase runs `git pull --rebase <remote> <branch>`. A rebase
	// conflict on .act/ops/** is the caller's responsibility to surface as
	// E_REBASE per spec §3.4 implementation notes.
	PullRebase() error
	// Push runs `git push`. Only invoked on win when Options.Push is set.
	Push() error
}

// ErrInvalidFlags is returned when conflicting universal flags are passed.
// Callers translate this to exit code 2 per spec §4.
var ErrInvalidFlags = errors.New("claim: invalid flag combination")

// ErrEmptyAssignee is returned when Options.Assignee is empty.
var ErrEmptyAssignee = errors.New("claim: assignee is required")

// ErrNoUpstream is the sentinel a GitOps.PullRebase implementation should
// return when the working tree has no upstream remote configured. RunClaim
// treats this as a no-op success path for the local-first / fresh-repo
// case (act-fdb2): the protocol still works without a remote because
// nothing can have raced us. The gitops package re-exports this value as
// ErrNoRemote so callers that want to detect "no remote at all" can keep
// using either sentinel; errors.Is matches both.
var ErrNoUpstream = errors.New("claim: no upstream remote configured")

// ErrPullRebaseSoftFail is the sentinel a GitOps.PullRebase implementation
// returns when the rebase pre-step failed for a transient/cosmetic reason
// that does NOT compromise the local write. By the time RunClaim invokes
// PullRebase the new claim op has already been written and committed to the
// local working tree (steps 2-3); a soft pull-rebase failure leaves the
// local op durable. The op log is convergent — the next read or write will
// re-fetch and reconcile against any concurrent writer.
//
// The canonical case (act-68f08b) is `git pull --rebase` refusing because
// the working tree has unstaged changes — under Phase 1's nested-repo
// layout, `.act/index.db` is tracked but rewritten on every read, so any
// prior `act show` dirties the index even though no op was written. The
// gitops package re-exports this value as ErrPullRebaseDirtyTree.
//
// RunClaim treats this sentinel like ErrNoUpstream: log nothing, continue
// to the fold/winner-determination step. Other PullRebase failures (rebase
// conflict on .act/ops/**, network failure, auth) remain hard errors.
var ErrPullRebaseSoftFail = errors.New("claim: pull --rebase soft failure (local op durable)")

// sleeper is the indirection used to make --wait retry deterministic in
// tests. The default implementation calls time.Sleep; tests inject a fake.
type sleeper func(time.Duration)

// jitterSource is the indirection used to make --wait jitter deterministic
// in tests. The default returns a uniform random value in [0.75, 1.25].
type jitterSource func() float64

// noJitter returns the multiplier 1.0 — the production CLI does not jitter
// (per spec §5.D.1, jitter belongs to the MCP composed tool). Tests can
// substitute their own.
func noJitter() float64 { return 1.0 }

// RunClaim executes the atomic claim protocol for issueID. See package doc.
//
// Step ordering (spec §3.4 + §5.C.3):
//  1. HLC plausibility check against the repo reference. If implausible,
//     return error before any network mutation.
//  2. Build claim envelope, write op file via op.ProbeAndWrite, commit.
//  3. If !Isolated: gitOps.PullRebase().
//  4. Re-fold the issue.
//  5. Pick winner by smallest (wall, logical, op_hash) tuple.
//  6. If we won and Options.Push: gitOps.Push(). Return Result.
//  7. If we lost and Options.Wait: backoff (1s, 2s, 4s, 8s) capped by
//     WaitTimeout, retry from step 2 with a fresh HLC.
func RunClaim(repoRoot, issueID string, opts Options, clock *hlc.Clock, gitOps GitOps) (Result, error) {
	return runClaimInternal(repoRoot, issueID, opts, clock, gitOps, time.Sleep, noJitter)
}

// runClaimInternal is the testable core: the sleeper and jitter source are
// injected.
func runClaimInternal(
	repoRoot, issueID string,
	opts Options,
	clock *hlc.Clock,
	gitOps GitOps,
	sleep sleeper,
	jitter jitterSource,
) (Result, error) {
	if opts.Assignee == "" {
		return Result{IssueID: issueID}, ErrEmptyAssignee
	}
	if opts.Isolated && opts.Push {
		return Result{IssueID: issueID}, fmt.Errorf("%w: --isolated and --push are mutually exclusive", ErrInvalidFlags)
	}
	if clock == nil {
		return Result{IssueID: issueID}, fmt.Errorf("claim: clock is nil")
	}
	if gitOps == nil {
		return Result{IssueID: issueID}, fmt.Errorf("claim: gitOps is nil")
	}

	// Step 1 (§5.C.3): HLC drift check FIRST, before any network mutation.
	// We probe a candidate HLC produced from the current clock against the
	// repo reference (last_hlc from .act/config.json, if present). Per
	// spec, an implausible drift fails fast with no commit / no pull.
	repoRef, err := repoReference(repoRoot)
	if err != nil {
		return Result{IssueID: issueID}, fmt.Errorf("claim: read repo reference: %w", err)
	}
	probe := clock.Send()
	if err := clock.Plausible(probe, repoRef); err != nil {
		return Result{IssueID: issueID, HLC: probe}, err
	}

	// Schedule for --wait retries: 1s, 2s, 4s, 8s (then 8s cap; further
	// attempts cut off by WaitTimeout). The first attempt has no preceding
	// sleep.
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	var elapsed time.Duration
	var last Result

	// Reuse the probe HLC for the first attempt to avoid burning a logical
	// counter and to keep the plausibility check meaningful for that op.
	firstHLC := probe
	useFirstHLC := true

	for attempt := 0; ; attempt++ {
		var stamp hlc.HLC
		if useFirstHLC {
			stamp = firstHLC
			useFirstHLC = false
		} else {
			stamp = clock.Send()
		}

		res, err := singleAttempt(repoRoot, issueID, opts, stamp, gitOps)
		if err != nil {
			return res, err
		}
		last = res
		if res.Claimed {
			return res, nil
		}
		if !opts.Wait {
			return res, nil
		}
		// Budget exhausted? Stop without another sleep+attempt.
		if opts.WaitTimeout > 0 && elapsed >= opts.WaitTimeout {
			return last, nil
		}
		// Choose the next backoff. attempt is 0-based; the post-loss sleep
		// before attempt N+1 uses backoff[min(attempt, len-1)].
		idx := attempt
		if idx >= len(backoff) {
			idx = len(backoff) - 1
		}
		base := backoff[idx]
		mul := jitter()
		delay := time.Duration(float64(base) * mul)
		// Cap delay so we never exceed the remaining timeout budget. After
		// the cap we always perform one more retry, even if the cap is
		// less than the natural backoff.
		if opts.WaitTimeout > 0 {
			remaining := opts.WaitTimeout - elapsed
			if remaining <= 0 {
				return last, nil
			}
			if delay > remaining {
				delay = remaining
			}
		}
		sleep(delay)
		elapsed += delay
	}
}

// singleAttempt runs one full attempt of the claim protocol (steps 2-6 of
// §3.4) using the given HLC. It returns a Result (Claimed=true on win) or
// an error on a hard failure (write/commit/pull/fold).
func singleAttempt(
	repoRoot, issueID string,
	opts Options,
	stamp hlc.HLC,
	gitOps GitOps,
) (Result, error) {
	paths := config.Layout(repoRoot)

	// Step 0 (act-fdb2): idempotent re-claim short-circuit. If the active
	// claim window already has a winner whose assignee equals ours, the
	// issue is already ours and re-running is a no-op success — we MUST
	// NOT write a duplicate claim op. Without this guard, a re-claim by
	// the same node writes a later-HLC claim, then loses the (earliest-
	// wins) ordering against its own earlier op and reports "lost race
	// (winner=<self-node-id>)". See spec §5.B.3 (claim-suppression rule).
	if winnerHash, winnerAssignee, err := winnerOnDisk(paths.Ops, issueID); err != nil {
		return Result{IssueID: issueID, HLC: stamp}, fmt.Errorf("claim: idempotence-check winner: %w", err)
	} else if winnerHash != "" && winnerAssignee == opts.Assignee {
		return Result{
			IssueID:    issueID,
			YourOpHash: winnerHash,
			Winner:     winnerAssignee,
			HLC:        stamp,
			Claimed:    true,
		}, nil
	}

	// Step 2: build envelope.
	payload := op.ClaimPayload{Assignee: opts.Assignee}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return Result{IssueID: issueID, HLC: stamp}, fmt.Errorf("claim: marshal payload: %w", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "claim",
		IssueID:       issueID,
		Payload:       payloadBytes,
		HLC:           stamp,
		NodeID:        stamp.NodeID,
	}
	if err := env.Validate(); err != nil {
		return Result{IssueID: issueID, HLC: stamp}, fmt.Errorf("claim: validate envelope: %w", err)
	}
	body, err := env.Marshal()
	if err != nil {
		return Result{IssueID: issueID, HLC: stamp}, fmt.Errorf("claim: marshal envelope: %w", err)
	}
	ourHash, err := env.Hash()
	if err != nil {
		return Result{IssueID: issueID, HLC: stamp}, fmt.Errorf("claim: hash: %w", err)
	}

	// Step 3a: write op file under .act/ops/<issue>/<yyyy-mm>/.
	fsLock := func() (func(), error) { return func() {}, nil }
	if _, _, err := op.ProbeAndWrite(paths.Ops, env, body, fsLock); err != nil {
		return Result{IssueID: issueID, YourOpHash: ourHash, HLC: stamp}, fmt.Errorf("claim: write op: %w", err)
	}

	// Step 3b: commit the new op. The canonical auto-commit subject is
	// `act-op: (act-XXXX) claim`; the parenthesized short id is required
	// for doctor's orphan-close grep, and the op_type-only suffix matches
	// every other write op (act-d3a5). The previous form `act-<id>: claim
	// <assignee>` produced a double-prefix bug (`act-act-XXXX: ...`)
	// because issueID already begins with "act-".
	msg := buildClaimCommitMessage(issueID)
	if err := gitOps.Commit(msg); err != nil {
		return Result{IssueID: issueID, YourOpHash: ourHash, HLC: stamp}, fmt.Errorf("claim: commit: %w", err)
	}

	// Step 4: pull --rebase unless --isolated. Two sentinel cases are
	// swallowed:
	//
	//   - ErrNoUpstream: the local-first / fresh-repo case (act-fdb2) —
	//     no remote means no concurrent writer to rebase against; the
	//     protocol is still safe.
	//   - ErrPullRebaseSoftFail: the rebase pre-step refused for a
	//     transient/cosmetic reason (the canonical case is a dirty
	//     working tree from a prior read mutating `.act/index.db` —
	//     act-68f08b). The local commit landed in step 3, so the op is
	//     durable; the next read/write will re-fetch and reconcile.
	//     Surfacing raw git stderr here would mislead the agent into
	//     thinking the write failed.
	//
	// Other PullRebase failures (rebase conflict on .act/ops/**, network,
	// auth) remain hard errors.
	if !opts.Isolated {
		if err := gitOps.PullRebase(); err != nil {
			if !errors.Is(err, ErrNoUpstream) && !errors.Is(err, ErrPullRebaseSoftFail) {
				return Result{IssueID: issueID, YourOpHash: ourHash, HLC: stamp}, fmt.Errorf("claim: pull --rebase: %w", err)
			}
		}
	}

	// Step 5: re-fold the issue. We discard the state because winner
	// selection re-reads the on-disk envelopes (the fold layer's claim
	// apply already picks the earliest-tuple winner, but we need the
	// op_hash for our own identity check, which the IssueState does not
	// expose). The fold call still serves as a structural validation that
	// the post-rebase tree is a coherent op log.
	if _, err := fold.FoldIssue(paths.Ops, issueID, fold.ApplyDispatch); err != nil {
		return Result{IssueID: issueID, YourOpHash: ourHash, HLC: stamp}, fmt.Errorf("claim: fold: %w", err)
	}

	// Step 6: determine winner over all claim ops on disk for this issue.
	winnerHash, winnerAssignee, err := winnerOnDisk(paths.Ops, issueID)
	if err != nil {
		return Result{IssueID: issueID, YourOpHash: ourHash, HLC: stamp}, fmt.Errorf("claim: winner: %w", err)
	}
	res := Result{
		IssueID:    issueID,
		YourOpHash: ourHash,
		Winner:     winnerAssignee,
		HLC:        stamp,
		Claimed:    winnerHash == ourHash,
	}

	// Step 7: push only on win.
	if res.Claimed && opts.Push {
		if err := gitOps.Push(); err != nil {
			return res, fmt.Errorf("claim: push: %w", err)
		}
	}
	return res, nil
}

// winnerOnDisk walks rootOps/<issueID>/**/*.json, parses every op envelope,
// and returns the (op_hash, assignee) of the winning claim op — the one
// with the smallest (HLC.Wall, HLC.Logical, op_hash) tuple — within the
// active claim window. A close op resets the active window; a subsequent
// (non-existent in this codebase yet) reopen reopens it. While no reopen
// op type is implemented here, we still respect close: any claim ops
// strictly after the latest close are ignored as "not in the window".
//
// In practice we follow the simpler-but-equivalent rule: we collect all
// claim ops with HLC strictly greater than the latest close's HLC (or all
// claim ops if no close is present), then pick the smallest.
func winnerOnDisk(rootOps, issueID string) (string, string, error) {
	type claimRec struct {
		hlc      hlc.HLC
		fullHash string
		hash8    string
		assignee string
	}

	var claims []claimRec
	var latestClose hlc.HLC
	haveClose := false

	envelopes, err := loadIssueEnvelopes(rootOps, issueID)
	if err != nil {
		return "", "", err
	}
	for _, env := range envelopes {
		switch env.OpType {
		case "claim":
			var p op.ClaimPayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				return "", "", fmt.Errorf("claim: unmarshal claim payload: %w", err)
			}
			full, err := env.FullHash()
			if err != nil {
				return "", "", fmt.Errorf("claim: full hash: %w", err)
			}
			short, err := env.Hash()
			if err != nil {
				return "", "", fmt.Errorf("claim: short hash: %w", err)
			}
			claims = append(claims, claimRec{
				hlc:      env.HLC,
				fullHash: full,
				hash8:    short,
				assignee: p.Assignee,
			})
		case "close":
			if !haveClose || latestClose.Less(env.HLC) {
				latestClose = env.HLC
				haveClose = true
			}
		}
	}

	// Filter claims to those within the active window.
	var active []claimRec
	for _, c := range claims {
		if haveClose && !latestClose.Less(c.hlc) {
			continue
		}
		active = append(active, c)
	}
	if len(active) == 0 {
		return "", "", nil
	}

	// Earliest tuple wins (§5.B.3): smallest (wall, logical, full_hash).
	winner := active[0]
	for _, c := range active[1:] {
		if claimLess(c, winner) {
			winner = c
		}
	}
	return winner.hash8, winner.assignee, nil
}

// claimLess implements the (wall, logical, full_hash) ordering used for
// claim winner selection.
//
// Delegates to hlc.Stamp.Less so the claim winner-selection path and the LWW
// gate path in internal/fold use a single comparison primitive — the fix for
// act-492e, which had these two paths tiebreaking differently (by op_hash
// here, by node_id in the old hlc.HLC.Less LWW gate).
func claimLess(a, b struct {
	hlc      hlc.HLC
	fullHash string
	hash8    string
	assignee string
}) bool {
	return hlc.Stamp{HLC: a.hlc, Hash: a.fullHash}.Less(hlc.Stamp{HLC: b.hlc, Hash: b.fullHash})
}

// loadIssueEnvelopes reads all *.json op files under rootOps/<issueID>/ and
// returns parsed envelopes. Missing root or issue subtree yields an empty
// slice with no error (matches fold.FoldIssue's contract).
func loadIssueEnvelopes(rootOps, issueID string) ([]op.Envelope, error) {
	pattern := filepath.Join(rootOps, issueID, "*", "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("claim: glob: %w", err)
	}
	envs := make([]op.Envelope, 0, len(matches))
	for _, path := range matches {
		body, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("claim: read %s: %w", path, err)
		}
		env, err := op.Unmarshal(body)
		if err != nil {
			return nil, fmt.Errorf("claim: unmarshal %s: %w", path, err)
		}
		envs = append(envs, env)
	}
	return envs, nil
}

// repoReference returns the HLC reference recorded in .act/config.json's
// last_hlc, used by the plausibility check. Missing config or missing
// last_hlc returns a zero HLC, which makes the check default to
// "compare against now()" per hlc.Clock.Plausible.
func repoReference(repoRoot string) (hlc.HLC, error) {
	paths := config.Layout(repoRoot)
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		// Missing config is non-fatal; tests construct minimal repos
		// without the file, and the clock's plausibility check tolerates
		// a zero reference.
		return hlc.HLC{}, nil
	}
	return hlc.HLC{
		Wall:    cfg.LastHLC.Wall,
		Logical: cfg.LastHLC.Logical,
	}, nil
}

// readFile is var-indirect for fault-injection tests.
var readFile = osReadFile

// commitMarkerLen is the length of the parenthesized short id used in the
// claim auto-commit subject (`(act-XXXX)`). It mirrors
// cli.CommitMarkerLen but is inlined here to avoid a cli→claim→cli import
// cycle (cli depends on claim). The constant is intentionally small —
// `len("act-") + 4` — and load-bearing across the codebase; if either side
// changes, both must move together. See act-d3a5.
const commitMarkerLen = len("act-") + 4

// buildClaimCommitMessage returns the canonical auto-commit subject for a
// claim op: `act-op: (act-XXXX) claim`. Mirrors cli.BuildOpCommitMessage
// for op_type="claim" without taking a cli import. The previous form
// `act-<id>: claim <assignee>` (issueID already has the `act-` prefix)
// produced double-prefixed subjects (`act-act-XXXX: claim …`) and broke
// doctor's orphan-close grep, which keys on the literal `(act-XXXX)`
// marker. See act-d3a5.
func buildClaimCommitMessage(issueID string) string {
	short := issueID
	if len(issueID) > commitMarkerLen {
		short = issueID[:commitMarkerLen]
	}
	return fmt.Sprintf("act-op: (%s) claim", short)
}
