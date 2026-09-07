package index

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/aac/act/internal/fold"
)

// rebuildIncremental brings the index up to date by refolding only the issues
// whose ops subtree moved, instead of refolding the whole op log and rewriting
// every row (act-50d2e2).
//
// This is what the read AFTER A WRITE pays. act-43d11f made an unchanged store
// cheap, but any change at all invalidated the whole-tree key, so a store under
// active writes — a drain claiming and closing tickets — paid a full fold on
// every read interleaved with a write: 0.84-1.02s on a 4,317-op store where the
// work actually done was one issue's worth.
//
// It reports done=false, with a nil error, when the incremental path does not
// apply and the caller should fall back to a full Rebuild:
//
//   - the index carries no per-issue signatures (written before this table
//     existed, or deliberately withheld by a Rebuild that could not partition
//     the tree);
//   - an issue's subtree holds an op belonging to a different issue
//     (fold.ErrForeignOp), which is the one shape that breaks the per-issue
//     partition the whole approach rests on.
//
// Three properties are load-bearing, and each is the reason for a specific
// line below:
//
//   - Signatures are taken by the caller BEFORE any folding, so an op landing
//     during the fold is missing from the stored key and refolded next read.
//   - An issue whose subtree DISAPPEARED is deleted, not left behind from the
//     previous rows — that is the rolled-back-close shape act-fec192 cost us,
//     and a cache that only ever upserts would silently keep serving it.
//   - Every fold runs before the transaction opens, so a fold error leaves the
//     index exactly as it was rather than half-updated.
func (i *Index) rebuildIncremental(rootOps string, sigs fold.Signatures) (done bool, err error) {
	prev, err := i.readIssueSigs()
	if err != nil {
		return false, err
	}
	if len(prev) == 0 {
		return false, nil
	}

	var changed, removed []string
	for id, sig := range sigs.PerIssue {
		if prev[id] != sig {
			changed = append(changed, id)
		}
	}
	for id := range prev {
		if _, ok := sigs.PerIssue[id]; !ok {
			removed = append(removed, id)
		}
	}
	// Deterministic order so a failure reports the same issue run to run.
	sort.Strings(changed)
	sort.Strings(removed)

	// Fold every changed issue before touching the database, so an error
	// aborts with the index untouched.
	type folded struct {
		id    string
		state *fold.IssueState
		found bool
	}
	states := make([]folded, 0, len(changed))
	for _, id := range changed {
		st, found, ferr := fold.FoldIssueStrict(rootOps, id, fold.ApplyDispatch)
		if ferr != nil {
			if errors.Is(ferr, fold.ErrForeignOp) {
				return false, nil
			}
			return false, fmt.Errorf("index: incremental fold %s: %w", id, ferr)
		}
		states = append(states, folded{id: id, state: st, found: found})
	}

	tx, err := i.db.Begin()
	if err != nil {
		return false, fmt.Errorf("index: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range removed {
		if err := deleteIssueRowsTx(tx, id); err != nil {
			return false, err
		}
		if _, err := tx.Exec(`DELETE FROM index_issue_sig WHERE issue_id = ?`, id); err != nil {
			return false, fmt.Errorf("index: drop signature for %s: %w", id, err)
		}
	}

	for _, f := range states {
		if f.found {
			if err := upsertTx(tx, f.state); err != nil {
				return false, fmt.Errorf("index: incremental upsert %s: %w", f.id, err)
			}
		} else {
			// A directory that yielded no ops produces no row in a full
			// rebuild either, so clear whatever this issue had.
			if err := deleteIssueRowsTx(tx, f.id); err != nil {
				return false, err
			}
		}
		if err := putIssueSigTx(tx, f.id, sigs.PerIssue[f.id]); err != nil {
			return false, err
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO index_state (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		opsBuildKeyRow, buildKey(sigs.Tree),
	); err != nil {
		return false, fmt.Errorf("index: record build key: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("index: commit incremental rebuild: %w", err)
	}
	return true, nil
}

// readIssueSigs returns the per-issue ops signatures recorded by the last
// build, or an empty map when there are none.
func (i *Index) readIssueSigs() (map[string]string, error) {
	rows, err := i.db.Query(`SELECT issue_id, sig FROM index_issue_sig`)
	if err != nil {
		return nil, fmt.Errorf("index: read issue signatures: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, sig string
		if err := rows.Scan(&id, &sig); err != nil {
			return nil, fmt.Errorf("index: scan issue signature: %w", err)
		}
		out[id] = sig
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: iterate issue signatures: %w", err)
	}
	return out, nil
}

// putIssueSigTx records one issue's ops-subtree signature.
func putIssueSigTx(tx *sql.Tx, id, sig string) error {
	if _, err := tx.Exec(
		`INSERT INTO index_issue_sig (issue_id, sig) VALUES (?, ?)
		 ON CONFLICT(issue_id) DO UPDATE SET sig = excluded.sig`,
		id, sig,
	); err != nil {
		return fmt.Errorf("index: record signature for %s: %w", id, err)
	}
	return nil
}
