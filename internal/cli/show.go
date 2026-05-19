package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// Full, when true, disables the human-mode truncation guard on
	// description and closed_reason so the full text renders verbatim
	// (act-3c89). Independent of AsJSON: --json always returns full data
	// regardless of this flag.
	Full bool
	// Fresh, when true, forces the read-path cache layer to fetch+rebase
	// before reading state, regardless of FETCH_HEAD freshness (Phase 2
	// ticket 5). Not surfaced as a CLI flag on `act show` in this phase;
	// callers wire it programmatically or rely on ACT_DISPATCH_MODE.
	Fresh bool
}

// ShowResult is the success-shape returned by RunShow on a live (non-
// tombstoned) issue. Fields is the rendered state map produced by
// fold.RenderState plus `id` and `short_id`. When IncludeOps is set, Ops
// carries the HLC-sorted op envelopes.
type ShowResult struct {
	// Fields is the public-facing rendered state (after accept-removal
	// filtering). It always contains at least `id` and
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
	// Full, when true, signals FormatShowHuman to skip the truncation
	// guard on description and closed_reason and render them verbatim
	// (act-3c89). Plumbed in from ShowOptions.Full.
	Full bool
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

	// Phase 2 ticket 5: read-path cache check (no flag wiring on show
	// yet; bypass via ACT_DISPATCH_MODE=1 or the option is supported).
	_, _ = MaybeRefresh(repoRoot, MaybeRefreshOptions{Fresh: opts.Fresh})

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

	res := ShowResult{Fields: rendered, IncludeOps: opts.IncludeOps, Full: opts.Full}

	// Step 6a: list work commits attributed to this issue via the
	// `(act-XXXX)` marker. Read-side, no caching, single git invocation;
	// best-effort — git failures here degrade to "no commits surfaced"
	// rather than failing the whole show (act-9c8c).
	//
	// markerHex is the hex tail of ShortIssueID(full) — the canonical
	// commit-marker form. For new ids (MinShortHexLen=6) this is 6 hex; for
	// historical 4-hex ids it's the full 4-hex tail.
	if markerHex := commitMarkerHex(full); len(markerHex) >= 4 {
		g := gitops.NewHostGitOps(repoRoot)
		if commits, gerr := g.WorkCommitsForIssue(markerHex, showCommitsLimit); gerr == nil {
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
	// commit_marker is the canonical `Act-Id: act-XXXXXX` trailer an agent
	// should append (in the commit body, after a blank line) to their
	// work-commit message. Render it next to id so it's readable at a
	// glance in act show. Skip on tombstoned issues (handled by
	// ShowTombstoned branch above). Pre-act-c4c5 this was `(act-XXXX)`
	// subject-line form; the trailer form is now the only emission shape
	// (docs/coordination-plane-design.md v2.1 "Marker placement").
	if id, ok := f["id"].(string); ok && id != "" {
		fmt.Fprintf(&b, "commit_marker: %s\n", WorkCommitMarker(id))
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
	// chars; use --json for the full text, or pass --full to disable
	// truncation in the human format (act-3c89).
	if desc, ok := f["description"].(string); ok && desc != "" {
		if res.Full {
			fmt.Fprintf(&b, "description: %s\n", desc)
		} else {
			fmt.Fprintf(&b, "description: %s\n", truncateForShow(desc))
		}
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
				fmt.Fprintf(&b, "dep: %s %s\n", depShowLabel(e["edge_type"]), e["parent"])
			}
		case []any:
			for _, e := range d {
				if m, ok := e.(map[string]any); ok {
					et, _ := m["edge_type"].(string)
					pa, _ := m["parent"].(string)
					fmt.Fprintf(&b, "dep: %s %s\n", depShowLabel(et), pa)
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
	// closed_reason: render in human mode with the same truncation guard
	// as description so long close-reason text doesn't dominate show
	// output. Pass --full (act-3c89) or use --json for the verbatim text.
	if reason, ok := f["closed_reason"].(string); ok && reason != "" {
		if res.Full {
			fmt.Fprintf(&b, "closed_reason: %s\n", reason)
		} else {
			fmt.Fprintf(&b, "closed_reason: %s\n", truncateForShow(reason))
		}
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
	// ops: rendered when --include-ops is set (text mirror of the JSON
	// `ops` key). Format matches `act log`'s one-line-per-op shape so the
	// two surfaces are visually consistent: timestamp, op_type, short hash,
	// summary. The section header is emitted whenever the flag was set,
	// even on zero ops, so behavior matches the JSON path (which always
	// emits an `ops` key when IncludeOps is true). For a live issue Ops is
	// never empty — at minimum a `create` op exists — but the contract
	// stays honest. act-b891.
	if res.IncludeOps {
		fmt.Fprintf(&b, "ops:\n")
		for _, env := range res.Ops {
			hash, err := env.Hash()
			if err != nil {
				hash = "????????"
			}
			wall := time.UnixMilli(env.HLC.Wall).UTC().Format(rfc3339Millis)
			summary := opSummary(env)
			if summary == "" {
				fmt.Fprintf(&b, "  %s %s %s\n", wall, env.OpType, hash)
			} else {
				fmt.Fprintf(&b, "  %s %s %s  %s\n", wall, env.OpType, hash, summary)
			}
		}
	}
	return b.String()
}

// opSummary returns a short, human-readable summary fragment for an op
// envelope — e.g. the field name on an update_field op, the assignee on a
// claim op. Empty string when there's nothing useful beyond op_type. The
// payload is best-effort decoded; malformed payloads collapse to "" rather
// than failing the render. Used by formatShowFields when --include-ops is
// set so each op line carries one extra hint about what changed without
// requiring the reader to drop to --json.
func opSummary(env op.Envelope) string {
	if len(env.Payload) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(env.Payload, &m); err != nil {
		return ""
	}
	switch env.OpType {
	case "create":
		if t, ok := m["title"].(string); ok && t != "" {
			return t
		}
	case "update_field":
		if f, ok := m["field"].(string); ok && f != "" {
			return f
		}
	case "claim":
		if a, ok := m["assignee"].(string); ok && a != "" {
			return a
		}
	case "close":
		if r, ok := m["reason"].(string); ok && r != "" {
			return firstLine(r)
		}
	case "add_dep", "remove_dep":
		et, _ := m["edge_type"].(string)
		pa, _ := m["parent"].(string)
		switch {
		case et != "" && pa != "":
			return et + " " + pa
		case pa != "":
			return pa
		}
	case "add_external_dep", "remove_external_dep":
		if r, ok := m["ref"].(string); ok && r != "" {
			return r
		}
	case "add_accept", "remove_accept":
		if c, ok := m["criterion"].(string); ok && c != "" {
			return firstLine(c)
		}
	}
	return ""
}

// firstLine returns the first line of s, trimmed; useful for collapsing
// multi-line reasons or accept criteria into a single op-list line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
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

// commitMarkerHex returns the hex tail of the canonical commit marker for
// fullID — i.e. the hex part of ShortIssueID(fullID). For ids at or above
// the generation floor (MinShortHexLen, 6 since act-f9a0) this is exactly
// MinShortHexLen hex chars; for historical ids that were minted shorter
// (e.g. 4-hex ids from before the bump), this returns the full hex tail
// verbatim. Returns "" for ids that don't start with "act-".
//
// This is the doctor-grep key (act commit-marker invariants).
func commitMarkerHex(fullID string) string {
	const prefix = "act-"
	short := ShortIssueID(fullID)
	if !strings.HasPrefix(short, prefix) {
		return ""
	}
	return short[len(prefix):]
}

// depShowLabel translates an edge_type into the label used in the
// human-mode `dep:` line on `act show`. The semantic direction is
// (child, parent) — i.e. the issue being shown holds the dep entry,
// and `parent` is the target on the other side of the edge.
//
// For edge_type=blocks, the child is BLOCKED BY the parent, so the
// natural-English label is `blocked-by`. The pre-act-982a label
// (the raw edge_type) read as "A blocks <parent>" and caused agents
// to misread the direction. For other edge types ("relates",
// "supersedes") the type name itself is the right label.
func depShowLabel(edgeType string) string {
	if edgeType == "blocks" {
		return "blocked-by"
	}
	return edgeType
}
