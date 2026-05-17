package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// showCommitsLimit caps the number of work commits surfaced inline.
// Typical issues land in 1–3 commits; a generous-but-not-noisy ceiling
// keeps `act show` readable for outliers (review tasks, long refactors).
const showCommitsLimit = 10

// ShowOptions captures the flags accepted by `act show <id>`.
type ShowOptions struct {
	// ID is the user-supplied id or prefix to resolve against the repo's
	// known issue ids.
	ID string
	// AsJSON shapes the output rendering. The function returns the same
	// data shape regardless; main.go decides how to render.
	AsJSON bool
	// IncludeOps, when true, includes the HLC-sorted op stream alongside
	// the rendered snapshot under the `ops` key.
	IncludeOps bool
}

// ShowResult is the success-shape returned by RunShow on a live (non-
// tombstoned) issue. Fields is the rendered state map produced by
// fold.RenderState plus `id` and `short_id`. When IncludeOps is set, Ops
// carries the HLC-sorted op envelopes.
type ShowResult struct {
	// Fields is the public-facing rendered state (after redaction filtering
	// and accept-removal filtering). It always contains at least `id` and
	// `short_id`. JSON serialisation is handled by main.go which marshals
	// this map directly.
	Fields map[string]any
	// Ops, when non-nil, is the HLC-sorted op stream for the issue.
	Ops []op.Envelope
	// IncludeOps records whether Ops was populated; consumers that
	// flatten Fields into JSON consult this rather than len(Ops) so the
	// presence of the `ops` key matches the flag exactly (an issue with
	// zero ops never happens for a live id, but the contract is honest).
	IncludeOps bool
	// Commits is the list of work commits whose subject contains this
	// issue's `(act-XXXX)` marker, most-recent-first, capped at
	// showCommitsLimit. Empty (not nil) when the issue has no work commits
	// or when the show was run outside a git working tree (act-9c8c).
	Commits []gitops.WorkCommit
}

// ShowTombstoned is the success-shape returned by RunShow when the resolved
// id is tombstoned. The JSON output is `{"id":"...","tombstoned":true}`;
// the human renderer prints "[deleted]".
type ShowTombstoned struct {
	ID         string `json:"id"`
	Tombstoned bool   `json:"tombstoned"`
}

// ShowErrorOutput is the structured shape returned to the caller when show
// refuses. Candidates is non-nil only on the id_ambiguous path; it is also
// mirrored under Details["candidates"] so the on-the-wire JSON envelope
// matches spec §"Errors" (`details.candidates[]`).
type ShowErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// RunShow implements `act show <id>`. It resolves the user-supplied id
// against the set of all known full ids (read from `.act/ops/` subdirs),
// folds the issue's op stream through fold.ApplyDispatch, renders the
// terminal state via fold.RenderState, and returns a ShowResult.
//
// Returns:
//   - output: ShowResult on success, ShowTombstoned for a tombstoned issue,
//     ShowErrorOutput on failure.
//   - exitCode: 0 success; 2 ambiguous prefix (usage); 3 missing .act/ or
//     unknown id.
func RunShow(repoRoot string, opts ShowOptions) (output any, exitCode int) {
	paths := config.Layout(repoRoot)

	// Step 1: require .act/.
	if _, err := os.Stat(paths.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ShowErrorOutput{
				Error:   "not_in_git",
				Message: fmt.Sprintf("act show: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return ShowErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act show: stat %s: %v", paths.Root, err),
		}, 3
	}

	// Step 2: resolve id against known full ids on disk.
	allIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return ShowErrorOutput{
			Error:   "ops_walk_failed",
			Message: err.Error(),
		}, 3
	}

	full, ambiguous, found := ids.ResolvePrefix(allIDs, opts.ID)
	if ambiguous {
		candidates := ambiguousCandidates(allIDs, opts.ID)
		// Exit 2 (usage): per spec universal-exit-code table, ambiguous
		// prefix is a non-unique caller argument. See resolve_helpers.go.
		return ShowErrorOutput{
			Error:   "id_ambiguous",
			Message: fmt.Sprintf("act show: prefix %q matches %d issues", opts.ID, len(candidates)),
			Details: map[string]any{
				"prefix":     opts.ID,
				"candidates": candidates,
			},
			Candidates: candidates,
		}, 2
	}
	if !found {
		return ShowErrorOutput{
			Error:   "issue_not_found",
			Message: fmt.Sprintf("act show: no issue matches %q", opts.ID),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 3: fold the issue's op stream.
	state, err := fold.FoldIssue(paths.Ops, full, fold.ApplyDispatch)
	if err != nil {
		return ShowErrorOutput{
			Error:   "fold_failed",
			Message: err.Error(),
		}, 3
	}

	// Step 4: tombstoned short-circuit.
	if state != nil && state.Tombstoned {
		return ShowTombstoned{ID: full, Tombstoned: true}, 0
	}

	// Step 5: render the terminal state.
	rendered := fold.RenderState(state)
	if rendered == nil {
		// Defensive: RenderState returns nil only for nil/tombstoned state;
		// the tombstone path is handled above. Treat anything else as a
		// missing issue.
		rendered = map[string]any{}
	}

	// Step 6: enrich with id + short_id (the spec emits the full id only,
	// but short_id mirrors `act list` and aids stable test assertions).
	rendered["id"] = full
	prefixes := ids.ShortestUniquePrefixes(allIDs)
	if short := prefixes[full]; short != "" {
		rendered["short_id"] = short
	} else {
		rendered["short_id"] = full
	}

	res := ShowResult{Fields: rendered, IncludeOps: opts.IncludeOps}

	// Step 6a: list work commits attributed to this issue via the
	// `(act-XXXX)` marker. Read-side, no caching, single git invocation;
	// best-effort — git failures here degrade to "no commits surfaced"
	// rather than failing the whole show (act-9c8c).
	if prefix4 := first4Hex(full); prefix4 != "" {
		g := gitops.NewGitOps(repoRoot)
		if commits, gerr := g.WorkCommitsForIssue(prefix4, showCommitsLimit); gerr == nil {
			res.Commits = commits
		}
	}

	// Step 7: optionally inline the op stream, sorted by HLC.
	if opts.IncludeOps {
		envs, err := readIssueOps(paths.Ops, full)
		if err != nil {
			return ShowErrorOutput{
				Error:   "ops_read_failed",
				Message: err.Error(),
			}, 3
		}
		sortLogOps(envs)
		res.Ops = envelopesOnly(envs)
	}

	return res, 0
}

// FormatShowHuman renders a ShowResult as a multi-line summary suitable for
// non-JSON output. Tombstoned issues print "[deleted]". The line set is
// stable: id, title, status, assignee, priority, type, parent, accept,
// deps, created_at, closed_at.
func FormatShowHuman(res any) string {
	switch v := res.(type) {
	case ShowTombstoned:
		return fmt.Sprintf("%s [deleted]\n", v.ID)
	case ShowResult:
		return formatShowFields(v)
	}
	return ""
}

// truncateForShow renders a description for human-mode show output. Caps
// at 400 chars or 5 lines (whichever hits first), appending an explicit
// '… (truncated; see --json)' marker so the reader knows there's more.
// Single-line short descriptions pass through unchanged.
func truncateForShow(desc string) string {
	const maxChars = 400
	const maxLines = 5
	const marker = "… (truncated; see --json for full text)"

	// Single-line, short → pass through.
	if !strings.Contains(desc, "\n") && len(desc) <= maxChars {
		return desc
	}
	// Multi-line: indent continuation lines so the block is visibly part
	// of the description value, not a sibling field.
	lines := strings.Split(desc, "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}
	body := strings.Join(lines, "\n  ")
	if len(body) > maxChars {
		body = body[:maxChars]
		truncated = true
	}
	if truncated {
		body = body + "\n  " + marker
	}
	return body
}

// formatShowFields renders the rendered-state map as a multi-line key/value
// summary. Field ordering is fixed so test assertions are deterministic.
func formatShowFields(res ShowResult) string {
	f := res.Fields
	var b strings.Builder
	if id, ok := f["id"].(string); ok {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	// commit_marker is the canonical (act-XXXX) string an agent should
	// embed in their work-commit message. Render it next to id so it's
	// readable at a glance in act show. Skip on tombstoned issues
	// (handled by ShowTombstoned branch above).
	if id, ok := f["id"].(string); ok && id != "" {
		fmt.Fprintf(&b, "commit_marker: (%s)\n", ShortIssueID(id))
	}
	if title, ok := f["title"].(string); ok {
		fmt.Fprintf(&b, "title: %s\n", title)
	}
	if status, ok := f["status"].(string); ok {
		fmt.Fprintf(&b, "status: %s\n", status)
	}
	if assignee, ok := f["assignee"].(string); ok && assignee != "" {
		fmt.Fprintf(&b, "assignee: %s\n", assignee)
	}
	if priority, ok := f["priority"]; ok {
		fmt.Fprintf(&b, "priority: %v\n", priority)
	}
	if typ, ok := f["type"].(string); ok {
		fmt.Fprintf(&b, "type: %s\n", typ)
	}
	if parent, ok := f["parent"].(string); ok && parent != "" {
		fmt.Fprintf(&b, "parent: %s\n", parent)
	}
	// description: render in human mode with truncation guard (was
	// hidden in v0.1 — agents reached for show --json | jq for routine
	// reads; act-10f7). Long descriptions truncate at 5 lines or 400
	// chars; use --json for the full text.
	if desc, ok := f["description"].(string); ok && desc != "" {
		fmt.Fprintf(&b, "description: %s\n", truncateForShow(desc))
	}
	if accept, ok := f["accept"]; ok {
		switch a := accept.(type) {
		case []string:
			for _, c := range a {
				fmt.Fprintf(&b, "accept: %s\n", c)
			}
		case []any:
			for _, c := range a {
				fmt.Fprintf(&b, "accept: %v\n", c)
			}
		}
	}
	if deps, ok := f["deps"]; ok {
		switch d := deps.(type) {
		case []map[string]string:
			for _, e := range d {
				fmt.Fprintf(&b, "dep: %s %s\n", e["edge_type"], e["parent"])
			}
		case []any:
			for _, e := range d {
				if m, ok := e.(map[string]any); ok {
					et, _ := m["edge_type"].(string)
					pa, _ := m["parent"].(string)
					fmt.Fprintf(&b, "dep: %s %s\n", et, pa)
				}
			}
		}
	}
	if refs, ok := f["external_deps"]; ok {
		switch xs := refs.(type) {
		case []string:
			for _, r := range xs {
				fmt.Fprintf(&b, "external_dep: %s\n", r)
			}
		case []any:
			for _, r := range xs {
				if s, ok := r.(string); ok {
					fmt.Fprintf(&b, "external_dep: %s\n", s)
				}
			}
		}
	}
	if created, ok := f["created_at"].(string); ok {
		fmt.Fprintf(&b, "created_at: %s\n", created)
	}
	if closed, ok := f["closed_at"].(string); ok && closed != "" {
		fmt.Fprintf(&b, "closed_at: %s\n", closed)
	}
	if reason, ok := f["closed_reason"].(string); ok && reason != "" {
		fmt.Fprintf(&b, "closed_reason: %s\n", reason)
	}
	if closer, ok := f["closed_by_node"].(string); ok && closer != "" {
		fmt.Fprintf(&b, "closed_by_node: %s\n", closer)
	}
	// Work commits attributed to this issue via the (act-XXXX) marker.
	// Section omitted entirely when there are zero matches so issues
	// closed without code (tracking issues, doc-only closes) don't carry
	// an empty header (act-9c8c AC #4).
	if len(res.Commits) > 0 {
		fmt.Fprintf(&b, "commits:\n")
		for _, c := range res.Commits {
			fmt.Fprintf(&b, "  %s %s  %s\n", shortSHA(c.SHA), c.AuthorDate, c.Subject)
		}
	}
	return b.String()
}

// shortSHA renders the first 7 hex chars of a 40-hex commit sha — the
// canonical short form used by `git log --oneline`.
func shortSHA(sha string) string {
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

// ShowJSON returns the JSON-shape map used by main.go to marshal a successful
// ShowResult. The result is the rendered fields verbatim plus, when
// IncludeOps is true, an `ops` key carrying the HLC-sorted envelope slice.
// `commits` is always present (empty slice when none) so MCP consumers can
// rely on the key existing (act-9c8c AC #4).
func (r ShowResult) ShowJSON() map[string]any {
	out := make(map[string]any, len(r.Fields)+2)
	for k, v := range r.Fields {
		out[k] = v
	}
	if r.IncludeOps {
		out["ops"] = r.Ops
	}
	if r.Commits == nil {
		out["commits"] = []gitops.WorkCommit{}
	} else {
		out["commits"] = r.Commits
	}
	return out
}

// listIssueIDs and readIssueOps live in log.go (same package) and are
// reused here to share the on-disk walk implementation.
//
// The compile-time reference below documents the dependency without
// triggering a "declared but not used" warning.
var _ = filepath.Separator

// first4Hex returns the 4 hex characters after the "act-" prefix. Returns
// "" for ids that don't start with "act-" or are too short. The 4-char
// prefix is the doctor-grep key (act commit-marker invariants).
func first4Hex(fullID string) string {
	const prefix = "act-"
	if !strings.HasPrefix(fullID, prefix) {
		return ""
	}
	rest := fullID[len(prefix):]
	if len(rest) < 4 {
		return ""
	}
	return rest[:4]
}
