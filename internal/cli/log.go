package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// LogResult is the success-shape returned by RunLog. It is JSON-serialisable
// and is the same shape that the human renderer consumes.
//
// When the caller scopes the log to a single issue (positional id or
// --by-issue), ID is the resolved full id. When the caller asked for the
// full op stream across all issues (no id, no --by-issue), ID is the
// empty string.
type LogResult struct {
	ID  string        `json:"id"`
	Ops []op.Envelope `json:"ops"`
}

// LogOptions carries the filter knobs for RunLog. All fields are optional;
// the zero value reproduces the historical "scope by id only" behaviour
// when ByIssue is also empty (the caller must still supply at least one
// of: positional id via ByIssue, or one of the cross-issue filters).
//
// Filter semantics:
//   - Since: only ops with HLC.Wall >= now-Since are returned. Zero means
//     no time filter.
//   - ByIssue: full id or unique prefix; resolved against the on-disk
//     issue universe. Empty means "all issues".
//   - Types: op types to include. Empty means "all types". The strings
//     here are the user-facing names — friendly aliases (update, dep_add,
//     delete) are translated to spec names (update_field, add_dep,
//     tombstone) by normalizeOpTypeFilter before matching.
//   - Summary is a presentation knob, not a filter: when true the human
//     renderer emits one line per op (timestamp, op_type, 8-char hash,
//     summary) instead of the full envelope. The JSON shape is
//     unaffected — JSON callers see the same LogResult either way.
type LogOptions struct {
	Since   time.Duration
	ByIssue string
	Types   []string
	Summary bool
}

// LogErrorOutput is the structured shape returned to the caller when log
// refuses. Candidates is non-nil only on the id_ambiguous path; it is also
// mirrored under Details["candidates"] so the on-the-wire JSON envelope
// matches spec §"Errors" (`details.candidates[]`).
type LogErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// RunLog implements `act log [<id>] [--since D] [--by-issue ID] [--type T,T]`.
// It walks the on-disk op tree, parses envelopes, sorts globally by HLC
// then op hash, and returns the chronological op stream after applying
// any active filters.
//
// Scoping precedence:
//   - If idOrPrefix is non-empty, it is treated as the issue scope
//     (equivalent to --by-issue). Passing both a positional id and a
//     non-empty opts.ByIssue is a usage error.
//   - If only opts.ByIssue is set, the log is scoped to that issue.
//   - Otherwise the log spans every issue under .act/ops/ and the
//     LogResult.ID field is left empty.
//
// Returns:
//   - output: LogResult on success, LogErrorOutput on failure.
//   - exitCode: 0 success; 2 ambiguous prefix or conflicting scope
//     (usage); 3 missing .act/ or unknown id.
func RunLog(repoRoot, idOrPrefix string, asJSON bool) (output any, exitCode int) {
	return RunLogOpts(repoRoot, idOrPrefix, asJSON, LogOptions{})
}

// RunLogOpts is the options-bearing form of RunLog. Existing callers
// that don't need filters keep the historical RunLog signature; the new
// flag plumbing in cmd/act/main.go reaches RunLogOpts directly.
func RunLogOpts(repoRoot, idOrPrefix string, asJSON bool, opts LogOptions) (output any, exitCode int) {
	_ = asJSON // reserved: asJSON shapes the human renderer in main.go, not here.

	// Conflicting scope: positional id + --by-issue. Pick one.
	if idOrPrefix != "" && opts.ByIssue != "" && idOrPrefix != opts.ByIssue {
		return LogErrorOutput{
			Error:   "bad_flag",
			Message: "act log: pass either a positional <id> or --by-issue, not both",
		}, 2
	}
	scope := idOrPrefix
	if scope == "" {
		scope = opts.ByIssue
	}

	actDir := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return LogErrorOutput{
				Error:   "not_in_git",
				Message: fmt.Sprintf("act log: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return LogErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act log: stat %s: %v", actDir, err),
		}, 3
	}

	// Phase 2 ticket 5: read-path cache check. RunLog has no Fresh
	// option struct today, so only env-based ACT_DISPATCH_MODE bypass
	// applies; the default TTL gate still fires.
	_, _ = MaybeRefresh(repoRoot, MaybeRefreshOptions{})

	opsDir := filepath.Join(actDir, "ops")
	allIDs, err := listIssueIDs(opsDir)
	if err != nil {
		return LogErrorOutput{
			Error:   "ops_walk_failed",
			Message: err.Error(),
		}, 3
	}

	// Normalise the op-type filter once. Empty list means "all types".
	typeFilter, badType := normalizeOpTypeFilter(opts.Types)
	if badType != "" {
		return LogErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act log: --type: unknown op type %q", badType),
			Details: map[string]any{"value": badType},
		}, 2
	}

	// Time-window cutoff in HLC wall (ms since epoch). Zero means
	// "no time filter".
	var sinceMs int64
	if opts.Since > 0 {
		sinceMs = time.Now().Add(-opts.Since).UnixMilli()
	}

	// Issue scope: either one resolved id (when scope is non-empty) or
	// every id on disk.
	var scopeIDs []string
	resolvedID := ""
	if scope != "" {
		full, ambiguous, found := ids.ResolvePrefix(allIDs, scope)
		if ambiguous {
			candidates := ambiguousCandidates(allIDs, scope)
			// Exit 2 (usage): see resolve_helpers.go for the spec rationale.
			return LogErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act log: prefix %q matches %d issues", scope, len(candidates)),
				Details: map[string]any{
					"prefix":     scope,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		if !found {
			return LogErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act log: no issue matches %q", scope),
				Details: map[string]any{"query": scope},
			}, 3
		}
		scopeIDs = []string{full}
		resolvedID = full
	} else {
		scopeIDs = allIDs
	}

	var envs []loggedOp
	for _, id := range scopeIDs {
		got, rerr := readIssueOps(opsDir, id)
		if rerr != nil {
			return LogErrorOutput{
				Error:   "ops_read_failed",
				Message: rerr.Error(),
			}, 3
		}
		envs = append(envs, got...)
	}

	envs = applyLogFilters(envs, sinceMs, typeFilter)
	sortLogOps(envs)
	return LogResult{ID: resolvedID, Ops: envelopesOnly(envs)}, 0
}

// applyLogFilters returns the subset of envs that pass the active
// filters. sinceMs == 0 disables the time filter; an empty typeFilter
// disables the op-type filter. Both filters AND together when both are
// set.
func applyLogFilters(envs []loggedOp, sinceMs int64, typeFilter map[string]bool) []loggedOp {
	if sinceMs == 0 && len(typeFilter) == 0 {
		return envs
	}
	out := envs[:0]
	for _, e := range envs {
		if sinceMs != 0 && e.env.HLC.Wall < sinceMs {
			continue
		}
		if len(typeFilter) != 0 && !typeFilter[e.env.OpType] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// opTypeAliases maps user-friendly names to the spec op_type strings.
// The ticket (act-f800) names update / dep_add / delete in --help; the
// spec uses update_field / add_dep / tombstone. We accept either form.
var opTypeAliases = map[string]string{
	"update":     "update_field",
	"dep_add":    "add_dep",
	"dep_remove": "remove_dep",
	"delete":     "tombstone",
}

// normalizeOpTypeFilter turns the raw --type list (after comma-splitting
// at the flag layer) into a set keyed by spec op_type. Aliases are
// resolved; an unknown name is returned as the second value so the
// caller can shape a bad_flag error envelope. Empty input returns an
// empty (nil) set, meaning "no filter".
func normalizeOpTypeFilter(raw []string) (map[string]bool, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	out := make(map[string]bool, len(raw))
	for _, r := range raw {
		t := strings.ToLower(strings.TrimSpace(r))
		if t == "" {
			continue
		}
		if alias, ok := opTypeAliases[t]; ok {
			t = alias
		}
		if !op.ValidOpTypes[t] {
			return nil, r
		}
		out[t] = true
	}
	if len(out) == 0 {
		return nil, ""
	}
	return out, ""
}

// FormatLogHumanSummary renders a LogResult as a one-line-per-op timeline:
// `<RFC3339Millis wall> <op-type> <8hex-hash> <summary>` plus a trailing
// count line. The summary fragment is op-type-specific (title for create,
// field name for update_field, etc.) via opSummary so the line carries one
// extra hint about what changed without dropping to --json. When the result
// spans multiple issues an `[issue=<short>]` token is appended so the
// reader can tell ops apart. Mirrors the shape used by `act show
// --include-ops` (act-b891) so the two surfaces stay visually consistent.
// act-56a0.
func FormatLogHumanSummary(res LogResult) string {
	shortByID := shortIssueIndex(res)
	includeIssue := res.ID == ""
	var b strings.Builder
	for _, env := range res.Ops {
		hash, err := env.Hash()
		if err != nil {
			hash = "????????"
		}
		wall := time.UnixMilli(env.HLC.Wall).UTC().Format(rfc3339Millis)
		summary := opSummary(env)
		// Build the line with optional summary and optional issue tag.
		// We emit a trailing `[issue=<short>]` when the result is
		// cross-issue so the timeline stays readable; for a single-issue
		// scope it's redundant.
		fmt.Fprintf(&b, "%s %s %s", wall, env.OpType, hash)
		if summary != "" {
			fmt.Fprintf(&b, "  %s", summary)
		}
		if includeIssue {
			short := shortByID[env.IssueID]
			if short == "" {
				short = env.IssueID
			}
			fmt.Fprintf(&b, "  [issue=%s]", short)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%d ops\n", len(res.Ops))
	return b.String()
}

// shortIssueIndex returns a map from full issue id to its shortest unique
// prefix for the issue universe represented by res. For single-issue
// results the map contains just the one entry; for cross-issue results it
// covers the union of all issue ids in res.Ops.
func shortIssueIndex(res LogResult) map[string]string {
	if res.ID != "" {
		return ids.ShortestUniquePrefixes([]string{res.ID})
	}
	seen := map[string]bool{}
	var all []string
	for _, env := range res.Ops {
		if seen[env.IssueID] {
			continue
		}
		seen[env.IssueID] = true
		all = append(all, env.IssueID)
	}
	if len(all) == 0 {
		return map[string]string{}
	}
	return ids.ShortestUniquePrefixes(all)
}

// FormatLogHuman renders a LogResult as the human-friendly text form: one line
// per op with `<RFC3339Millis wall> <op-type> <8hex-hash> [issue=<short>]`,
// followed by a count line. Returns the multi-line string with a trailing
// newline. Callers print directly to stdout.
//
// When the LogResult spans multiple issues (res.ID == ""), the per-line
// `issue=<short>` field is computed per envelope from the union of all
// issue ids in the result so prefixes stay unambiguous.
func FormatLogHuman(res LogResult) string {
	shortByID := map[string]string{}
	if res.ID != "" {
		shortByID = ids.ShortestUniquePrefixes([]string{res.ID})
	} else {
		seen := map[string]bool{}
		var all []string
		for _, env := range res.Ops {
			if seen[env.IssueID] {
				continue
			}
			seen[env.IssueID] = true
			all = append(all, env.IssueID)
		}
		if len(all) > 0 {
			shortByID = ids.ShortestUniquePrefixes(all)
		}
	}
	var b strings.Builder
	for _, env := range res.Ops {
		hash, err := env.Hash()
		if err != nil {
			hash = "????????"
		}
		short := shortByID[env.IssueID]
		if short == "" {
			short = env.IssueID
		}
		wall := time.UnixMilli(env.HLC.Wall).UTC().Format(rfc3339Millis)
		fmt.Fprintf(&b, "%s %s %s [issue=%s]\n", wall, env.OpType, hash, short)
	}
	fmt.Fprintf(&b, "%d ops\n", len(res.Ops))
	return b.String()
}

// loggedOp is the in-memory representation of a parsed op for sorting:
// envelope plus its full canonical hash for the secondary tiebreak.
type loggedOp struct {
	env      op.Envelope
	fullHash string
}

// ListIssueIDs is the exported alias for listIssueIDs, used by sibling
// packages (e.g. internal/mcp) that compose multi-op writes and need to
// resolve ids against the on-disk universe.
func ListIssueIDs(opsDir string) ([]string, error) {
	return listIssueIDs(opsDir)
}

// listIssueIDs returns the full ids known to the repo by enumerating the
// per-issue subdirectories under `.act/ops/`. A missing opsDir is reported as
// an empty list, not an error: the caller distinguishes this from a corrupt
// `.act/` via the upstream stat on `.act/`.
func listIssueIDs(opsDir string) ([]string, error) {
	entries, err := os.ReadDir(opsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("act log: read %s: %w", opsDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !ids.IsValidID(name) {
			continue
		}
		out = append(out, name)
	}
	return out, nil
}

// readIssueOps walks `<opsDir>/<issueID>/<yyyy-mm>/*.json` and returns the
// parsed envelopes plus their canonical hashes. Files outside the
// month-shard layout (or with non-.json suffix) are skipped.
func readIssueOps(opsDir, issueID string) ([]loggedOp, error) {
	root := filepath.Join(opsDir, issueID)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("act log: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("act log: %s: not a directory", root)
	}

	var out []loggedOp
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("act log: walk %s: %w", path, werr)
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("act log: read %s: %w", path, rerr)
		}
		env, uerr := op.Unmarshal(body)
		if uerr != nil {
			return fmt.Errorf("act log: parse %s: %w", path, uerr)
		}
		full, herr := env.FullHash()
		if herr != nil {
			return fmt.Errorf("act log: hash %s: %w", path, herr)
		}
		out = append(out, loggedOp{env: env, fullHash: full})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}

// sortLogOps sorts ops by (HLC.Wall, HLC.Logical, fullHash) ascending. This is
// the same key fold uses; logging the audit stream must agree with the order
// fold consumes.
func sortLogOps(ops []loggedOp) {
	sort.SliceStable(ops, func(i, j int) bool {
		a, b := ops[i].env.HLC, ops[j].env.HLC
		if a.Wall != b.Wall {
			return a.Wall < b.Wall
		}
		if a.Logical != b.Logical {
			return a.Logical < b.Logical
		}
		return ops[i].fullHash < ops[j].fullHash
	})
}

// envelopesOnly extracts the envelope slice from a sorted []loggedOp. The
// fullHash side-data is not part of the JSON output (it's an in-memory sort
// key only).
func envelopesOnly(ops []loggedOp) []op.Envelope {
	out := make([]op.Envelope, len(ops))
	for i, o := range ops {
		out[i] = o.env
	}
	return out
}

// normalizePrefix is the local mirror of ids.normalizeHex (unexported in the
// ids package). We re-derive the hex tail to recompute the candidate list on
// the ambiguous path; the resolver itself doesn't return candidates.
func normalizePrefix(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "act-")
	return s
}

// stripActPrefix is the local mirror of ids.hexTail.
func stripActPrefix(id string) string {
	return strings.TrimPrefix(strings.ToLower(id), "act-")
}
