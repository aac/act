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
    claimed_at    TEXT,
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

CREATE TABLE IF NOT EXISTS issue_external_deps (
    issue_id TEXT NOT NULL,
    ref      TEXT NOT NULL,
    PRIMARY KEY(issue_id, ref)
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

// IsMalformed reports whether err looks like a SQLite "database is malformed"
// or "file is not a database" failure surfaced by the modernc.org/sqlite
// driver. Doctor's `--fix-index` path uses this to disambiguate "the index
// file is unusable; rebuild from ops/" from "every other open/exec failure".
//
// The modernc driver returns errors whose string form embeds the SQLite
// extended result code text — "database disk image is malformed (11)" for
// SQLITE_CORRUPT, and "file is not a database (26)" for SQLITE_NOTADB
// (truncated file, scrambled header). We match on the canonical substrings
// rather than asserting the concrete error type so the helper survives a
// driver version bump that re-wraps the underlying error.
func IsMalformed(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "database disk image is malformed") ||
		strings.Contains(s, "file is not a database")
}

// Open opens (or creates) a SQLite db at dbPath and returns an Index handle.
// The database is opened with WAL journal_mode, NORMAL synchronous, and
// foreign_keys=ON. The call is idempotent; repeated opens of the same path
// produce equivalent handles.
//
// Schema is NOT applied automatically; callers should invoke ApplySchema.
//
// A truncated, header-corrupted, or otherwise unreadable file surfaces here
// as an error matched by IsMalformed (the driver fails db.Ping with the
// SQLITE_NOTADB result code text). A file whose header is intact but whose
// page tree is corrupt passes Open; callers that care must invoke
// IntegrityCheck on the returned handle.
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
//
// After running the canonical CREATE TABLE IF NOT EXISTS pass, ApplySchema
// reconciles each tracked table's column list with the expected list and
// issues `ALTER TABLE ... ADD COLUMN` for any missing nullable columns. This
// is the migration path for an index.db that pre-dates a backwards-compatible
// schema addition (act-4bb6, fixing the act-4b45 regression where
// `claimed_at` was added without migration). The migration is silent on
// already-current databases — `PRAGMA table_info` finds the column, the
// missing-set is empty, and no ALTER fires.
//
// ALTER TABLE in SQLite supports ADD COLUMN cleanly but cannot drop or
// rename in older builds, so this path only handles additive schema changes.
// A destructive change (column removed or renamed) would require the
// `.act/ops/` rebuild path; doctor's `--fix-index` covers that case.
func (i *Index) ApplySchema() error {
	if _, err := i.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("index: apply schema: %w", err)
	}
	if err := i.migrateAddMissingColumns(); err != nil {
		return fmt.Errorf("index: migrate: %w", err)
	}
	return nil
}

// expectedColumns names the column type for every column the current
// schemaSQL expects on each tracked table. Migration adds any missing column
// via `ALTER TABLE ... ADD COLUMN <name> <type>`. Keep this in sync with the
// CREATE TABLE statements above when adding columns.
//
// Order within a table matters only for newly-created (post-ALTER) columns:
// SQLite appends ADDed columns to the end of the row regardless of where they
// appear in this list. Keeping the list in declaration order makes the
// mapping easy to audit.
var expectedColumns = map[string][]struct{ name, sqlType string }{
	"issues": {
		{"id", "TEXT PRIMARY KEY"},
		{"title", "TEXT"},
		{"description", "TEXT"},
		{"status", "TEXT"},
		{"priority", "INTEGER"},
		{"type", "TEXT"},
		{"parent", "TEXT"},
		{"assignee", "TEXT"},
		{"created_at", "TEXT"},
		{"claimed_at", "TEXT"},
		{"closed_at", "TEXT"},
		{"closed_reason", "TEXT"},
		{"tombstoned", "INTEGER DEFAULT 0"},
	},
}

// migrateAddMissingColumns introspects each tracked table and runs
// `ALTER TABLE ... ADD COLUMN` for any column expectedColumns lists that
// `PRAGMA table_info` does not report. The PRIMARY KEY column is skipped
// for ALTER (SQLite cannot add a PK via ALTER) — its absence indicates a
// fresh table that the prior `CREATE TABLE IF NOT EXISTS` already
// populated, so by the time we reach this point, the PK column is always
// present.
func (i *Index) migrateAddMissingColumns() error {
	for table, cols := range expectedColumns {
		have, err := i.tableColumns(table)
		if err != nil {
			return err
		}
		for _, c := range cols {
			if _, ok := have[c.name]; ok {
				continue
			}
			// SQLite ALTER TABLE ADD COLUMN cannot add a PRIMARY KEY
			// column. The PK is set at CREATE TABLE time; if it's
			// missing here, the table itself is missing — which the
			// prior CREATE TABLE IF NOT EXISTS would have repaired.
			alterType := c.sqlType
			if strings.Contains(strings.ToUpper(alterType), "PRIMARY KEY") {
				continue
			}
			stmt := fmt.Sprintf(
				`ALTER TABLE %s ADD COLUMN %s %s`,
				table, c.name, alterType,
			)
			if _, err := i.db.Exec(stmt); err != nil {
				return fmt.Errorf("alter %s add %s: %w", table, c.name, err)
			}
		}
	}
	return nil
}

// tableColumns returns the set of column names present on table according to
// SQLite's `PRAGMA table_info(<table>)`. An empty set indicates the table
// does not exist (PRAGMA returns zero rows in that case, no error).
func (i *Index) tableColumns(table string) (map[string]struct{}, error) {
	rows, err := i.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()
	out := make(map[string]struct{})
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		out[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("table_info(%s) iter: %w", table, err)
	}
	return out, nil
}

// IntegrityCheck runs `PRAGMA integrity_check` and returns nil when the
// database reports `ok`. A genuine page-tree corruption (one that survives
// Open + Ping but breaks subsequent reads or writes) surfaces here as a
// non-nil error whose string form embeds `database disk image is malformed`
// — matched by IsMalformed.
//
// The pragma returns one or more rows; a clean database returns a single
// row of literal `ok`. Anything else is reported back to the caller verbatim
// (truncated to the first reported defect) so doctor's finding message
// names what SQLite saw.
func (i *Index) IntegrityCheck() error {
	if i == nil || i.db == nil {
		return fmt.Errorf("index: integrity_check: nil handle")
	}
	rows, err := i.db.Query("PRAGMA integrity_check")
	if err != nil {
		// Query itself failing on a SQLite corruption code is the
		// strongest signal — surface verbatim, wrap so IsMalformed
		// matches on the substring.
		return fmt.Errorf("index: integrity_check query: %w", err)
	}
	defer rows.Close()
	var first string
	for rows.Next() {
		var s string
		if scanErr := rows.Scan(&s); scanErr != nil {
			return fmt.Errorf("index: integrity_check scan: %w", scanErr)
		}
		if first == "" {
			first = s
		}
	}
	if first == "" {
		return fmt.Errorf("index: integrity_check: no rows returned")
	}
	if first == "ok" {
		return nil
	}
	// Any non-"ok" row is a defect. Embed the canonical SQLite phrase
	// so IsMalformed picks it up regardless of what integrity_check
	// printed for the specific page.
	return fmt.Errorf("index: database disk image is malformed: %s", first)
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

// Row is a denormalised view of one issues row plus its accept criteria,
// internal dependency edges, and external opaque-string deps.
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
	ClaimedAt    string
	ClosedAt     string
	ClosedReason string
	Accept       []string
	Deps         []Dep
	ExternalDeps []string
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
		"DELETE FROM issue_external_deps",
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
		{"DELETE FROM issues              WHERE id       = ?", id},
		{"DELETE FROM issue_accept        WHERE issue_id = ?", id},
		{"DELETE FROM issue_deps          WHERE issue_id = ?", id},
		{"DELETE FROM issue_external_deps WHERE issue_id = ?", id},
		{"DELETE FROM issue_meta          WHERE issue_id = ?", id},
		{"DELETE FROM fts                 WHERE issue_id = ?", id},
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
	claimedAt, _ := rendered["claimed_at"].(string)
	closedAt, _ := rendered["closed_at"].(string)
	closedReason, _ := rendered["closed_reason"].(string)
	priority := coerceInt(rendered["priority"])

	if _, err := tx.Exec(`
        INSERT INTO issues (
            id, title, description, status, priority, type, parent, assignee,
            created_at, claimed_at, closed_at, closed_reason, tombstoned
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
    `, id, title, description, status, priority, itype, parent, assignee,
		createdAt, claimedAt, closedAt, closedReason); err != nil {
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

	// Dependency edges. RenderState normalises "deps" to []map[string]string
	// regardless of whether state came from a live fold or a JSON-round-tripped
	// snapshot, so a single typed assertion is sufficient (see act-8c78).
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

	// External (opaque-ref) dependency entries. RenderState normalises
	// "external_deps" to []string for both live and post-snapshot state, so
	// the assertion is on a single canonical type.
	if refs, ok := rendered["external_deps"].([]string); ok {
		for _, ref := range refs {
			if _, err := tx.Exec(
				`INSERT INTO issue_external_deps (issue_id, ref) VALUES (?, ?)`,
				id, ref,
			); err != nil {
				return fmt.Errorf("index: insert external_dep %s->%s: %w", id, ref, err)
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

// BlocksOf returns the sorted list of issue ids whose deps[] contain
// (parent_id=parentID, edge_type=blocks) — i.e. the set of issues that
// are blocked by parentID. This is the reverse direction of the blocks
// edge: if "act-AAA is blocked by act-BBB" is the forward direction,
// then BlocksOf("act-BBB") returns ["act-AAA"].
//
// The query reads from the current index state without triggering a
// rebuild; callers that need guaranteed freshness should Rebuild before
// calling. Returns nil (not an error) if the index has no rows for this
// parent.
func (i *Index) BlocksOf(parentID string) ([]string, error) {
	rows, err := i.db.Query(
		`SELECT issue_id FROM issue_deps WHERE parent_id = ? AND edge_type = 'blocks' ORDER BY issue_id ASC`,
		parentID,
	)
	if err != nil {
		return nil, fmt.Errorf("index: blocks_of %s: %w", parentID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("index: blocks_of scan %s: %w", parentID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: blocks_of iter %s: %w", parentID, err)
	}
	return out, nil
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
               assignee, created_at, claimed_at, closed_at, closed_reason
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
               assignee, created_at, claimed_at, closed_at, closed_reason
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
		createdAt, claimedAt, closedAt, closedReason        sql.NullString
		priority                                            sql.NullInt64
	)
	if err := s.Scan(&r.ID, &title, &description, &status, &priority, &itype, &parent,
		&assignee, &createdAt, &claimedAt, &closedAt, &closedReason); err != nil {
		return Row{}, err
	}
	r.Title = title.String
	r.Description = description.String
	r.Status = status.String
	r.Type = itype.String
	r.Parent = parent.String
	r.Assignee = assignee.String
	r.CreatedAt = createdAt.String
	r.ClaimedAt = claimedAt.String
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

	// external deps (opaque refs)
	erows, err := i.db.Query(
		`SELECT ref FROM issue_external_deps WHERE issue_id = ? ORDER BY ref`,
		r.ID)
	if err != nil {
		return fmt.Errorf("index: load external_deps %s: %w", r.ID, err)
	}
	for erows.Next() {
		var ref string
		if err := erows.Scan(&ref); err != nil {
			erows.Close()
			return fmt.Errorf("index: scan external_dep %s: %w", r.ID, err)
		}
		r.ExternalDeps = append(r.ExternalDeps, ref)
	}
	erows.Close()
	return nil
}
