package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
	"github.com/aac/act/internal/redaction"
)

// `act redact` (docs/redaction-design.md §5): remove listed strings from
// every stored op file by re-issuing the files that hold them, record that
// it happened, commit, publish, and verify.
//
// SECURITY POSTURE. The patterns are the secret. Nothing this file returns
// or prints — a result, an error message, error details, a warning — holds
// a pattern, a matched string or the text around a match. Errors report the
// failing step, counts and paths (relative to .act/). The redaction package
// keeps the same promise for everything it returns.

// redactMaxRounds bounds the plan/execute/publish rounds of one run
// (docs/redaction-design.md §5.6 step 6).
const redactMaxRounds = 5

// RedactOptions captures `act redact`.
type RedactOptions struct {
	// Patterns are the strings to remove, as redaction.LoadPatterns
	// returned them. Never printed.
	Patterns []string
	// DryRun plans against the tree as it stands — no lock, no fetch, no
	// write — and reports the counts a run would produce.
	DryRun bool
	// Stderr receives the publish-deferred warning; nil means os.Stderr.
	Stderr io.Writer
}

// RedactResult is the result object (docs/redaction-design.md §5.7). Every
// key is always present except the three an unconfirmed run adds.
type RedactResult struct {
	OK                   bool   `json:"ok"`
	Error                string `json:"error,omitempty"`
	DryRun               bool   `json:"dry_run"`
	Patterns             int    `json:"patterns"`
	Issues               int    `json:"issues"`
	OpsRewritten         int    `json:"ops_rewritten"`
	Replacements         int    `json:"replacements"`
	LeftoversRemoved     int    `json:"leftovers_removed"`
	QuarantinedRewritten int    `json:"quarantined_rewritten"`
	Published            bool   `json:"published"`
	PushDeferred         bool   `json:"push_deferred,omitempty"`
	PushDeferredReason   string `json:"push_deferred_reason,omitempty"`

	// NoRemote is true when the nested repo has no origin.
	NoRemote bool `json:"-"`
	// Changed is true when a round of this run committed something.
	Changed bool `json:"-"`
	// PublishedNow is true when this run pushed a commit to origin (its
	// own, or one an earlier run left unpublished).
	PublishedNow bool `json:"-"`
}

// RedactErrorOutput is the failure envelope.
type RedactErrorOutput struct {
	Error   string         `json:"error"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// redactTotals accumulates counts across rounds.
type redactTotals struct {
	counts redaction.Counts
	issues map[string]bool
}

func (t *redactTotals) add(perIssue map[string]redaction.Counts) {
	for id, c := range perIssue {
		t.counts.OpsRewritten += c.OpsRewritten
		t.counts.Replacements += c.Replacements
		t.counts.LeftoversRemoved += c.LeftoversRemoved
		t.counts.QuarantinedRewritten += c.QuarantinedRewritten
		if c.OpsRewritten > 0 || c.QuarantinedRewritten > 0 {
			t.issues[id] = true
		}
	}
}

func (t *redactTotals) result(patterns int, dryRun bool) RedactResult {
	return RedactResult{
		OK:                   true,
		DryRun:               dryRun,
		Patterns:             patterns,
		Issues:               len(t.issues),
		OpsRewritten:         t.counts.OpsRewritten,
		Replacements:         t.counts.Replacements,
		LeftoversRemoved:     t.counts.LeftoversRemoved,
		QuarantinedRewritten: t.counts.QuarantinedRewritten,
	}
}

func redactFailed(step string, err error) (any, int) {
	return RedactErrorOutput{
		Error:   ErrRedactFailed,
		Message: fmt.Sprintf("act redact: %s: %v", step, err),
	}, 1
}

// redactResidual is the redact_residual envelope. changed says whether a
// round of this run committed before the residual was found.
func redactResidual(paths []string, changed bool) (any, int) {
	where := "hold a listed string where act does not rewrite it; nothing was changed"
	if changed {
		where = "still hold a listed string after the rewrite, which is committed"
	}
	return RedactErrorOutput{
		Error:   ErrRedactResidual,
		Message: fmt.Sprintf("act redact: %d file(s) under .act/ %s: %s", len(paths), where, strings.Join(paths, ", ")),
		Details: map[string]any{"paths": paths, "changed": changed},
	}, 1
}

// RunRedact implements `act redact` from the repo checks on
// (docs/redaction-design.md §5.6 steps 2–9); the caller has parsed the
// flags and loaded the patterns (step 1).
func RunRedact(repoRoot string, opts RedactOptions) (output any, exitCode int) {
	if !hasGitDir(repoRoot) {
		return RedactErrorOutput{
			Error:   ErrNotInGit,
			Message: fmt.Sprintf("act redact: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		return RedactErrorOutput{
			Error:   ErrActNotInitialized,
			Message: fmt.Sprintf("act redact: %s/.act not initialized; run `act init` first", repoRoot),
		}, 3
	}
	if len(opts.Patterns) == 0 {
		return redactFailed("patterns", errors.New("no patterns"))
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	// Step 3: a dry run plans against the tree as it stands.
	if opts.DryRun {
		plan, err := redaction.NewPlan(paths.Root, opts.Patterns, nil)
		if err != nil {
			return redactFailed("plan", err)
		}
		if len(plan.Residuals) > 0 {
			return redactResidual(plan.Residuals, false)
		}
		t := redactTotals{issues: map[string]bool{}}
		t.add(plan.PerIssue)
		res := t.result(len(opts.Patterns), true)
		gops := gitops.NewActGitOps(paths.Root)
		if _, err := gops.RemoteURL("origin"); err != nil {
			res.NoRemote = true
		}
		return res, 0
	}

	// Step 4: the write lock, held to the end.
	release, lerr := gitops.AcquireWriteLock(paths.Root)
	if lerr != nil {
		if msg, details, isTimeout := WriteLockTimeoutDetails(lerr); isTimeout {
			return RedactErrorOutput{Error: ErrWriteLockTimeout, Message: msg, Details: details}, 1
		}
		return redactFailed("write lock", lerr)
	}
	defer release()

	gops := gitops.NewActGitOps(paths.Root)
	originURL, oerr := gops.RemoteURL("origin")
	hasOrigin := oerr == nil

	// Step 5: the first fetch is mandatory. Redacting a stale tree is how
	// two redactions come to disagree (docs/redaction-design.md §6.3).
	if hasOrigin {
		branch, berr := gops.CurrentBranch()
		if berr != nil {
			return redactFailed("read branch", berr)
		}
		if ferr := gops.FetchAndRebase(branch); ferr != nil && !errors.Is(ferr, gitops.ErrShallowRecovered) {
			ferr = gops.RedactError(ferr)
			if errors.Is(ferr, gitops.ErrFetchFailed) {
				shownURL := gitops.RedactURLUserinfo(originURL)
				return RedactErrorOutput{
					Error:   ErrRemoteUnreachable,
					Message: fmt.Sprintf("act redact: fetch %s: %v; nothing was changed", shownURL, ferr),
					Details: map[string]any{
						"url":         shownURL,
						"stderr_tail": CaptureStderrTail(ferr.Error()),
					},
				}, 3
			}
			return redactFailed("fetch and rebase", ferr)
		}
	}

	nodeID, nerr := config.ResolveNodeID(paths)
	if nerr != nil {
		return redactFailed("read node_id", nerr)
	}
	clock := hlc.NewClock(nodeID, func() int64 { return time.Now().UnixMilli() })

	// Step 6: rounds.
	totals := redactTotals{issues: map[string]bool{}}
	own := map[string]bool{}
	changed := false
	var pub PublishResult
	// published: origin holds everything this checkout committed.
	// publishedNow: this run pushed something to get it there.
	published, publishedNow := false, false
	for round := 1; ; round++ {
		plan, err := redaction.NewPlan(paths.Root, opts.Patterns, own)
		if err != nil {
			return redactFailed("plan", err)
		}
		if round == 1 && len(plan.Residuals) > 0 {
			return redactResidual(plan.Residuals, false)
		}
		_, perIssue, err := plan.Execute()
		if err != nil {
			return redactFailed("rewrite", err)
		}
		totals.add(perIssue)
		if err := redactWriteRecords(paths, clock, nodeID, perIssue); err != nil {
			return redactFailed("record", err)
		}
		touched, err := redactTouchedIssues(gops, paths, plan.TouchedIssues())
		if err != nil {
			return redactFailed("stage", err)
		}
		committed, err := redactCommit(gops, touched, perIssue)
		if err != nil {
			return redactFailed("commit", err)
		}
		if committed {
			changed = true
		}
		n := plan.Changes()
		if committed && n == 0 {
			// A pass an earlier run left uncommitted, finished here.
			n = 1
		}
		if !hasOrigin {
			break
		}
		unpublished, uerr := gops.Unpublished()
		if uerr != nil {
			return redactFailed("read publish state", uerr)
		}
		if n == 0 && !unpublished {
			published = true
			break
		}
		pub = PublishCommittedOp(gops, "redact", "", stderr)
		if pub.Deferred {
			break
		}
		published, publishedNow = true, true
		if n == 0 {
			break
		}
		if round == redactMaxRounds {
			return redactFailed("rounds", fmt.Errorf("round %d still changed %d file(s); re-run to finish", round, n))
		}
	}

	// Step 7: the index holds titles and descriptions, and SQLite does not
	// zero what it frees, so it is removed and rebuilt rather than updated
	// (docs/redaction-design.md §7) — always, even when nothing matched.
	if err := redactRebuildIndex(paths); err != nil {
		return RedactErrorOutput{
			Error:   ErrIndexRebuildFail,
			Message: fmt.Sprintf("act redact: rebuild index: %v", err),
		}, 1
	}

	// Step 8: the verification scan, every file under .act/ outside .git.
	hits, err := redaction.Scan(paths.Root, opts.Patterns)
	if err != nil {
		return redactFailed("scan", err)
	}
	if len(hits) > 0 {
		return redactResidual(hits, changed)
	}

	// Step 9.
	res := totals.result(len(opts.Patterns), false)
	res.NoRemote = !hasOrigin
	res.Changed = changed
	if pub.Deferred {
		reason := ""
		if pub.Reason != nil {
			reason = pub.Reason.Error()
		}
		return RedactResult{
			OK:                   false,
			Error:                ErrRedactUnconfirmed,
			DryRun:               false,
			Patterns:             res.Patterns,
			Issues:               res.Issues,
			OpsRewritten:         res.OpsRewritten,
			Replacements:         res.Replacements,
			LeftoversRemoved:     res.LeftoversRemoved,
			QuarantinedRewritten: res.QuarantinedRewritten,
			Published:            false,
			PushDeferred:         true,
			PushDeferredReason:   reason,
			Changed:              changed,
		}, 6
	}
	res.Published = hasOrigin && published
	res.PublishedNow = publishedNow
	return res, 0
}

// redactWriteRecords writes one `redact` record op into each issue in which
// this pass re-issued at least one op file under ops/
// (docs/redaction-design.md §5.5). The record holds the two counts and
// nothing else: no pattern, no hash of one, no name of a replaced file.
func redactWriteRecords(paths config.LayoutPaths, clock *hlc.Clock, nodeID string, perIssue map[string]redaction.Counts) error {
	ids := make([]string, 0, len(perIssue))
	for id, c := range perIssue {
		if c.OpsRewritten > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	noLock := func() (func(), error) { return func() {}, nil }
	for _, id := range ids {
		c := perIssue[id]
		payload := op.RedactPayload{OpsRewritten: c.OpsRewritten, Replacements: c.Replacements}
		if err := payload.Validate(); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		body, err := canonicaljson.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		stamp := clock.Send()
		stamp.NodeID = nodeID
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "redact",
			IssueID:       id,
			Payload:       body,
			HLC:           stamp,
			NodeID:        nodeID,
		}
		if err := env.Validate(); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		if err := op.ValidatePayload(env.OpType, env.Payload); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		out, err := env.Marshal()
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		if _, _, err := op.ProbeAndWrite(paths.Ops, env, out, noLock); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
	}
	return nil
}

// redactTouchedIssues returns, sorted, the issues whose ops/ subtree this
// pass commits: those the plan changed, plus every issue that holds a
// re-issued (marked) op and has uncommitted changes under ops/<issue> — a
// pass an earlier run was killed in the middle of, which this plan sees as
// already done (docs/redaction-design.md §5.4).
func redactTouchedIssues(gops *gitops.ActGitOps, paths config.LayoutPaths, planned []string) ([]string, error) {
	set := map[string]bool{}
	for _, id := range planned {
		set[id] = true
	}
	out, err := gops.RunGit("status", "--porcelain", "--untracked-files=all", "--", "ops")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.Trim(line[3:], `"`)
		if _, after, ok := strings.Cut(p, " -> "); ok {
			p = strings.Trim(after, `"`)
		}
		rest, ok := strings.CutPrefix(p, "ops/")
		if !ok {
			continue
		}
		id, _, _ := strings.Cut(rest, "/")
		if id == "" || set[id] {
			continue
		}
		marked, err := redactHoldsMarkedOp(filepath.Join(paths.Ops, id))
		if err != nil {
			return nil, err
		}
		if marked {
			set[id] = true
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// redactHoldsMarkedOp reports whether any op file under dir carries a
// redaction generation.
func redactHoldsMarkedOp(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if errors.Is(werr, fs.ErrNotExist) {
				return nil
			}
			return werr
		}
		if found || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if env, err := op.UnmarshalTolerant(b); err == nil && fold.Generation(env) > 0 {
			found = true
		}
		return nil
	})
	return found, err
}

// redactCommit stages ops/<issue> for every touched issue and makes one
// pathspec-scoped commit of those directories, --no-verify: no hook runs,
// since hooks fire once per logical op on the writer that originated it
// and a re-issued create is not a create. It reports whether it committed.
func redactCommit(gops *gitops.ActGitOps, issues []string, perIssue map[string]redaction.Counts) (bool, error) {
	if len(issues) == 0 {
		return false, nil
	}
	specs := make([]string, len(issues))
	for i, id := range issues {
		specs[i] = "ops/" + id
	}
	if _, err := gops.RunGit(append([]string{"add", "-A", "--"}, specs...)...); err != nil {
		return false, err
	}
	staged, err := gops.RunGit(append([]string{"diff", "--cached", "--name-status", "--"}, specs...)...)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(staged) == "" {
		return false, nil
	}
	rewritten, inIssues := 0, 0
	for _, id := range issues {
		if c := perIssue[id]; c.OpsRewritten > 0 {
			rewritten += c.OpsRewritten
			inIssues++
		}
	}
	var subject string
	if rewritten > 0 {
		subject = fmt.Sprintf("act-redact: re-issued %d op file(s) in %d issue(s)", rewritten, inIssues)
	} else {
		removed := 0
		for _, line := range strings.Split(staged, "\n") {
			if strings.HasPrefix(line, "D\t") {
				removed++
			}
		}
		subject = fmt.Sprintf("act-redact: removed %d superseded op file(s)", removed)
	}
	if _, err := gops.RunGit(append([]string{"commit", "-q", "-m", subject, "--no-verify", "--"}, specs...)...); err != nil {
		return false, err
	}
	return true, nil
}

// redactRebuildIndex removes index.db and its SQLite sidecars and builds a
// fresh index from the op files. It stands in for index.ScrubDerived, which
// removes the whole derived set, until that lands.
func redactRebuildIndex(paths config.LayoutPaths) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(paths.IndexDB + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return err
	}
	if err := idx.Rebuild(paths.Ops); err != nil {
		_ = idx.Close()
		return err
	}
	return idx.Close()
}

// FormatRedactHuman renders a result as the human form
// (docs/redaction-design.md §5.7).
func FormatRedactHuman(res RedactResult) string {
	var b strings.Builder
	zero := res.OpsRewritten == 0 && res.Replacements == 0 && res.LeftoversRemoved == 0 && res.QuarantinedRewritten == 0
	switch {
	case zero:
		fmt.Fprintf(&b, "No stored file holds any of the %d pattern(s). Nothing was changed.\n", res.Patterns)
	case res.DryRun:
		fmt.Fprintf(&b, "Would redact %d occurrence(s) in %d op file(s) across %d issue(s); would remove %d leftover file(s).", res.Replacements, res.OpsRewritten, res.Issues, res.LeftoversRemoved)
		if res.QuarantinedRewritten > 0 {
			fmt.Fprintf(&b, " Would rewrite %d set-aside op file(s).", res.QuarantinedRewritten)
		}
		b.WriteString(" Nothing was changed.\n")
	default:
		fmt.Fprintf(&b, "Redacted %d occurrence(s) in %d op file(s) across %d issue(s); removed %d leftover file(s).", res.Replacements, res.OpsRewritten, res.Issues, res.LeftoversRemoved)
		if res.QuarantinedRewritten > 0 {
			fmt.Fprintf(&b, " Rewrote %d set-aside op file(s).", res.QuarantinedRewritten)
		}
		b.WriteString("\n")
	}
	if res.DryRun {
		return b.String()
	}
	switch {
	case res.Error == ErrRedactUnconfirmed:
		b.WriteString("Not published (redact_unconfirmed): origin and every other checkout still hold the text. Re-run the same command once origin is reachable.\n")
	case zero && !res.Changed && !res.PublishedNow:
		return b.String()
	case res.NoRemote:
		b.WriteString("No origin remote: this checkout is the only copy act knows of.\n")
	default:
		b.WriteString("Published to origin.\n")
	}
	if res.Changed {
		b.WriteString("Not rewritten: the git history of .act/ still holds the original text, here, on origin and in every clone and backup.\n")
	}
	return b.String()
}
