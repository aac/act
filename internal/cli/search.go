package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/index"
)

// SearchOptions captures the flag knobs for `act search`.
type SearchOptions struct {
	// In is one of "title", "desc", "all" (default "all"). It restricts the
	// FTS5 column scope.
	In string
	// Status is a CSV list of status values to include. Empty means no
	// filter.
	Status string
	// Limit caps the result count. Zero or negative means use the default
	// (50).
	Limit int
	// AsJSON is reserved for compatibility with the parent renderer; the
	// returned `output` shape is the same map either way.
	AsJSON bool
}

// SearchMatch is one row in the search results.
type SearchMatch struct {
	ID      string `json:"id"`
	ShortID string `json:"short_id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Snippet string `json:"snippet"`
}

// SearchResult is the success envelope returned by RunSearch.
type SearchResult struct {
	Matches []SearchMatch `json:"matches"`
	Count   int           `json:"count"`
}

// SearchErrorOutput is the failure envelope returned by RunSearch. Code is
// the planned exit code; Kind names the failure category for machine
// consumers.
type SearchErrorOutput struct {
	Error   string `json:"error"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
}

// defaultSearchLimit is the default --limit value.
const defaultSearchLimit = 50

// RunSearch implements `act search <query>`.
//
// Returns:
//   - output: SearchResult on success, SearchErrorOutput on failure.
//   - exitCode: 0 success (incl. empty); 2 bad flag or FTS5 parse error;
//     3 missing .act/.
func RunSearch(repoRoot, query string, opts SearchOptions) (output any, exitCode int) {
	// 1. Validate repo + .act/.
	actDir := filepath.Join(repoRoot, ".act")
	if _, err := os.Stat(actDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SearchErrorOutput{
				Error:   "not_in_git",
				Kind:    "missing_act",
				Message: fmt.Sprintf("act search: %s/.act not found; run `act init` first", repoRoot),
			}, 3
		}
		return SearchErrorOutput{
			Error:   "not_in_git",
			Kind:    "missing_act",
			Message: fmt.Sprintf("act search: stat %s: %v", actDir, err),
		}, 3
	}

	// 1b. Validate flags.
	in := strings.ToLower(strings.TrimSpace(opts.In))
	if in == "" {
		in = "all"
	}
	switch in {
	case "title", "desc", "all":
	default:
		return SearchErrorOutput{
			Error:   "invalid_flag",
			Kind:    "bad_in",
			Message: fmt.Sprintf("act search: --in must be title|desc|all, got %q", opts.In),
		}, 2
	}

	limit := opts.Limit
	if limit < 0 {
		return SearchErrorOutput{
			Error:   "invalid_flag",
			Kind:    "bad_limit",
			Message: fmt.Sprintf("act search: --limit must be >= 0, got %d", opts.Limit),
		}, 2
	}
	if limit == 0 {
		limit = defaultSearchLimit
	}

	statuses := splitCSV(opts.Status)

	if strings.TrimSpace(query) == "" {
		return SearchErrorOutput{
			Error:   "invalid_flag",
			Kind:    "empty_query",
			Message: "act search: query is required",
		}, 2
	}

	if err := validateFTSQuery(query); err != nil {
		return SearchErrorOutput{
			Error:   "fts_parse",
			Kind:    "fts_parse",
			Message: fmt.Sprintf("act search: %v", err),
		}, 2
	}

	// 2. Open the index and rebuild for freshness.
	paths := config.Layout(repoRoot)
	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		return SearchErrorOutput{
			Error:   "index_open_failed",
			Kind:    "index_open",
			Message: fmt.Sprintf("act search: %v", err),
		}, 3
	}
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		return SearchErrorOutput{
			Error:   "index_open_failed",
			Kind:    "index_open",
			Message: fmt.Sprintf("act search: %v", err),
		}, 3
	}
	defer idx.Close()

	if err := idx.Rebuild(paths.Ops); err != nil {
		return SearchErrorOutput{
			Error:   "index_rebuild_failed",
			Kind:    "index_rebuild",
			Message: fmt.Sprintf("act search: %v", err),
		}, 3
	}

	// 3. Build FTS query string with optional column qualifier.
	ftsQuery, err := composeFTSQuery(in, query)
	if err != nil {
		return SearchErrorOutput{
			Error:   "fts_parse",
			Kind:    "fts_parse",
			Message: fmt.Sprintf("act search: %v", err),
		}, 2
	}

	// 4. Run the join query.
	db := idx.DB()
	sqlQ, args := buildSearchSQL(ftsQuery, statuses, limit)
	rows, err := db.Query(sqlQ, args...)
	if err != nil {
		// FTS5 parse errors surface here from the SQLite driver.
		if isFTSError(err) {
			return SearchErrorOutput{
				Error:   "fts_parse",
				Kind:    "fts_parse",
				Message: fmt.Sprintf("act search: %v", err),
			}, 2
		}
		return SearchErrorOutput{
			Error:   "query_failed",
			Kind:    "query",
			Message: fmt.Sprintf("act search: %v", err),
		}, 2
	}
	defer rows.Close()

	var (
		matches []SearchMatch
		fullIDs []string
	)
	type rawMatch struct {
		id      string
		title   string
		status  string
		snippet string
	}
	var raw []rawMatch
	for rows.Next() {
		var (
			id, title, status, snippet sql.NullString
		)
		if err := rows.Scan(&id, &title, &status, &snippet); err != nil {
			return SearchErrorOutput{
				Error:   "query_failed",
				Kind:    "scan",
				Message: fmt.Sprintf("act search: %v", err),
			}, 2
		}
		raw = append(raw, rawMatch{
			id: id.String, title: title.String, status: status.String, snippet: snippet.String,
		})
		fullIDs = append(fullIDs, id.String)
	}
	if err := rows.Err(); err != nil {
		if isFTSError(err) {
			return SearchErrorOutput{
				Error:   "fts_parse",
				Kind:    "fts_parse",
				Message: fmt.Sprintf("act search: %v", err),
			}, 2
		}
		return SearchErrorOutput{
			Error:   "query_failed",
			Kind:    "iter",
			Message: fmt.Sprintf("act search: %v", err),
		}, 2
	}

	shortByID := ids.ShortestUniquePrefixes(fullIDs)
	for _, r := range raw {
		short := shortByID[r.id]
		if short == "" {
			short = r.id
		}
		matches = append(matches, SearchMatch{
			ID:      r.id,
			ShortID: short,
			Title:   r.title,
			Status:  r.status,
			Snippet: r.snippet,
		})
	}

	if matches == nil {
		matches = []SearchMatch{}
	}
	return SearchResult{Matches: matches, Count: len(matches)}, 0
}

// FormatSearchHuman renders a SearchResult as one line per match:
//
//	<short> <title> — <snippet>
//
// followed by a count line. Empty result emits only the count line.
func FormatSearchHuman(res SearchResult) string {
	var b strings.Builder
	for _, m := range res.Matches {
		fmt.Fprintf(&b, "%s %s — %s\n", m.ShortID, m.Title, m.Snippet)
	}
	fmt.Fprintf(&b, "%d matches\n", res.Count)
	return b.String()
}

// splitCSV splits a comma-separated list, trimming whitespace and dropping
// empty fields. A blank input yields nil.
func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validateFTSQuery performs a lightweight pre-flight check on the user query
// before handing it to SQLite. Catches the common syntactic errors (unbalanced
// quotes / parens) so we can surface a usage error without a partial DB
// roundtrip.
func validateFTSQuery(q string) error {
	if strings.TrimSpace(q) == "" {
		return errors.New("empty FTS5 query")
	}
	depth := 0
	inQuote := false
	for i := 0; i < len(q); i++ {
		c := q[i]
		if inQuote {
			if c == '"' {
				inQuote = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return errors.New("unbalanced parentheses in FTS5 query")
			}
		}
	}
	if inQuote {
		return errors.New("unbalanced quote in FTS5 query")
	}
	if depth != 0 {
		return errors.New("unbalanced parentheses in FTS5 query")
	}
	return nil
}

// composeFTSQuery wraps the user query with a column qualifier when
// requested. The user query is already pre-validated; we still escape any
// embedded double-quotes if we need to wrap the whole thing as a quoted
// string.
//
// For "all" we pass the query through unmodified, so users can write FTS5
// expressions (AND/OR/NOT, "phrase queries", column filters) directly.
//
// For "title"/"desc" we build `<col>:(<query>)` so the user's expression is
// scoped to a single column without losing its internal grammar.
func composeFTSQuery(in, query string) (string, error) {
	q := strings.TrimSpace(query)
	switch in {
	case "all":
		return q, nil
	case "title":
		return "title:(" + q + ")", nil
	case "desc":
		return "description:(" + q + ")", nil
	}
	return "", fmt.Errorf("unknown --in value %q", in)
}

// buildSearchSQL composes the SQL string and bind args for a search query.
func buildSearchSQL(ftsQuery string, statuses []string, limit int) (string, []any) {
	var (
		whereParts []string
		args       []any
	)
	whereParts = append(whereParts, "fts MATCH ?")
	args = append(args, ftsQuery)

	whereParts = append(whereParts, "issues.tombstoned = 0")

	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, s := range statuses {
			placeholders[i] = "?"
			args = append(args, s)
		}
		whereParts = append(whereParts, "issues.status IN ("+strings.Join(placeholders, ",")+")")
	}

	args = append(args, limit)

	q := `
        SELECT issues.id,
               issues.title,
               issues.status,
               snippet(fts, -1, '[', ']', '...', 16) AS snip
          FROM fts
          JOIN issues ON issues.id = fts.issue_id
         WHERE ` + strings.Join(whereParts, " AND ") + `
         ORDER BY bm25(fts) ASC
         LIMIT ?
    `
	return q, args
}

// isFTSError returns true if err looks like a SQLite FTS5 syntax error. The
// modernc.org/sqlite driver wraps these as plain errors; we match by
// substring.
func isFTSError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "fts5") ||
		strings.Contains(msg, "syntax error near") ||
		strings.Contains(msg, "malformed match")
}
