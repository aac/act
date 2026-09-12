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
	// Limit caps the result size. <=0 means "no limit" — the same
	// meaning ListOptions.Limit has, so `--limit 0` returns every ready
	// issue on both subcommands (act-1b816e).
	//
	// The 50-row default lives at the CLI flag (and at the other call
	// sites that want a bound), NOT here. It used to live here, which is
	// what made `act ready --limit 0` silently fall back to 50 while
	// `act list --limit 0` returned everything: two subcommands
	// disagreeing on the same flag, with no way at all to ask ready for
	// a complete answer.
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
	// AllMachines, when true, turns OFF the host filter and returns issues
	// pinned to other machines alongside local ones. Wired by
	// `act ready --all-machines` / `act next --all-machines`.
	//
	// The filter is ON by default, and that is the load-bearing choice
	// (act-2c7be3). /orchestrate is tracker-agnostic: it shells out to
	// `act ready` with no act-specific flags, so default-on fixes it —
	// and every future caller — with no change to the command, the
	// skill, or anything else. Opt-in would have fixed only the callers
	// someone remembered to update, which is the failure this exists to
	// end: on 2026-09-11 eleven consecutive /orchestrate captains were
	// launched into a queue whose every ready row needed the other
	// machine.
	AllMachines bool
	// MachineOverride, when non-empty, answers "ready for THIS label"
	// instead of for this machine. Set from ACT_MACHINE by ResolveMachine in
	// normal use; exposed as a field so tests can pin a label without
	// mutating process environment.
	MachineOverride string
	// NoFetch, when true, makes this a genuinely non-mutating read: the
	// cache layer skips the fetch+rebase entirely and the command
	// answers from on-disk state (act-3803ac). Wired by `--no-fetch`;
	// ACT_NO_FETCH=1 has the same effect process-wide. Mutually
	// exclusive with Fresh — the CLI rejects both together.
	NoFetch bool
}

// ReadyIssue is one row of the ready set.
//
// CreatedAt and ClaimedAt are carried so a caller can judge AGE without a
// follow-up `act show` per row (act-d627c8): an aggregator sweeping many
// stores otherwise pays one subprocess — and one cache-cold fetch — per
// id, keyed on exactly the projects with the most stale work. Both are
// omitempty: a ready row is by definition open and unclaimed, so
// claimed_at is normally absent, and the field earns its place on the
// --mine/--as views and on any future ready set that admits claimed work.
type ReadyIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"short_id,omitempty"`
	Title     string `json:"title"`
	Priority  int    `json:"priority"`
	Status    string `json:"status"`
	Assignee  string `json:"assignee,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	ClaimedAt string `json:"claimed_at,omitempty"`
	// Machine is the machine this issue is pinned to, or "" for the
	// default "runs anywhere". Present so a caller reading
	// --all-machines output can tell the rows apart without an
	// `act show` per id.
	Machine string `json:"machine,omitempty"`
}

// ReadyResult is the JSON-serialisable success envelope. The shape is
// `{"ready": [...], "count": N, "total": N, "truncated": bool}`, matching
// ListResult field-for-field (act-1b816e).
//
// Count is how many rows were RETURNED; Total is how many were ready
// before Limit was applied. Truncated states the difference outright and
// carries no `omitempty` for the same reason ListResult.Truncated doesn't:
// an omitted key reads as null and puts the consumer back to inferring
// truncation from `count == limit`, which is wrong exactly when the ready
// count equals the limit. Before this, `act ready --json` carried only
// {ready, count} — an aggregator sweeping many stores could not tell a
// project with exactly 50 ready issues from one with 500, and silently
// under-reported with exit 0.
type ReadyResult struct {
	Ready []ReadyIssue `json:"ready"`
	Count int          `json:"count"`
	// Total is the pre-limit ready count. Equal to Count when the result
	// was not capped.
	Total int `json:"total"`
	// Truncated reports whether Limit dropped ready issues.
	Truncated bool `json:"truncated"`
	// Machine carries everything a consumer needs to understand the
	// machine filter: who this machine is, whether the filter ran, and
	// how many rows need another machine (act-2c7be3).
	//
	// It is ONE always-emitted nested object rather than flat siblings,
	// for two reasons. Feature detection is `"machine" in doc` — a
	// genuinely absent key on a binary that predates this, not a promise
	// that nobody ever adds `omitempty` to an int. And the facts have to
	// be present or absent TOGETHER: a consumer that learned the count
	// but not whether the filter ran would read a confident `0` from a
	// caller that passed --all-machines.
	Machine MachineFilter `json:"machine"`
	// Refresh reports what the read-path cache layer did before this
	// answer was produced — served from cache, freshly fetched, skipped
	// under --no-fetch, or failed. Omitted when there is nothing to say
	// (no .act/ at all). act-3803ac.
	Refresh *RefreshInfo `json:"refresh,omitempty"`
}

// MachineFilter is the `machine` object on a ReadyResult.
//
// Filtered is the bit that stops a zero from lying. PinnedElsewhere is an
// honest count of rows needing another machine EITHER WAY: when the filter
// ran it counts what was dropped, and when it did not it counts what was
// let through. So "7 rows here need another machine" is answerable without
// knowing which mode produced the answer — the rules/09
// differentiate-on-cause requirement applied to our own envelope.
//
// Filtered is false in two situations a consumer must tell apart, and
// Source is what tells them: --all-machines was passed (Source is
// "ACT_MACHINE" or "config"), or this machine was never named so act
// refused to filter on a guess (Source is "hostname"). See
// MachineInfo.Explicit for why the second fails open.
type MachineFilter struct {
	// Label is this machine's resolved label.
	Label string `json:"label"`
	// Source is one of MachineSourceEnv, MachineSourceConfig,
	// MachineSourceHostname.
	Source string `json:"source"`
	// Filtered reports whether pinned-elsewhere rows were actually
	// excluded from Ready.
	Filtered bool `json:"filtered"`
	// PinnedElsewhere counts ready rows pinned to a machine other than
	// Label — dropped when Filtered, still present in Ready when not.
	PinnedElsewhere int `json:"pinned_elsewhere"`
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

// DefaultReadyLimit matches spec §act ready: bound the result count at 50
// when --limit is not supplied. It is exported because the default now
// belongs to the CALLERS (the `act ready` flag default, act_next's
// frontier fetch) rather than to RunReady — see ReadyOptions.Limit for
// why (act-1b816e).
const DefaultReadyLimit = 50

// RunReady implements `act ready`.
//
// Algorithm (per spec §3 act ready):
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

	// Read-path cache check. Fetch+rebase if FETCH_HEAD is stale or a
	// bypass is set; no-op silently when there's no remote, no nested
	// .git, or --no-fetch/ACT_NO_FETCH=1 asked for a genuinely
	// non-mutating read (act-3803ac).
	//
	// The outcome is NOT discarded (it used to be): a failed refresh is
	// non-fatal — we fall through to on-disk state so a transient network
	// failure doesn't break a read — but it is REPORTED, via the
	// `refresh` key and a stderr warning, so served-from-cache and
	// could-not-refresh stop looking identical.
	refreshRes, refreshErr := MaybeRefresh(repoRoot, MaybeRefreshOptions{Fresh: opts.Fresh, NoFetch: opts.NoFetch})
	refresh := NewRefreshInfo(refreshRes, refreshErr)

	// Step 2: open + rebuild the index.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return ReadyErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	defer func() { _ = idx.Close() }()
	if _, err := idx.EnsureCurrent(paths.Ops); err != nil {
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
	//   Per spec.md §act ready, only status=="open" issues are candidates.
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

	// Step 4c: the machine filter. Runs LAST of the filters so the
	// pinned_elsewhere count describes exactly the set the caller asked
	// about — "of the rows matching your --under/--mine, N need another
	// machine" — rather than a number about issues the caller never
	// asked to see.
	//
	// An issue with no machine runs anywhere and is never dropped: the
	// field only ever SUBTRACTS from where work can run, so adding it
	// cannot hide anything that was visible before someone pinned it.
	me := ResolveMachine()
	if opts.MachineOverride != "" {
		me = MachineInfo{Label: opts.MachineOverride, Source: MachineSourceEnv, Path: me.Path}
	}
	// Fail open: filter ONLY when this machine was actually named. An
	// unconfigured machine behaves exactly as act did before this field
	// existed. See MachineInfo.Explicit for the failure that prevents.
	doFilter := !opts.AllMachines && me.Explicit()
	pinnedElsewhere := 0
	kept := ready[:0]
	for _, r := range ready {
		if MachineMatches(r.Machine, me.Label) {
			kept = append(kept, r)
			continue
		}
		pinnedElsewhere++
		if !doFilter {
			// Counted, but let through: either the caller asked for
			// everything, or act has no business filtering on a guess.
			kept = append(kept, r)
		}
	}
	ready = kept

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

	// Step 6: apply limit. `total` is captured BEFORE the cap so the
	// result can report what the caller did not see (act-1b816e), the
	// same way RunList does.
	total := len(ready)
	truncated := false
	if opts.Limit > 0 && len(ready) > opts.Limit {
		ready = ready[:opts.Limit]
		truncated = true
	}

	// Materialise output rows with shortest-unique-prefix ids. The prefix
	// table is computed over the FULL id universe so prefixes remain
	// stable across invocations regardless of filtering.
	allIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		allIDs = append(allIDs, r.ID)
	}
	prefixes := ids.ShortestUniquePrefixes(allIDs)

	out := ReadyResult{Ready: make([]ReadyIssue, 0, len(ready)), Refresh: refresh}
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
			CreatedAt: r.CreatedAt,
			ClaimedAt: r.ClaimedAt,
			Machine:   r.Machine,
		})
	}
	out.Count = len(out.Ready)
	out.Total = total
	out.Truncated = truncated
	out.Machine = MachineFilter{
		Label:           me.Label,
		Source:          me.Source,
		Filtered:        doFilter,
		PinnedElsewhere: pinnedElsewhere,
	}
	return out, 0
}

// FormatReadyMachineNotice returns the stderr notice about issues pinned
// to another machine, or "" when there is nothing to say. cmd is the
// subcommand printing it ("act ready", "act next"), because both print
// this and a notice that says "act ready:" during an `act next` sends the
// reader to the wrong command.
//
// Two different notices, one function, because they are two different
// facts a reader must tell apart (rules/09):
//
//   - Rows WERE excluded. Say how many, and NAME THE LABEL AND ITS SOURCE.
//     The source is not decoration: it is the difference between "of
//     course, that is laptop work" and "why does this machine think it is
//     called andrews-mbp", and only one of those needs action.
//   - Rows were NOT excluded because this machine was never named. This is
//     the fail-open path, and staying silent here would be the worst of
//     both worlds — the pins would look like they were being honoured
//     while every one of them was being ignored.
//
// It goes to stderr in both human and --json mode, matching the
// truncation and refresh notices: `act ready --json | jq` swallows
// stdout, and anything added to the row stream corrupts the parse.
func FormatReadyMachineNotice(cmd string, res ReadyResult) string {
	m := res.Machine
	if m.PinnedElsewhere <= 0 {
		return ""
	}
	noun := "ready issues are"
	verb := "were"
	if m.PinnedElsewhere == 1 {
		noun = "ready issue is"
		verb = "was"
	}
	info := MachineInfo{Label: m.Label, Source: m.Source, Path: MachineConfigPath()}
	if !m.Filtered && m.Source == MachineSourceHostname {
		return fmt.Sprintf(
			"%s: %d %s pinned to a machine, and NOTHING %s excluded — this machine has no label.\n"+
				"  guessed: %s\n"+
				"  name it so pins take effect: act machine --set <label>\n",
			cmd, m.PinnedElsewhere, noun, verb, info.Describe(),
		)
	}
	if !m.Filtered {
		return fmt.Sprintf(
			"%s: %d %s pinned to another machine and shown anyway (--all-machines).\n"+
				"  this machine: %s\n",
			cmd, m.PinnedElsewhere, noun, info.Describe(),
		)
	}
	return fmt.Sprintf(
		"%s: %d %s pinned to another machine and %s excluded.\n"+
			"  this machine: %s\n"+
			"  see them with: act ready --all-machines\n",
		cmd, m.PinnedElsewhere, noun, verb, info.Describe(),
	)
}

// FormatReadyTruncationNotice returns the one-line warning that must
// accompany a capped ready set, or "" when nothing was dropped
// (act-1b816e). It is the `act ready` twin of
// FormatListTruncationNotice, and the stderr placement is load-bearing
// for the same reason: `act ready --json | jq '.count'` swallows stdout,
// so a stdout trailer would never reach the human, and anything added to
// the row stream corrupts the parse.
func FormatReadyTruncationNotice(res ReadyResult) string {
	if !res.Truncated {
		return ""
	}
	return fmt.Sprintf(
		"act ready: WARNING: showing %d of %d ready issues — %d hidden by --limit. "+
			"Counts taken from this output will be WRONG. "+
			"Use `act ready --limit 0` for everything, or narrow with --under/--mine.\n",
		res.Count, res.Total, res.Total-res.Count,
	)
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
		// A pinned row wears its label between the claimed-at column and
		// the title. It appears exactly when a pinned row survives into
		// the listing at all: under --all-machines, or on a machine with
		// no explicit label, where act fails open and filters nothing.
		// On a labelled machine the default view has no pinned rows left
		// to mark, so the format there is byte-identical to what it has
		// always been — which matters, because things parse it
		// positionally.
		title := r.Title
		if r.Machine != "" {
			title = "@" + r.Machine + " " + title
		}
		fmt.Fprintf(&b, "%s %d %s %s %s\n", r.ShortID, r.Priority, assignee, claimed, title)
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
