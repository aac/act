package op

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDispatchByVersionRegistered installs a stub for op_version=1 and
// verifies the dispatch returns it. Real wiring lives in the fold package
// init, but the op-package test sets its own stub so the test does not
// depend on import side effects.
func TestDispatchByVersionRegistered(t *testing.T) {
	prev, hadPrev := OpVersionRegistry[1]
	t.Cleanup(func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		if hadPrev {
			OpVersionRegistry[1] = prev
		} else {
			delete(OpVersionRegistry, 1)
		}
	})
	called := false
	RegisterOpVersion(1, func(any, Envelope, []byte) error {
		called = true
		return nil
	})
	fn, err := DispatchByVersion(1)
	if err != nil {
		t.Fatalf("DispatchByVersion(1): unexpected error: %v", err)
	}
	if fn == nil {
		t.Fatalf("DispatchByVersion(1): want non-nil apply function")
	}
	if err := fn(nil, Envelope{}, nil); err != nil {
		t.Fatalf("apply: unexpected error: %v", err)
	}
	if !called {
		t.Fatalf("apply: registered function was not invoked")
	}
}

func TestDispatchByVersionUnknown(t *testing.T) {
	if _, err := DispatchByVersion(99); err == nil {
		t.Fatalf("DispatchByVersion(99): want error, got nil")
	}
}

// TestReadMaxOpVersionMixed populates a tmp dir with op files at versions 1
// and 2 and confirms the max is reported.
func TestReadMaxOpVersionMixed(t *testing.T) {
	dir := t.TempDir()
	writeOp := func(rel string, opVersion int) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(map[string]any{"op_version": opVersion})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeOp("act-aaaa/2026-01/a.json", 1)
	writeOp("act-aaaa/2026-01/b.json", 2)
	writeOp("act-bbbb/2026-01/c.json", 1)

	max, err := ReadMaxOpVersion(dir)
	if err != nil {
		t.Fatalf("ReadMaxOpVersion: %v", err)
	}
	if max != 2 {
		t.Fatalf("ReadMaxOpVersion: got %d, want 2", max)
	}
}

func TestReadMaxOpVersionMissingDir(t *testing.T) {
	max, err := ReadMaxOpVersion(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("ReadMaxOpVersion: unexpected error: %v", err)
	}
	if max != 0 {
		t.Fatalf("ReadMaxOpVersion(missing): got %d, want 0", max)
	}
}

// TestRunMigrateMissingMigration confirms that with the empty Migrations
// registry, RunMigrate returns the migration_not_found error envelope.
func TestRunMigrateMissingMigration(t *testing.T) {
	// Snapshot and clear Migrations for the duration of the test so future
	// registrations do not change the assertion.
	prev := Migrations
	Migrations = nil
	t.Cleanup(func() { Migrations = prev })

	repo := t.TempDir()
	out, code := RunMigrate(repo, 1, 2)
	if code != 5 {
		t.Fatalf("RunMigrate: code=%d, want 5", code)
	}
	merr, ok := out.(MigrateError)
	if !ok {
		t.Fatalf("RunMigrate: out type %T, want MigrateError", out)
	}
	if merr.Error != "migration_not_found" {
		t.Fatalf("RunMigrate: error=%q, want migration_not_found", merr.Error)
	}
}

func TestRunMigrateBadInput(t *testing.T) {
	repo := t.TempDir()
	cases := []struct {
		from, to int
	}{
		{0, 1},
		{1, 1},
		{2, 1},
		{-1, 2},
	}
	for _, c := range cases {
		out, code := RunMigrate(repo, c.from, c.to)
		if code != 2 {
			t.Errorf("RunMigrate(%d,%d): code=%d, want 2", c.from, c.to, code)
		}
		if _, ok := out.(MigrateError); !ok {
			t.Errorf("RunMigrate(%d,%d): out type %T, want MigrateError", c.from, c.to, out)
		}
	}
}

func TestRunMigrateNoOpsRoot(t *testing.T) {
	// With a registered migration but no .act/ops directory present,
	// RunMigrate succeeds with zero counts (no work to do).
	prev := Migrations
	Migrations = []Migration{
		{
			FromVersion: 1,
			ToVersion:   2,
			Description: "noop",
			Transform:   func(env Envelope) ([]Envelope, error) { return nil, nil },
		},
	}
	t.Cleanup(func() { Migrations = prev })

	repo := t.TempDir()
	out, code := RunMigrate(repo, 1, 2)
	if code != 0 {
		t.Fatalf("RunMigrate: code=%d, want 0; out=%+v", code, out)
	}
	mout, ok := out.(MigrateOutput)
	if !ok {
		t.Fatalf("RunMigrate: out type %T, want MigrateOutput", out)
	}
	if mout.MigratedIssues != 0 || mout.WroteOps != 0 {
		t.Fatalf("RunMigrate: got %+v, want zeros", mout)
	}
}
