package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

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
}

// ReadyIssue is one row of the ready set.
type ReadyIssue struct {
	ID       string `json:"id"`
	ShortID  string `json:"short_id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
	Assignee string `json:"assignee,omitempty"`
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
			ID:       r.ID,
			ShortID:  short,
			Title:    r.Title,
			Priority: r.Priority,
			Status:   r.Status,
			Assignee: r.Assignee,
		})
	}
	out.Count = len(out.Ready)
	return out, 0
}

// FormatReadyHuman renders a ReadyResult as one line per issue:
//
//	<short> <prio> <title>
//
// followed by a trailing newline per row. Empty result emits no output.
func FormatReadyHuman(res ReadyResult) string {
	var b strings.Builder
	for _, r := range res.Ready {
		fmt.Fprintf(&b, "%s %d %s\n", r.ShortID, r.Priority, r.Title)
	}
	return b.String()
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
