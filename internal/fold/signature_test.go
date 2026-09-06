package fold

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSigOp drops a file into a nested ops-shaped path and returns it.
func writeSigOp(t *testing.T, opsRoot, issue, month, name, body string) string {
	t.Helper()
	dir := filepath.Join(opsRoot, issue, month)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func sigOf(t *testing.T, opsRoot string) string {
	t.Helper()
	s, err := OpsSignature(opsRoot)
	if err != nil {
		t.Fatalf("OpsSignature(%s): %v", opsRoot, err)
	}
	if s == "" {
		t.Fatalf("OpsSignature(%s) returned an empty signature", opsRoot)
	}
	return s
}

func TestOpsSignature_StableAcrossCalls(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "ops")
	writeSigOp(t, ops, "act-aaaa", "2026-04", "a-create.json", `{"op":"create"}`)
	writeSigOp(t, ops, "act-bbbb", "2026-05", "b-create.json", `{"op":"create"}`)

	first := sigOf(t, ops)
	for i := 0; i < 3; i++ {
		if got := sigOf(t, ops); got != first {
			t.Fatalf("signature changed on call %d with no change on disk: %s != %s", i, got, first)
		}
	}
}

func TestOpsSignature_MissingRootIsStable(t *testing.T) {
	root := t.TempDir()
	a := sigOf(t, filepath.Join(root, "nope"))
	b := sigOf(t, filepath.Join(root, "also-nope"))
	if a != b {
		t.Fatalf("missing ops roots produced different signatures: %s != %s", a, b)
	}

	ops := filepath.Join(root, "ops")
	writeSigOp(t, ops, "act-aaaa", "2026-04", "a-create.json", `{"op":"create"}`)
	if got := sigOf(t, ops); got == a {
		t.Fatalf("a populated tree hashed the same as a missing one: %s", got)
	}
}

func TestOpsSignature_ChangesWhenAnOpIsAdded(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "ops")
	writeSigOp(t, ops, "act-aaaa", "2026-04", "a-create.json", `{"op":"create"}`)
	before := sigOf(t, ops)

	writeSigOp(t, ops, "act-aaaa", "2026-04", "a-close.json", `{"op":"close"}`)
	if after := sigOf(t, ops); after == before {
		t.Fatal("signature unchanged after an op was appended")
	}
}

func TestOpsSignature_ChangesWhenAnOpIsRemoved(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "ops")
	writeSigOp(t, ops, "act-aaaa", "2026-04", "a-create.json", `{"op":"create"}`)
	closed := writeSigOp(t, ops, "act-aaaa", "2026-04", "a-close.json", `{"op":"close"}`)
	before := sigOf(t, ops)

	// The act-fec192 shape: a close op is rolled back off disk.
	if err := os.Remove(closed); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if after := sigOf(t, ops); after == before {
		t.Fatal("signature unchanged after an op was removed")
	}
}

// TestOpsSignature_ChangesWhenAnOpIsRewritten covers the case the metadata
// design is weakest on: same path, different bytes. A rewrite changes size
// or mtime (here, both), so the signature moves.
func TestOpsSignature_ChangesWhenAnOpIsRewritten(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "ops")
	p := writeSigOp(t, ops, "act-aaaa", "2026-04", "a-create.json", `{"op":"create","title":"one"}`)
	before := sigOf(t, ops)

	// Force a distinct mtime even on a filesystem with coarse resolution,
	// so this test measures the signature rather than the clock.
	if err := os.WriteFile(p, []byte(`{"op":"create","title":"a different title"}`), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if after := sigOf(t, ops); after == before {
		t.Fatal("signature unchanged after an op file was rewritten in place")
	}
}

// TestOpsSignature_SameContentDifferentCreationOrder pins that the signature
// is a property of the tree's contents, not of the order the files were made in.
func TestOpsSignature_SameContentDifferentCreationOrder(t *testing.T) {
	mk := func(order []string) string {
		ops := filepath.Join(t.TempDir(), "ops")
		for _, name := range order {
			writeSigOp(t, ops, "act-"+name, "2026-04", name+"-create.json", `{"op":"create"}`)
		}
		// Normalise mtimes so only the path/size components differ.
		stamp := time.Unix(1700000000, 0)
		_ = filepath.Walk(ops, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			return os.Chtimes(p, stamp, stamp)
		})
		return sigOf(t, ops)
	}
	a := mk([]string{"aaaa", "bbbb", "cccc"})
	b := mk([]string{"cccc", "aaaa", "bbbb"})
	if a != b {
		t.Fatalf("identical trees built in different order hashed differently:\n%s\n%s", a, b)
	}
}
