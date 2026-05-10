package mcp

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// nextBaseDelays is the per-attempt base sleep schedule for act_next's
// claim-loss backoff, per spec §5.D.1: 100ms, 400ms, 1.6s. Sum = 2.1s.
var nextBaseDelays = []time.Duration{
	100 * time.Millisecond,
	400 * time.Millisecond,
	1600 * time.Millisecond,
}

// nextMaxAttempts is the bounded retry budget per spec §5.D.1.
const nextMaxAttempts = 3

// nextCandidateLimit is the size of the candidate slice returned on
// retry-budget exhaustion. Spec §"act_next" doesn't pin an exact value; we
// use 5 so callers see enough alternatives without flooding the wire.
const nextCandidateLimit = 5

// jitterFunc returns a uniform-random multiplier in [0.75, 1.25] per
// spec §5.D.1. Tests inject a deterministic source returning 1.0 to make
// the budget exact.
type jitterFunc func() float64

// sleepFunc abstracts time.Sleep so tests can advance a mock clock instead
// of the wall-clock. Per spec §5.D.5 the test's elapsed-time assertion
// (2.1s ± 50ms) uses an injected sleep counter.
type sleepFunc func(time.Duration)

// defaultJitter draws from math/rand seeded once at server start. Each
// call returns one fresh sample in [0.75, 1.25]; per spec §5.D.1 the
// jitter MUST be re-rolled per attempt, which is the natural consequence
// of calling this function from inside the retry loop.
func defaultJitter() float64 {
	return 0.75 + rand.Float64()*0.5
}

// composedDeps groups the pluggable dependencies that act_next needs for
// deterministic testing. Production code passes nil → defaults.
type composedDeps struct {
	jitter jitterFunc
	sleep  sleepFunc
}

func (d composedDeps) jitterOrDefault() jitterFunc {
	if d.jitter != nil {
		return d.jitter
	}
	return defaultJitter
}

func (d composedDeps) sleepOrDefault() sleepFunc {
	if d.sleep != nil {
		return d.sleep
	}
	return time.Sleep
}

// callNext implements the `act_next` composed tool: ready → claim →
// show, with bounded-retry on claim loss per spec §5.D.1.
//
// Returns the JSON-shaped result and an isError flag.
func (s *Server) callNext(raw json.RawMessage) (any, bool) {
	return s.callNextWithDeps(raw, composedDeps{})
}

// callNextWithDeps is the test seam: same as callNext but accepts a
// deterministic clock + jitter source per spec §5.D.5.
func (s *Server) callNextWithDeps(raw json.RawMessage, deps composedDeps) (any, bool) {
	var args struct {
		Under    string `json:"under"`
		ReadOnly bool   `json:"read_only"`
		Isolated bool   `json:"isolated"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return errEnvelope("bad_args", err.Error()), true
		}
	}
	if args.ReadOnly || s.readOnly {
		return errEnvelope("read_only_violation", "act_next: server or call is read-only"), true
	}

	jitter := deps.jitterOrDefault()
	sleep := deps.sleepOrDefault()

	// Step 1: gather the ready set (filtered by under).
	readyOut, code := cli.RunReady(s.repoRoot, cli.ReadyOptions{
		Under:  args.Under,
		AsJSON: true,
	})
	if code != 0 {
		return readyOut, true
	}
	res, ok := readyOut.(cli.ReadyResult)
	if !ok {
		return errEnvelope("internal", fmt.Sprintf("ready: unexpected type %T", readyOut)), true
	}
	if len(res.Ready) == 0 {
		return map[string]any{
			"claimed":    false,
			"candidates": []cli.ReadyIssue{},
		}, false
	}

	// Tracks ids that have lost their claim race during this run; they are
	// excluded from re-fold per spec §"act_next refolds and excludes
	// just-lost ids each attempt".
	lost := make(map[string]bool)

	// Bounded-retry loop: each attempt grabs the (refreshed) head of the
	// ready set, attempts a claim, and either returns the show or sleeps
	// before the next attempt. Per §5.D.5 we observe exactly 3 attempts
	// when every claim loses; the loop sleeps after attempts 1 and 2 and
	// after attempt 3 (the test asserts on that final sleep too — §5.D.5
	// says total elapsed = 2.1s = 100+400+1600).
	for attempt := 0; attempt < nextMaxAttempts; attempt++ {
		// Refresh the ready set on attempt > 0 so newly-arrived ops are
		// visible (per acceptance: "refolds and excludes just-lost ids").
		if attempt > 0 {
			refreshed, rcode := cli.RunReady(s.repoRoot, cli.ReadyOptions{
				Under:  args.Under,
				AsJSON: true,
			})
			if rcode == 0 {
				if rr, ok := refreshed.(cli.ReadyResult); ok {
					res = rr
				}
			}
		}

		// Pick the first not-yet-lost candidate.
		var pick *cli.ReadyIssue
		for i := range res.Ready {
			if !lost[res.Ready[i].ID] {
				pick = &res.Ready[i]
				break
			}
		}
		if pick == nil {
			// No remaining candidates this attempt — sleep + retry.
			delay := time.Duration(float64(nextBaseDelays[attempt]) * jitter())
			sleep(delay)
			continue
		}

		// Step 2: attempt claim.
		claimOut, claimCode := cli.RunUpdate(s.repoRoot, cli.UpdateOptions{
			ID:       pick.ID,
			Claim:    true,
			AsJSON:   true,
			Isolated: args.Isolated,
		})
		if claimCode == 0 {
			// Step 3: show the issue.
			showOut, showCode := cli.RunShow(s.repoRoot, cli.ShowOptions{
				ID:     pick.ID,
				AsJSON: true,
			})
			if showCode != 0 {
				return showOut, true
			}
			var issueJSON any = showOut
			// commit_marker carries the `(act-XXXX)` string the caller
			// MUST embed in any work-commit message for this issue, so
			// `act doctor` orphan-close can correlate the close with a
			// real commit. We derive it from the same shortest-unique
			// prefix the CLI exposes via show's `short_id`; fall back
			// to the full id if that lookup misses (defensive — show
			// always populates short_id for live ids).
			short := pick.ID
			if sr, ok := showOut.(cli.ShowResult); ok {
				m := sr.ShowJSON()
				issueJSON = m
				if s, ok := m["short_id"].(string); ok && s != "" {
					short = s
				}
			}
			return map[string]any{
				"claimed":       true,
				"issue":         issueJSON,
				"commit_marker": "(" + short + ")",
			}, false
		}
		// Claim lost (or other non-zero). Mark id as lost and back off.
		_ = claimOut
		lost[pick.ID] = true
		delay := time.Duration(float64(nextBaseDelays[attempt]) * jitter())
		sleep(delay)
	}

	// Budget exhausted. Return the top-N current ready set as candidates.
	cands := res.Ready
	if len(cands) > nextCandidateLimit {
		cands = cands[:nextCandidateLimit]
	}
	return map[string]any{
		"claimed":    false,
		"candidates": cands,
	}, false
}

// callFinish implements the `act_finish` composed tool: a thin wrapper
// over act close that surfaces a uniform `{closed, id, short_id}` shape
// per the issue acceptance criteria.
func (s *Server) callFinish(raw json.RawMessage) (any, bool) {
	var args struct {
		ID       string `json:"id"`
		Reason   string `json:"reason"`
		NoCommit bool   `json:"no_commit"`
		Push     bool   `json:"push"`
		Isolated bool   `json:"isolated"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	if args.ReadOnly || s.readOnly {
		return errEnvelope("read_only_violation", "act_finish: server or call is read-only"), true
	}
	if args.ID == "" {
		return errEnvelope("bad_args", "act_finish: id is required"), true
	}
	out, code := cli.RunClose(s.repoRoot, cli.CloseOptions{
		ID:       args.ID,
		Reason:   args.Reason,
		AsJSON:   true,
		NoCommit: args.NoCommit,
		Push:     args.Push,
		Isolated: args.Isolated,
	})
	if code != 0 {
		return out, true
	}
	switch r := out.(type) {
	case cli.CloseResult:
		return map[string]any{
			"closed":   true,
			"id":       r.ID,
			"short_id": r.ShortID,
			"reason":   r.Reason,
		}, false
	case cli.CloseAlreadyClosed:
		return map[string]any{
			"closed":         true,
			"id":             r.ID,
			"already_closed": true,
		}, false
	default:
		return out, false
	}
}

// callBlock implements the `act_block` composed tool: writes both
// `update_field status=blocked` and `add_dep type=blocks` ops in a single
// git commit per spec §5.D.2.
func (s *Server) callBlock(raw json.RawMessage) (any, bool) {
	return s.callBlockWithGops(raw, nil)
}

// gopsFactory abstracts gitops construction so tests can inject a stub
// that fails on Commit (to exercise the rollback path).
type gopsFactory func(repoRoot string) blockGitOps

// blockGitOps is the subset of *gitops.GitOps that act_block needs. The
// real type satisfies this interface natively.
type blockGitOps interface {
	StageOpFile(path string) error
	Commit(message string) error
	Push() error
	Root() string
}

// realBlockGitOps wraps *gitops.GitOps to satisfy blockGitOps. Root is
// added as a small helper for the unstage path.
type realBlockGitOps struct{ inner *gitops.GitOps }

func (r realBlockGitOps) StageOpFile(p string) error { return r.inner.StageOpFile(p) }
func (r realBlockGitOps) Commit(msg string) error    { return r.inner.Commit(msg) }
func (r realBlockGitOps) Push() error                { return r.inner.Push() }
func (r realBlockGitOps) Root() string               { return r.inner.RepoRoot }

func (s *Server) callBlockWithGops(raw json.RawMessage, factory gopsFactory) (any, bool) {
	var args struct {
		ID        string `json:"id"`
		BlockedBy string `json:"blocked_by"`
		Reason    string `json:"reason"`
		NoCommit  bool   `json:"no_commit"`
		Push      bool   `json:"push"`
		Isolated  bool   `json:"isolated"`
		ReadOnly  bool   `json:"read_only"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	if args.ReadOnly || s.readOnly {
		return errEnvelope("read_only_violation", "act_block: server or call is read-only"), true
	}
	if args.ID == "" || args.BlockedBy == "" {
		return errEnvelope("bad_args", "act_block: id and blocked_by are required"), true
	}
	if args.NoCommit && args.Push {
		return errEnvelope("bad_args", "act_block: --no-commit and --push are mutually exclusive"), true
	}
	if args.Isolated && args.Push {
		return errEnvelope("bad_args", "act_block: --isolated and --push are mutually exclusive"), true
	}

	paths := config.Layout(s.repoRoot)

	// Resolve both ids via the prefix pipeline so the user can pass shorts.
	knownIDs, err := cli.ListIssueIDs(paths.Ops)
	if err != nil {
		return errEnvelope("ops_scan_failed", err.Error()), true
	}
	full, rerr := ids.Resolve(args.ID, knownIDs)
	if rerr != nil {
		return errEnvelope("issue_not_found", rerr.Error()), true
	}
	parentFull, rerr := ids.Resolve(args.BlockedBy, knownIDs)
	if rerr != nil {
		return errEnvelope("issue_not_found", rerr.Error()), true
	}
	if full == parentFull {
		return errEnvelope("bad_args", "act_block: id and blocked_by must differ"), true
	}

	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return errEnvelope("config_read_failed", cerr.Error()), true
	}

	// Build both envelopes with the SAME HLC clock so their stamps are
	// monotonic within this commit.
	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })

	// Op 1: update_field status=blocked.
	statusVal, _ := json.Marshal("blocked")
	statusPayload := op.UpdateFieldPayload{Field: "status", Value: statusVal}
	statusBody, perr := canonicaljson.Marshal(statusPayload)
	if perr != nil {
		return errEnvelope("marshal_failed", perr.Error()), true
	}
	if verr := statusPayload.Validate(); verr != nil {
		return errEnvelope("payload_invalid", verr.Error()), true
	}
	stamp1 := clock.Send()
	stamp1.NodeID = cfg.NodeID
	envStatus := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "update_field",
		IssueID:       full,
		Payload:       statusBody,
		HLC:           stamp1,
		NodeID:        cfg.NodeID,
	}
	if verr := envStatus.Validate(); verr != nil {
		return errEnvelope("envelope_invalid", verr.Error()), true
	}
	statusEnvBody, merr := envStatus.Marshal()
	if merr != nil {
		return errEnvelope("marshal_failed", merr.Error()), true
	}

	// Op 2: add_dep type=blocks (full → parentFull).
	depPayload := op.AddDepPayload{Parent: parentFull, EdgeType: "blocks"}
	if verr := depPayload.Validate(); verr != nil {
		return errEnvelope("payload_invalid", verr.Error()), true
	}
	depBody, perr := canonicaljson.Marshal(depPayload)
	if perr != nil {
		return errEnvelope("marshal_failed", perr.Error()), true
	}
	stamp2 := clock.Send()
	stamp2.NodeID = cfg.NodeID
	envDep := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "add_dep",
		IssueID:       full,
		Payload:       depBody,
		HLC:           stamp2,
		NodeID:        cfg.NodeID,
	}
	if verr := envDep.Validate(); verr != nil {
		return errEnvelope("envelope_invalid", verr.Error()), true
	}
	depEnvBody, merr := envDep.Marshal()
	if merr != nil {
		return errEnvelope("marshal_failed", merr.Error()), true
	}

	// Single-commit atomic write per §5.D.2.
	var gops *gitops.GitOps
	var bgops blockGitOps
	if !args.NoCommit {
		if factory != nil {
			bgops = factory(s.repoRoot)
		} else {
			gops = gitops.NewGitOps(s.repoRoot)
			bgops = realBlockGitOps{inner: gops}
		}
	}

	// Block-composed commit subject: `act-block: (act-XXXX)`. Parens
	// match the shape doctor's orphan-close grep keys on (act-d3a5) so a
	// block-then-close sequence still correlates the close with a commit.
	commitMsg := fmt.Sprintf("act-block: (%s)", cli.ShortIssueID(full))

	// Use the underlying real *gitops.GitOps when no factory was injected;
	// the factory path lets tests trigger commit failure to exercise the
	// rollback. The shared util helper expects a real *gitops.GitOps so the
	// rollback path can call git restore --staged via runUnstage. To keep
	// that helper canonical, we delegate to it for the production case and
	// inline the rollback for the test-injected case.
	envs := []op.Envelope{envStatus, envDep}
	bodies := [][]byte{statusEnvBody, depEnvBody}
	opts := cli.WriteOpts{
		NoCommit: args.NoCommit,
		Push:     args.Push,
		Isolated: args.Isolated,
	}

	if factory == nil {
		// Production path: use the canonical helper.
		if err := cli.WriteOpsAndAutoCommit(envs, bodies, paths, gops, opts, commitMsg); err != nil {
			return errEnvelope("block_failed", err.Error()), true
		}
	} else {
		// Test path: replicate the helper's logic against the injected
		// blockGitOps so we can exercise commit failure.
		if err := writeBlockOpsViaInterface(envs, bodies, paths, bgops, opts, commitMsg); err != nil {
			return errEnvelope("block_failed", err.Error()), true
		}
	}

	return map[string]any{
		"ok":          true,
		"id":          full,
		"blocked_by":  parentFull,
		"ops_written": []string{"set-status", "dep-add"},
	}, false
}

// writeBlockOpsViaInterface mirrors cli.WriteOpsAndAutoCommit but accepts a
// blockGitOps interface so tests can inject a stub that fails on Commit
// (exercising the rollback path in TestActBlockRollbackOnFailure). The
// production path uses cli.WriteOpsAndAutoCommit directly.
func writeBlockOpsViaInterface(envs []op.Envelope, bodies [][]byte, paths config.LayoutPaths, gops blockGitOps, opts cli.WriteOpts, commitMessage string) error {
	if len(envs) == 0 || len(envs) != len(bodies) {
		return fmt.Errorf("mcp: writeBlockOpsViaInterface: bad ops len")
	}
	if opts.NoCommit && opts.Push {
		return fmt.Errorf("mcp: --no-commit and --push are mutually exclusive")
	}
	if !opts.NoCommit && gops == nil {
		return fmt.Errorf("mcp: gitops is required unless --no-commit is set")
	}

	fsLock := func() (func(), error) { return func() {}, nil }
	written := make([]string, 0, len(envs))
	rollback := func() {
		for _, p := range written {
			_ = removeOpFile(p)
		}
	}
	for i, env := range envs {
		opPath, _, err := op.ProbeAndWrite(paths.Ops, env, bodies[i], fsLock)
		if err != nil {
			rollback()
			return fmt.Errorf("write op %d/%d: %w", i+1, len(envs), err)
		}
		written = append(written, opPath)
	}

	if opts.NoCommit {
		return nil
	}

	for _, p := range written {
		if err := gops.StageOpFile(p); err != nil {
			rollback()
			return fmt.Errorf("stage: %w", err)
		}
	}
	if err := gops.Commit(commitMessage); err != nil {
		rollback()
		return fmt.Errorf("commit: %w", err)
	}
	if opts.Push {
		if err := gops.Push(); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	}
	return nil
}

// removeOpFile is the indirection used by writeBlockOpsViaInterface's
// rollback path. Pulled out so tests can verify the file is removed even
// when StageOpFile / Commit fails.
func removeOpFile(path string) error {
	return os.Remove(path)
}
