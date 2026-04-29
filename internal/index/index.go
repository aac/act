// Package index manages the SQLite read index derived from the folded op log.
//
// The index is a derived cache only: the .act/ops/ tree is the source of
// truth. Index rows are produced by folding ops and rendering each issue's
// terminal state. The file lives at .act/index.db and is gitignored.
//
// The driver is modernc.org/sqlite (pure Go, no cgo).
package index

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/aac/act/internal/fold"
)

// schemaSQL is the canonical schema for index.db. ApplySchema executes this
// in a single transaction; every CREATE statement uses IF NOT EXISTS so the
// call is idempotent.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS issues (
    id            TEXT PRIMARY KEY,
    title         TEXT,
    description   TEXT,
    status        TEXT,
    priority      INTEGER,
    type          TEXT,
    parent        TEXT,
    assignee      TEXT,
    created_at    TEXT,
    closed_at     TEXT,
    closed_reason TEXT,
    tombstoned    INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS issue_accept (
    issue_id  TEXT NOT NULL,
    idx       INTEGER NOT NULL,
    criterion TEXT,
    PRIMARY KEY(issue_id, idx)
);

CREATE TABLE IF NOT EXISTS issue_deps (
    issue_id  TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    edge_type TEXT NOT NULL,
    PRIMARY KEY(issue_id, parent_id, edge_type)
);

CREATE TABLE IF NOT EXISTS issue_meta (
    issue_id       TEXT PRIMARY KEY,
    schema_version INTEGER
);

CREATE INDEX IF NOT EXISTS idx_status   ON issues(status);
CREATE INDEX IF NOT EXISTS idx_priority ON issues(priority);
CREATE INDEX IF NOT EXISTS idx_parent   ON issues(parent);

CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(
    issue_id UNINDEXED,
    title,
    description,
    tokenize = 'unicode61'
);
`

// Index is a handle to .act/index.db.
type Index struct {
	db   *sql.DB
	path string
}

// Open opens (or creates) a SQLite db at dbPath and returns an Index handle.
// The database is opened with WAL journal_mode, NORMAL synchronous, and
// foreign_keys=ON. The call is idempotent; repeated opens of the same path
// produce equivalent handles.
//
// Schema is NOT applied automatically; callers should invoke ApplySchema.
func Open(dbPath string) (*Index, error) {
	// modernc.org/sqlite accepts query parameters via the DSN:
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("index: open %s: %w", dbPath, err)
	}
	// Probe for usability — sql.Open is lazy and will not surface a corrupt
	// header until the first query. Run a trivial pragma to flush that.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("index: ping %s: %w", dbPath, err)
	}
	return &Index{db: db, path: dbPath}, nil
}

// Schema returns the SQL schema as a string. Tests use it to assert the
// expected DDL contents.
func (i *Index) Schema() string { return schemaSQL }

// DB exposes the underlying *sql.DB so other packages (e.g. cli/search) can
// run ad-hoc queries — notably FTS5 MATCH joins — without re-opening the
// database. Callers must not Close the returned handle; ownership remains
// with the Index.
func (i *Index) DB() *sql.DB { return i.db }

// ApplySchema creates tables and indices if missing. It is safe to call on a
// freshly opened Index or one that already has the schema applied.
func (i *Index) ApplySchema() error {
	if _, err := i.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("index: apply schema: %w", err)
	}
	return nil
}

// Close closes the underlying database handle. Subsequent calls on the
// Index are not valid.
func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

// Filter narrows the rows returned by ListAll. Empty fields impose no
// constraint.
type Filter struct {
	Status   string
	Type     string
	Assignee string
}

// Dep is a single dependency edge between two issues.
type Dep struct {
	Parent   string
	EdgeType string
}

// Row is a denormalised view of one issues row plus its accept criteria
// and dependency edges.
type Row struct {
	ID           string
	Title        string
	Description  string
	Status       string
	Type         string
	Parent       string
	Assignee     string
	Priority     int
	CreatedAt    string
	ClosedAt     string
	ClosedReason string
	Accept       []string
	Deps         []Dep
}

// Rebuild drops every row from the index and re-populates it from a fresh
// fold of rootOps.
//
// The rebuild runs in a single transaction. On any error the transaction is
// rolled back and the database is left untouched.
func (i *Index) Rebuild(rootOps string) error {
	if err := i.ApplySchema(); err != nil {
		return err
	}

	// Always do a full fold here — the FoldWithCheckpoint short-circuit
	// returns nil FoldResult on a checkpoint hit, which would zero-out the
	// index. The checkpoint path is intentionally empty so the caller can
	// drive checkpoint persistence separately (act-a1f6 owns that).
	res, err := fold.Fold(rootOps, fold.ApplyDispatch)
	if err != nil {
		return fmt.Errorf("index: fold for rebuild: %w", err)
	}

	tx, err := i.db.Begin()
	if err != nil {
		return fmt.Errorf("index: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{
		"DELETE FROM issues",
		"DELETE FROM issue_accept",
		"DELETE FROM issue_deps",
		"DELETE FROM issue_meta",
		"DELETE FROM fts",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("index: rebuild clear (%s): %w", stmt, err)
		}
	}

	if res != nil {
		for _, st := range res.Issues {
			if err := upsertTx(tx, st); err != nil {
				return fmt.Errorf("index: rebuild upsert %s: %w", st.ID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("index: commit rebuild: %w", err)
	}
	return nil
}

// Upsert inserts or replaces a single issue's rows from its terminal folded
// state. Used by command implementations after they write an op so the
// index reflects the new state without a full rebuild.
func (i *Index) Upsert(state *fold.IssueState) error {
	if state == nil {
		return fmt.Errorf("index: upsert: nil state")
	}
	tx, err := i.db.Begin()
	if err != nil {
		return fmt.Errorf("index: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertTx(tx, state); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertTx performs the per-issue insert/replace within an open transaction.
// Tombstoned issues are deleted from every table (they are invisible from
// the public render).
func upsertTx(tx *sql.Tx, state *fold.IssueState) error {
	id := state.ID
	// Always clear prior rows for this issue, so re-runs are idempotent.
	for _, stmt := range []struct{ q, arg string }{
		{"DELETE FROM issues       WHERE id       = ?", id},
		{"DELETE FROM issue_accept WHERE issue_id = ?", id},
		{"DELETE FROM issue_deps   WHERE issue_id = ?", id},
		{"DELETE FROM issue_meta   WHERE issue_id = ?", id},
		{"DELETE FROM fts          WHERE issue_id = ?", id},
	} {
		if _, err := tx.Exec(stmt.q, stmt.arg); err != nil {
			return fmt.Errorf("index: clear rows for %s: %w", id, err)
		}
	}

	if state.Tombstoned {
		// Tombstoned issues are not observable; we still track the id in
		// issue_meta so doctor can detect divergence.
		if _, err := tx.Exec(
			`INSERT INTO issues (id, status, tombstoned) VALUES (?, ?, 1)`,
			id, "tombstoned",
		); err != nil {
			return fmt.Errorf("index: insert tombstoned %s: %w", id, err)
		}
		return nil
	}

	rendered := fold.RenderState(state)
	if rendered == nil {
		return nil
	}

	title, _ := rendered["title"].(string)
	description, _ := rendered["description"].(string)
	status, _ := rendered["status"].(string)
	itype, _ := rendered["type"].(string)
	parent, _ := rendered["parent"].(string)
	assignee, _ := rendered["assignee"].(string)
	createdAt, _ := rendered["created_at"].(string)
	closedAt, _ := rendered["closed_at"].(string)
	closedReason, _ := rendered["closed_reason"].(string)
	priority := coerceInt(rendered["priority"])

	if _, err := tx.Exec(`
        INSERT INTO issues (
            id, title, description, status, priority, type, parent, assignee,
            created_at, closed_at, closed_reason, tombstoned
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
    `, id, title, description, status, priority, itype, parent, assignee,
		createdAt, closedAt, closedReason); err != nil {
		return fmt.Errorf("index: insert issues row %s: %w", id, err)
	}

	// Accept criteria.
	if accept, ok := rendered["accept"].([]string); ok {
		for idx, crit := range accept {
			if _, err := tx.Exec(
				`INSERT INTO issue_accept (issue_id, idx, criterion) VALUES (?, ?, ?)`,
				id, idx, crit,
			); err != nil {
				return fmt.Errorf("index: insert accept %s[%d]: %w", id, idx, err)
			}
		}
	}

	// Dependency edges.
	if deps, ok := rendered["deps"].([]map[string]string); ok {
		for _, d := range deps {
			if _, err := tx.Exec(
				`INSERT INTO issue_deps (issue_id, parent_id, edge_type) VALUES (?, ?, ?)`,
				id, d["parent"], d["edge_type"],
			); err != nil {
				return fmt.Errorf("index: insert dep %s->%s: %w", id, d["parent"], err)
			}
		}
	}

	// FTS row — title + description.
	if _, err := tx.Exec(
		`INSERT INTO fts (issue_id, title, description) VALUES (?, ?, ?)`,
		id, title, description,
	); err != nil {
		return fmt.Errorf("index: insert fts %s: %w", id, err)
	}

	// Meta — schema_version is op envelope schema for now.
	if _, err := tx.Exec(
		`INSERT INTO issue_meta (issue_id, schema_version) VALUES (?, ?)`,
		id, 1,
	); err != nil {
		return fmt.Errorf("index: insert meta %s: %w", id, err)
	}
	return nil
}

// coerceInt extracts an int from a value that came out of RenderState — which
// produces ints from create payloads and float64 from JSON round-trips. The
// fold package leaves priority as plain int after applyCreate, but
// update_field can deliver a json.RawMessage decoded as float64.
func coerceInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return 0
}

// ListAll returns every (non-tombstoned) issue matching filter, ordered by
// (priority asc, id asc) for stable output.
func (i *Index) ListAll(filter Filter) ([]Row, error) {
	var (
		where []string
		args  []any
	)
	where = append(where, "tombstoned = 0")
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Type != "" {
		where = append(where, "type = ?")
		args = append(args, filter.Type)
	}
	if filter.Assignee != "" {
		where = append(where, "assignee = ?")
		args = append(args, filter.Assignee)
	}
	q := `
        SELECT id, title, description, status, priority, type, parent,
               assignee, created_at, closed_at, closed_reason
          FROM issues
         WHERE ` + strings.Join(where, " AND ") + `
         ORDER BY priority ASC, id ASC
    `
	rows, err := i.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("index: list query: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		if err := i.fillAcceptDeps(&r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: list iter: %w", err)
	}
	return out, nil
}

// Get returns the row for id. It returns sql.ErrNoRows on miss.
func (i *Index) Get(id string) (Row, error) {
	row := i.db.QueryRow(`
        SELECT id, title, description, status, priority, type, parent,
               assignee, created_at, closed_at, closed_reason
          FROM issues
         WHERE id = ? AND tombstoned = 0
    `, id)
	r, err := scanSingleRow(row)
	if err != nil {
		return Row{}, err
	}
	if err := i.fillAcceptDeps(&r); err != nil {
		return Row{}, err
	}
	return r, nil
}

// scanner is the shared interface implemented by sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanInto(s scanner) (Row, error) {
	var r Row
	var (
		title, description, status, itype, parent, assignee sql.NullString
		createdAt, closedAt, closedReason                   sql.NullString
		priority                                            sql.NullInt64
	)
	if err := s.Scan(&r.ID, &title, &description, &status, &priority, &itype, &parent,
		&assignee, &createdAt, &closedAt, &closedReason); err != nil {
		return Row{}, err
	}
	r.Title = title.String
	r.Description = description.String
	r.Status = status.String
	r.Type = itype.String
	r.Parent = parent.String
	r.Assignee = assignee.String
	r.CreatedAt = createdAt.String
	r.ClosedAt = closedAt.String
	r.ClosedReason = closedReason.String
	r.Priority = int(priority.Int64)
	return r, nil
}

func scanRow(rows *sql.Rows) (Row, error) { return scanInto(rows) }

func scanSingleRow(row *sql.Row) (Row, error) { return scanInto(row) }

func (i *Index) fillAcceptDeps(r *Row) error {
	// accept
	arows, err := i.db.Query(
		`SELECT criterion FROM issue_accept WHERE issue_id = ? ORDER BY idx ASC`, r.ID)
	if err != nil {
		return fmt.Errorf("index: load accept %s: %w", r.ID, err)
	}
	for arows.Next() {
		var c sql.NullString
		if err := arows.Scan(&c); err != nil {
			arows.Close()
			return fmt.Errorf("index: scan accept %s: %w", r.ID, err)
		}
		r.Accept = append(r.Accept, c.String)
	}
	arows.Close()

	// deps
	drows, err := i.db.Query(
		`SELECT parent_id, edge_type FROM issue_deps WHERE issue_id = ? ORDER BY parent_id, edge_type`,
		r.ID)
	if err != nil {
		return fmt.Errorf("index: load deps %s: %w", r.ID, err)
	}
	for drows.Next() {
		var d Dep
		if err := drows.Scan(&d.Parent, &d.EdgeType); err != nil {
			drows.Close()
			return fmt.Errorf("index: scan dep %s: %w", r.ID, err)
		}
		r.Deps = append(r.Deps, d)
	}
	drows.Close()
	return nil
}
