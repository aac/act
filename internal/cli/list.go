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

// ListOptions captures the flags accepted by `act list`. Zero values map
// to the spec defaults: the working set (non-closed), default sort, JSON
// off, and the limit is applied by callers when 0.
type ListOptions struct {
	// Status is a comma-separated list of statuses to include. An empty
	// string means "the default working set" — every status except
	// closed (act-9dfdc1). Ask for closed rows explicitly with
	// `--status closed`, or for everything with All.
	Status string
	// All, when true, drops the non-closed default and lists issues of
	// every status. It is mutually exclusive with Status: asking for
	// "everything" and "exactly these statuses" in one invocation has no
	// single honest answer, so RunList rejects the pair with exit 2
	// rather than silently letting one win.
	All bool
	// Assignee is exact-match. Empty means "any".
	Assignee string
	// Type is exact-match against the issue type enum. Empty means "any".
	Type string
	// Limit truncates the result set. <=0 means "no limit".
	Limit int
	// Sort is a comma-separated list of sort fields, each optionally
	// prefixed with `-` to indicate descending. Empty falls back to
	// "priority,-created_at" with id asc as a stable tie-breaker.
	Sort string
	// AsJSON controls the rendering layer; the function returns the same
	// data shape regardless and main.go decides how to render.
	AsJSON bool
	// Fresh, when true, forces the read-path cache layer to fetch+rebase
	// before reading state (Phase 2 ticket 5). Not surfaced as a CLI
	// flag on `act list` in this phase.
	Fresh bool
}

// ListedIssue is one row of the JSON output. JSON tags match the v0.1 spec
// shape (`id`, `short_id`, `title`, `status`, `priority`, `type`,
// `assignee`, `created_at`).
type ListedIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"short_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Priority  int    `json:"priority"`
	Type      string `json:"type"`
	Assignee  string `json:"assignee,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ListResult is the JSON-serialisable wrapper returned on success. The shape
// is `{"issues": [...], "count": N, "total": N, "truncated": bool}`.
//
// Count is how many rows were RETURNED; Total is how many matched the
// filters before Limit was applied. They differ exactly when the limit
// capped the result, which Truncated states outright.
//
// Why both, and why Truncated has no `omitempty` (act-b50d81): a capped
// listing that looks complete is the defect this shape exists to close. A
// JSON consumer must be able to test one unambiguous boolean rather than
// infer truncation from `count == limit` — which is wrong whenever the
// match count happens to equal the limit exactly. An always-present
// `false` is the cheap half of that contract; an omitted key would leave
// `.truncated` reading as null and put the consumer back to guessing.
type ListResult struct {
	Issues []ListedIssue `json:"issues"`
	Count  int           `json:"count"`
	// Total is the pre-limit match count. Equal to Count when the
	// listing was not capped.
	Total int `json:"total"`
	// Truncated reports whether Limit dropped rows that matched the
	// filters.
	Truncated bool `json:"truncated"`
}

// ListErrorOutput is the structured shape returned on failure.
type ListErrorOutput struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// validSortFields enumerates the keys understood by the sort key parser.
var validSortFields = map[string]bool{
	"priority":   true,
	"created_at": true,
	"closed_at":  true,
	"id":         true,
}

// sortKey is one parsed (field, descending) pair extracted from
// ListOptions.Sort.
type sortKey struct {
	Field string
	Desc  bool
}

// RunList implements `act list`. It opens the SQLite index (rebuilding it
// from the op log for v0.1 simplicity), filters by the supplied options,
// applies the requested sort, truncates by Limit, and returns a
// ListResult. The output is shape-agnostic: main.go renders JSON or the
// human-friendly form.
//
// With no status filter and All unset, the listing is the working set:
// open, in_progress and blocked. Closed issues are reachable via
// `--status closed` (or All) and, when they appear alongside live work,
// sort after it (act-9dfdc1).
//
// Returns:
//   - output: ListResult on success, ListErrorOutput on failure.
//   - exitCode: 0 success; 2 bad flag (unknown sort, malformed status,
//     --all with --status); 3 missing .act/.
func RunList(repoRoot string, opts ListOptions) (output any, exitCode int) {
	paths := config.Layout(repoRoot)

	// Step 1: require .act/.
	if _, err := os.Stat(paths.Root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ListErrorOutput{
				Error:   "no_repo",
				Message: fmt.Sprintf("act list: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return ListErrorOutput{
			Error:   "no_repo",
			Message: fmt.Sprintf("act list: stat %s: %v", paths.Root, err),
		}, 3
	}

	// Step 2: validate flags up front so we surface exit 2 before touching
	// the index.
	if opts.All && strings.TrimSpace(opts.Status) != "" {
		return ListErrorOutput{
			Error:   "bad_flag",
			Message: "act list: --all and --status are mutually exclusive; --all means every status, --status names the ones you want",
		}, 2
	}
	statuses, err := parseStatusCSV(opts.Status)
	if err != nil {
		return ListErrorOutput{
			Error:   "bad_flag",
			Message: err.Error(),
		}, 2
	}
	// The default listing is the WORKING SET: everything except closed
	// (act-9dfdc1). A tracker's closed rows outnumber its open ones by an
	// order of magnitude within weeks, so a default that included them
	// spent the row budget on finished work and pushed open work off the
	// end of the listing — the caller asking "what is there to do?" got
	// the answer least able to tell them.
	if !opts.All && len(statuses) == 0 {
		statuses = defaultStatuses()
	}
	keys, err := parseSortKeys(opts.Sort)
	if err != nil {
		return ListErrorOutput{
			Error:   "bad_flag",
			Message: err.Error(),
		}, 2
	}

	// Phase 2 ticket 5: read-path cache check.
	_, _ = MaybeRefresh(repoRoot, MaybeRefreshOptions{Fresh: opts.Fresh})

	// Step 3: open + rebuild the index. v0.1 unconditionally rebuilds; the
	// fold-checkpoint short-circuit is a future optimisation (see act-a1f6).
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return ListErrorOutput{
			Error:   "index_open_failed",
			Message: err.Error(),
		}, 1
	}
	defer func() { _ = idx.Close() }()
	if err := idx.Rebuild(paths.Ops); err != nil {
		return ListErrorOutput{
			Error:   "index_rebuild_failed",
			Message: err.Error(),
		}, 1
	}

	// Step 4: query. The index Filter only supports a single status, so we
	// pull the unfiltered (or assignee/type-narrowed) set and apply CSV
	// status filtering ourselves.
	filter := index.Filter{
		Type:     opts.Type,
		Assignee: opts.Assignee,
	}
	rows, err := idx.ListAll(filter)
	if err != nil {
		return ListErrorOutput{
			Error:   "index_query_failed",
			Message: err.Error(),
		}, 1
	}
	rows = filterByStatuses(rows, statuses)

	// Step 5: sort + limit. `total` is captured BEFORE the cap so the
	// result can report what the caller did not see (act-b50d81).
	sortRows(rows, keys)
	total := len(rows)
	truncated := false
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
		truncated = true
	}

	// Step 6: compute shortest-unique-prefix per id, then materialise the
	// rendered output rows.
	allIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		allIDs = append(allIDs, r.ID)
	}
	prefixes := ids.ShortestUniquePrefixes(allIDs)

	out := ListResult{Issues: make([]ListedIssue, 0, len(rows))}
	for _, r := range rows {
		short := prefixes[r.ID]
		if short == "" {
			short = r.ID
		}
		out.Issues = append(out.Issues, ListedIssue{
			ID:        r.ID,
			ShortID:   short,
			Title:     r.Title,
			Status:    r.Status,
			Priority:  r.Priority,
			Type:      r.Type,
			Assignee:  r.Assignee,
			CreatedAt: r.CreatedAt,
		})
	}
	out.Count = len(out.Issues)
	out.Total = total
	out.Truncated = truncated
	return out, 0
}

// FormatListTruncationNotice returns the one-line warning that must
// accompany a capped listing, or "" when nothing was dropped (act-b50d81).
//
// Callers print this to STDERR, not stdout. That placement is the whole
// point: the incident this fixes was `act list | <filter>`, where a
// stdout trailer is swallowed by the pipe and the human at the terminal
// sees a confidently wrong count. On stderr the warning reaches the human
// in both the piped and unpiped cases, and it never contaminates the row
// stream that downstream parsers read.
func FormatListTruncationNotice(res ListResult) string {
	if !res.Truncated {
		return ""
	}
	return fmt.Sprintf(
		"act list: WARNING: showing %d of %d matching issues — %d hidden by --limit. "+
			"Counts taken from this output will be WRONG. "+
			"Use `act list --limit 0` for everything, or narrow with --status/--assignee/--type.\n",
		res.Count, res.Total, res.Total-res.Count,
	)
}

// FormatListHuman renders a ListResult as one line per issue with
// `<short> <status> <prio> <title>` columns. A trailing newline is included.
func FormatListHuman(res ListResult) string {
	var b strings.Builder
	for _, it := range res.Issues {
		fmt.Fprintf(&b, "%s %s %d %s\n", it.ShortID, it.Status, it.Priority, it.Title)
	}
	return b.String()
}

// parseStatusCSV splits a comma-separated status filter into a slice. An
// empty input yields nil (meaning "no filter"). Each non-empty token must
// match the closed enum {open,in_progress,blocked,closed}. Whitespace
// around tokens is trimmed.
func parseStatusCSV(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			return nil, fmt.Errorf("act list: --status: empty token in %q", raw)
		}
		switch s {
		case "open", "in_progress", "blocked", "closed":
		default:
			return nil, fmt.Errorf("act list: --status: unknown status %q", s)
		}
		out = append(out, s)
	}
	return out, nil
}

// parseSortKeys splits a comma-separated sort spec into a slice of sortKey.
// An empty input returns the default keys: priority asc, created_at desc,
// id asc.
func parseSortKeys(raw string) ([]sortKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSortKeys(), nil
	}
	parts := strings.Split(raw, ",")
	out := make([]sortKey, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			return nil, fmt.Errorf("act list: --sort: empty token in %q", raw)
		}
		desc := false
		// Accept both `-field` (CSV form) and `field:desc` (spec
		// alternate form).
		if strings.HasPrefix(s, "-") {
			desc = true
			s = strings.TrimPrefix(s, "-")
		} else if strings.HasPrefix(s, "+") {
			s = strings.TrimPrefix(s, "+")
		} else if i := strings.Index(s, ":"); i >= 0 {
			suffix := strings.ToLower(strings.TrimSpace(s[i+1:]))
			switch suffix {
			case "asc":
				desc = false
			case "desc":
				desc = true
			default:
				return nil, fmt.Errorf("act list: --sort: unknown direction %q", suffix)
			}
			s = strings.TrimSpace(s[:i])
		}
		if !validSortFields[s] {
			return nil, fmt.Errorf("act list: --sort: unknown field %q", s)
		}
		out = append(out, sortKey{Field: s, Desc: desc})
	}
	// Always append id asc as the final tie-breaker if the caller did
	// not include it.
	hasID := false
	for _, k := range out {
		if k.Field == "id" {
			hasID = true
			break
		}
	}
	if !hasID {
		out = append(out, sortKey{Field: "id", Desc: false})
	}
	return out, nil
}

// defaultStatuses is the working set: every status except closed. It is
// what a listing shows when the caller named no --status and did not ask
// for --all (act-9dfdc1).
func defaultStatuses() []string {
	return []string{"open", "in_progress", "blocked"}
}

// defaultSortKeys returns the spec default sort: priority asc, created_at
// desc, id asc tie-breaker.
func defaultSortKeys() []sortKey {
	return []sortKey{
		{Field: "priority", Desc: false},
		{Field: "created_at", Desc: true},
		{Field: "id", Desc: false},
	}
}

// filterByStatuses drops rows whose status is not in the supplied set.
// statuses==nil (or empty) means "no filter".
func filterByStatuses(rows []index.Row, statuses []string) []index.Row {
	if len(statuses) == 0 {
		return rows
	}
	keep := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		keep[s] = true
	}
	out := make([]index.Row, 0, len(rows))
	for _, r := range rows {
		if keep[r.Status] {
			out = append(out, r)
		}
	}
	return out
}

// sortRows applies a multi-key stable sort to rows, with one grouping key
// ahead of them all: closed issues sort AFTER everything still live
// (act-9dfdc1).
//
// The grouping is deliberately not expressible as a sort field and not
// overridable by --sort. A listing that mixes statuses is answering "what
// is the state of this tracker?", and a closed row placed above open work
// — which the default priority-asc sort did, since p0 closed beats p1 open
// — buries the only rows the reader can act on. Within each group the
// caller's sort keys apply unchanged, so --sort still controls the order
// of the rows it is asked about.
func sortRows(rows []index.Row, keys []sortKey) {
	sort.SliceStable(rows, func(i, j int) bool {
		if c := cmpInt(closedRank(rows[i]), closedRank(rows[j])); c != 0 {
			return c < 0
		}
		for _, k := range keys {
			cmp := compareRowField(rows[i], rows[j], k.Field)
			if cmp == 0 {
				continue
			}
			if k.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}

// closedRank ranks a row for the closed-last grouping: 0 for live work,
// 1 for closed.
func closedRank(r index.Row) int {
	if r.Status == "closed" {
		return 1
	}
	return 0
}

// compareRowField returns -1/0/1 comparing a and b on the named field.
// Unknown fields compare equal (caller should have validated the field
// name via parseSortKeys).
func compareRowField(a, b index.Row, field string) int {
	switch field {
	case "priority":
		return cmpInt(a.Priority, b.Priority)
	case "created_at":
		return strings.Compare(a.CreatedAt, b.CreatedAt)
	case "closed_at":
		return strings.Compare(a.ClosedAt, b.ClosedAt)
	case "id":
		return strings.Compare(a.ID, b.ID)
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
