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
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

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
//   - exitCode: 0 success; 3 missing .act/, unknown id, or ambiguous prefix.
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
		return ShowErrorOutput{
			Error:   "id_ambiguous",
			Message: fmt.Sprintf("act show: prefix %q matches %d issues", opts.ID, len(candidates)),
			Details: map[string]any{
				"prefix":     opts.ID,
				"candidates": candidates,
			},
			Candidates: candidates,
		}, 3
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

// formatShowFields renders the rendered-state map as a multi-line key/value
// summary. Field ordering is fixed so test assertions are deterministic.
func formatShowFields(res ShowResult) string {
	f := res.Fields
	var b strings.Builder
	if id, ok := f["id"].(string); ok {
		fmt.Fprintf(&b, "id: %s\n", id)
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
	if created, ok := f["created_at"].(string); ok {
		fmt.Fprintf(&b, "created_at: %s\n", created)
	}
	if closed, ok := f["closed_at"].(string); ok && closed != "" {
		fmt.Fprintf(&b, "closed_at: %s\n", closed)
	}
	if reason, ok := f["closed_reason"].(string); ok && reason != "" {
		fmt.Fprintf(&b, "closed_reason: %s\n", reason)
	}
	return b.String()
}

// ShowJSON returns the JSON-shape map used by main.go to marshal a successful
// ShowResult. The result is the rendered fields verbatim plus, when
// IncludeOps is true, an `ops` key carrying the HLC-sorted envelope slice.
func (r ShowResult) ShowJSON() map[string]any {
	out := make(map[string]any, len(r.Fields)+1)
	for k, v := range r.Fields {
		out[k] = v
	}
	if r.IncludeOps {
		out["ops"] = r.Ops
	}
	return out
}

// listIssueIDs and readIssueOps live in log.go (same package) and are
// reused here to share the on-disk walk implementation.
//
// The compile-time reference below documents the dependency without
// triggering a "declared but not used" warning.
var _ = filepath.Separator
