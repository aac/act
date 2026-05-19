package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
)

// ReadyOptions captures the flag knobs for `act ready`.
type ReadyOptions struct {
	// Under is an optional id prefix; when set, the result is restricted
	// to descendants of the resolved issue (via the parent chain).
	Under string
	// Limit caps the result size. Zero or negative means use the default
	// (50).
	Limit int
	// AsJSON is reserved for symmetry with other commands; the returned
	// shape is identical and main.go decides how to render.
	AsJSON bool
	// AssigneeFilter restricts the ready set to issues whose assignee
	// matches this exact string. Empty means no filter (status quo).
	// Used by `act ready --mine` (which sets it to the current node id)
	// and `--as <id>` (which sets it to an explicit override). The filter
	// is a post-pass on the already-computed ready set; --under composes.
	AssigneeFilter string
	// Fresh, when true, forces the read-path cache layer to fetch+rebase
	// before reading state, regardless of FETCH_HEAD freshness. Wired by
	// `act ready --fresh` and the `--no-cache` alias (Phase 2 ticket 5).
	Fresh bool
}

// ReadyIssue is one row of the ready set.
type ReadyIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"short_id"`
	Title     string `json:"title"`
	Priority  int    `json:"priority"`
	Status    string `json:"status"`
	Assignee  string `json:"assignee,omitempty"`
	ClaimedAt string `json:"claimed_at,omitempty"`
}

// ReadyResult is the JSON-serialisable success envelope.
type ReadyResult struct {
	Ready []ReadyIssue `json:"ready"`
	Count int          `json:"count"`
}

// ReadyErrorOutput is the failure envelope. Candidates is non-nil only on
// the id_ambiguous path; it is also mirrored under Details["candidates"] so
// the on-the-wire JSON envelope matches spec §"Errors".
type ReadyErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// defaultReadyLimit matches spec §act ready: bound the result count at 50
// when --limit is not supplied.
const defaultReadyLimit = 50

// RunReady implements `act ready`.
//
// Algorithm (per spec §3 act ready, see also docs/issues/act-e1d4.md):
//  1. Require repo + .act/. Missing → exit 3.
//  2. Open the index and Rebuild for freshness.
//  3. Compute the "ready" set: issues with status==open and no `blocks`
//     parent that is itself non-closed and non-tombstoned.
//  4. If opts.Under is non-empty, resolve it (prefix), then restrict the
//     ready set to descendants of that issue along the parent chain.
//  5. Sort by (priority asc, created_at desc, id asc).
//  6. Apply Limit (default 50).
//
// Returns:
//   - output: ReadyResult on success, ReadyErrorOutput on failure.
//   - exitCode: 0 success; 2 ambiguous --under; 3 missing .act/ or
//     --under not found; 1 unexpected internal error.
func RunReady(repoRoot string, opts ReadyOptions) (output any, exitCode int) {
	paths := config.Layout(repoRoot)

	// Step 1: require .act/.
	if _, err := os.Stat(paths.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ReadyErrorOutput{
				Error:   "no_repo",
				Message: fmt.Sprintf("act ready: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return ReadyErrorOutput{
			Error:   "no_repo",
			Message: fmt.Sprintf("act ready: stat %s: %v", paths.Root, err),
		}, 3
	}

	// Phase 2 ticket 5: read-path cache check. Fetch+rebase if FETCH_HEAD
	// is stale or a bypass is set; no-op silently when there's no remote
	// or no nested .git yet. Errors here are non-fatal — the underlying
	// command falls through to read whatever state is currently on disk
	// so a transient network failure doesn't break read-only commands.
	_, _ = MaybeRefresh(repoRoot, MaybeRefreshOptions{Fresh: opts.Fresh})

	// Step 2: open + rebuild the index.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return ReadyErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return ReadyErrorOutput{
			Error:   "index_rebuild_failed",
			Message: err.Error(),
		}, 1
	}

	// Pull every non-tombstoned row; we filter by status + blockers below.
	rows, err := idx.ListAll(index.Filter{})
	if err != nil {
		return ReadyErrorOutput{
			Error:   "index_query_failed",
			Message: err.Error(),
		}, 1
	}

	// Build a quick id → row map for blocker lookups and parent chain
	// traversal. Tombstoned rows are already excluded by ListAll, but we
	// still treat unknown parents as "closed" (they cannot block).
	byID := make(map[string]index.Row, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}

	// Two distinct predicates:
	//
	//   isReadyCandidate — does this issue belong in the ready set at all?
	//   Per spec-v2.md §act ready, only status=="open" issues are candidates.
	//   in_progress means someone has claimed it; surfacing it would tee up
	//   losing claim races. blocked means the assignee says it's stuck;
	//   surfacing it pretends otherwise.
	//
	//   isActive — does this issue, when seen as a dep parent, currently
	//   block its child? Anything not closed (open, in_progress, blocked)
	//   counts; only a closed parent unblocks the child.
	//
	// These were conflated in v0.1.0 (act-d79b), which let in_progress and
	// blocked issues appear in act ready output.
	isReadyCandidate := func(r index.Row) bool {
		return r.Status == "open"
	}
	isActive := func(r index.Row) bool {
		return r.Status != "closed"
	}

	// Compute the ready set.
	ready := make([]index.Row, 0, len(rows))
	for _, r := range rows {
		if !isReadyCandidate(r) {
			continue
		}
		// Any unresolved external dep excludes the issue. External refs are
		// opaque to act; the orchestrator removes them via `act update
		// --ext-rm` when the upstream work is done. Until then the issue is
		// considered blocked. No override flag mirrors the internal-blocks
		// surface because none exists today for internal blocks either.
		if len(r.ExternalDeps) > 0 {
			continue
		}
		blocked := false
		for _, dep := range r.Deps {
			if dep.EdgeType != "blocks" {
				continue
			}
			parent, ok := byID[dep.Parent]
			if !ok {
				// Unknown parent (e.g. tombstoned) cannot block.
				continue
			}
			if isActive(parent) {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, r)
		}
	}

	// Step 4: --under filter.
	if opts.Under != "" {
		allIDs := make([]string, 0, len(rows))
		for _, r := range rows {
			allIDs = append(allIDs, r.ID)
		}
		full, ambiguous, found := ids.ResolvePrefix(allIDs, opts.Under)
		if ambiguous {
			candidates := ambiguousCandidates(allIDs, opts.Under)
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return ReadyErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act ready: --under %q matches %d issues", opts.Under, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.Under,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		if !found {
			return ReadyErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act ready: --under %q: no matching issue", opts.Under),
				Details: map[string]any{"query": opts.Under},
			}, 3
		}
		descendants := descendantSet(full, byID)
		filtered := ready[:0]
		for _, r := range ready {
			if descendants[r.ID] {
				filtered = append(filtered, r)
			}
		}
		ready = filtered
	}

	// Step 4b: --mine / --as filter. Restricts the ready set to issues
	// whose assignee exactly matches AssigneeFilter. Empty filter means
	// no restriction (the v0.1 default). Composes with --under.
	if opts.AssigneeFilter != "" {
		filtered := ready[:0]
		for _, r := range ready {
			if r.Assignee == opts.AssigneeFilter {
				filtered = append(filtered, r)
			}
		}
		ready = filtered
	}

	// Step 5: sort by priority asc, created_at desc, id asc.
	sort.SliceStable(ready, func(i, j int) bool {
		a, b := ready[i], ready[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID < b.ID
	})

	// Step 6: apply limit.
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultReadyLimit
	}
	if len(ready) > limit {
		ready = ready[:limit]
	}

	// Materialise output rows with shortest-unique-prefix ids. The prefix
	// table is computed over the FULL id universe so prefixes remain
	// stable across invocations regardless of filtering.
	allIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		allIDs = append(allIDs, r.ID)
	}
	prefixes := ids.ShortestUniquePrefixes(allIDs)

	out := ReadyResult{Ready: make([]ReadyIssue, 0, len(ready))}
	for _, r := range ready {
		short := prefixes[r.ID]
		if short == "" {
			short = r.ID
		}
		out.Ready = append(out.Ready, ReadyIssue{
			ID:        r.ID,
			ShortID:   short,
			Title:     r.Title,
			Priority:  r.Priority,
			Status:    r.Status,
			Assignee:  r.Assignee,
			ClaimedAt: r.ClaimedAt,
		})
	}
	out.Count = len(out.Ready)
	return out, 0
}

// readyEmptyCell is the placeholder for an empty assignee / claimed_at cell
// in human output. Picked as a single dash so columns stay visually aligned
// and an unassigned ready row reads as "no assignee" rather than as a typo.
const readyEmptyCell = "-"

// FormatReadyHuman renders a ReadyResult as one line per issue:
//
//	<short> <prio> <assignee> <claimed_at> <title>
//
// followed by a trailing newline per row. Empty result emits no output.
// Assignee is truncated to the first 4 hex chars (matching the act-XXXX
// short-id convention) when it looks like a hex node id; claimed_at is
// rendered as a relative timestamp ("3m ago", "2h ago"). Unclaimed rows
// render `-` in both columns so column alignment is preserved.
func FormatReadyHuman(res ReadyResult) string {
	return formatReadyHumanAt(res, time.Now())
}

// formatReadyHumanAt is the deterministic helper FormatReadyHuman uses, with
// `now` injected so tests can assert on stable relative-time output.
func formatReadyHumanAt(res ReadyResult, now time.Time) string {
	var b strings.Builder
	for _, r := range res.Ready {
		assignee := readyEmptyCell
		if r.Assignee != "" {
			assignee = shortenAssignee(r.Assignee)
		}
		claimed := readyEmptyCell
		if r.ClaimedAt != "" {
			claimed = relativeAge(r.ClaimedAt, now)
		}
		fmt.Fprintf(&b, "%s %d %s %s %s\n", r.ShortID, r.Priority, assignee, claimed, r.Title)
	}
	return b.String()
}

// shortenAssignee truncates a node id to its first 4 hex chars when the
// value looks like a hex string (matching the act-XXXX short-id convention).
// Non-hex assignees (e.g. human handles, "agent-x") are passed through
// unchanged so the column stays readable.
func shortenAssignee(s string) string {
	if len(s) <= 4 {
		return s
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return s
		}
	}
	return s[:4]
}

// relativeAge renders an RFC3339Millis timestamp (as produced by the fold
// layer) as a coarse "Nu ago" string. Unparseable input or future-dated
// stamps yield the raw value so we never silently lie about provenance.
func relativeAge(ts string, now time.Time) string {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", ts)
	if err != nil {
		return ts
	}
	d := now.Sub(t)
	if d < 0 {
		// Future-dated (clock skew, test fixtures): fall back to the
		// raw stamp rather than emit a misleading "0s ago".
		return ts
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// descendantSet returns the set of ids reachable from root via the parent
// chain — i.e. every issue whose parent (transitively) is root. The root
// itself is NOT included; --under filters to the descendants of the
// supplied issue.
func descendantSet(root string, byID map[string]index.Row) map[string]bool {
	// Build a reverse adjacency: parent → []child.
	children := make(map[string][]string, len(byID))
	for _, r := range byID {
		if r.Parent != "" {
			children[r.Parent] = append(children[r.Parent], r.ID)
		}
	}
	out := make(map[string]bool)
	stack := []string{root}
	for len(stack) > 0 {
		n := len(stack) - 1
		cur := stack[n]
		stack = stack[:n]
		for _, c := range children[cur] {
			if out[c] {
				continue
			}
			out[c] = true
			stack = append(stack, c)
		}
	}
	return out
}
