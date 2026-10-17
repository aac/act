package index

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// act-cdc0db: index.db is shared by every act process in a checkout — reads,
// EnsureCurrent rebuilds, and the post-write RefreshIndexForIssue all open it
// with no cross-process lock of act's own. A handle that fails SQLITE_BUSY the
// instant another process holds a lock turns ordinary concurrency into
// `index_update_failed` (seen on CI in TestWriteLockStress). These tests hold
// a lock on a second connection, release it shortly, and require the index to
// wait for it rather than fail.

// holdLock opens a raw connection to dbPath, runs begin (e.g. "BEGIN
// EXCLUSIVE") plus a write so the lock is really taken, and rolls back after
// hold. It returns once the lock is held.
func holdLock(t *testing.T, dbPath, begin string, hold time.Duration) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatalf("raw conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS lock_probe(x)"); err != nil {
		t.Fatalf("raw create: %v", err)
	}
	if _, err := conn.ExecContext(ctx, begin); err != nil {
		t.Fatalf("raw %s: %v", begin, err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO lock_probe VALUES (1)"); err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	go func() {
		time.Sleep(hold)
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_ = conn.Close()
	}()
}

// TestOpen_WaitsForConcurrentLock: Open's journal_mode(WAL) pragma on a db
// another process has locked must wait for the lock, not fail SQLITE_BUSY.
func TestOpen_WaitsForConcurrentLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	holdLock(t, dbPath, "BEGIN EXCLUSIVE", 300*time.Millisecond)
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open while another connection held a lock for 300ms: %v", err)
	}
	_ = idx.Close()
}

// TestWrite_WaitsForConcurrentWriter: a write through an open Index while
// another process holds the WAL write lock must wait for it.
func TestWrite_WaitsForConcurrentWriter(t *testing.T) {
	idx, dir := newTempIndex(t)
	dbPath := filepath.Join(dir, "index.db")
	holdLock(t, dbPath, "BEGIN IMMEDIATE", 300*time.Millisecond)
	if _, err := idx.DB().Exec("INSERT INTO index_state(key, value) VALUES ('busy_probe', '1')"); err != nil {
		t.Fatalf("write while another connection held the write lock for 300ms: %v", err)
	}
}

// TestTx_WaitsForConcurrentWriter: a transaction (the shape Upsert and
// Rebuild use) begun while another process holds the write lock must wait
// for it, at BEGIN or at its first write.
func TestTx_WaitsForConcurrentWriter(t *testing.T) {
	idx, dir := newTempIndex(t)
	dbPath := filepath.Join(dir, "index.db")
	holdLock(t, dbPath, "BEGIN IMMEDIATE", 300*time.Millisecond)
	tx, err := idx.DB().Begin()
	if err != nil {
		t.Fatalf("begin while another connection held the write lock: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("INSERT INTO index_state(key, value) VALUES ('busy_probe', '1')"); err != nil {
		t.Fatalf("tx write while another connection held the write lock: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
