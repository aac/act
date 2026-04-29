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
// to the spec defaults: no filter, default sort, JSON off, and the limit is
// applied by callers when 0.
type ListOptions struct {
	// Status is a comma-separated list of statuses to include. An empty
	// string means "no status filter" (callers may still apply the
	// non-closed default at the rendering layer).
	Status string
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
// is `{"issues": [...], "count": N}`.
type ListResult struct {
	Issues []ListedIssue `json:"issues"`
	Count  int           `json:"count"`
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
// Returns:
//   - output: ListResult on success, ListErrorOutput on failure.
//   - exitCode: 0 success; 2 bad flag (unknown sort, malformed status,
//     limit==0); 3 missing .act/.
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
	statuses, err := parseStatusCSV(opts.Status)
	if err != nil {
		return ListErrorOutput{
			Error:   "bad_flag",
			Message: err.Error(),
		}, 2
	}
	keys, err := parseSortKeys(opts.Sort)
	if err != nil {
		return ListErrorOutput{
			Error:   "bad_flag",
			Message: err.Error(),
		}, 2
	}

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

	// Step 5: sort + limit.
	sortRows(rows, keys)
	if opts.Limit > 0 && len(rows) > opts.Limit {
		rows = rows[:opts.Limit]
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
	return out, 0
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

// sortRows applies a multi-key stable sort to rows. The first key is the
// primary; subsequent keys break ties.
func sortRows(rows []index.Row, keys []sortKey) {
	sort.SliceStable(rows, func(i, j int) bool {
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
