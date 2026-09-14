package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// act-841052: op validation range-checked create.priority but not
// update_field priority or type, so `act import` accepted values `act update`
// refuses. docs/spec.md: priority is "int 0..3 inclusive" and "outside 0..3
// is a hard error at write time"; type is one of task|bug|epic|chore.

// TestCapaudit_PriorityMaxRoundTripsThroughImport sets priority to the max (3)
// on create and on update, plus a type change, and imports the op log into a
// fresh store.
func TestCapaudit_PriorityMaxRoundTripsThroughImport(t *testing.T) {
	src := bootstrapLoopRepo(t)
	if _, stderr, code := runActIn(t, src, "create", "created at max priority", "-p", "3", "--json"); code != 0 {
		t.Fatalf("act create -p 3: exit %d; stderr=%s", code, stderr)
	}
	id := createIssue(t, src, "updated to max priority")
	if _, stderr, code := runActIn(t, src, "update", id, "--priority", "3", "--type", "chore", "--json"); code != 0 {
		t.Fatalf("act update --priority 3 --type chore: exit %d; stderr=%s", code, stderr)
	}

	jsonl := filepath.Join(t.TempDir(), "ops.jsonl")
	if err := os.WriteFile(jsonl, []byte(titlecapOpsJSONL(t, src)), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	dst := bootstrapLoopRepo(t)
	if out, stderr, code := runActIn(t, dst, "import", jsonl, "--json", "--no-commit"); code != 0 {
		t.Fatalf("act import: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	showOut, stderr, code := runActIn(t, dst, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show in import target: exit %d; stderr=%s", code, stderr)
	}
	for _, want := range []string{`"priority":3`, `"type":"chore"`} {
		if !strings.Contains(strings.ReplaceAll(showOut, " ", ""), want) {
			t.Errorf("imported issue missing %s; show=%s", want, showOut)
		}
	}
}

// TestCapaudit_PriorityOverMaxRejected asserts priority 4 and an unknown type
// are exit 2 on the write path, and that an op log carrying them is refused by
// `act import` (the op-layer check this ticket added).
func TestCapaudit_PriorityOverMaxRejected(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	if _, stderr, code := runActIn(t, dir, "create", "p4", "-p", "4"); code != 2 {
		t.Fatalf("act create -p 4: exit %d, want 2; stderr=%s", code, stderr)
	}
	id := createIssue(t, dir, "short")
	if _, stderr, code := runActIn(t, dir, "update", id, "--priority", "4"); code != 2 {
		t.Fatalf("act update --priority 4: exit %d, want 2; stderr=%s", code, stderr)
	}

	// Build an op log whose update_field carries an out-of-range value by
	// rewriting a legitimate one, then import it.
	for _, tc := range []struct{ flag, val, from, to string }{
		{"--priority", "3", `"value":3`, `"value":4`},
		{"--type", "chore", `"value":"chore"`, `"value":"feature"`},
	} {
		src := bootstrapLoopRepo(t)
		sid := createIssue(t, src, "import probe")
		if _, stderr, code := runActIn(t, src, "update", sid, tc.flag, tc.val, "--json"); code != 0 {
			t.Fatalf("act update %s %s: exit %d; stderr=%s", tc.flag, tc.val, code, stderr)
		}
		log := titlecapOpsJSONL(t, src)
		if !strings.Contains(log, tc.from) {
			t.Fatalf("op log has no %s to rewrite:\n%s", tc.from, log)
		}
		jsonl := filepath.Join(t.TempDir(), "ops.jsonl")
		if err := os.WriteFile(jsonl, []byte(strings.Replace(log, tc.from, tc.to, 1)), 0o644); err != nil {
			t.Fatalf("write jsonl: %v", err)
		}
		dst := bootstrapLoopRepo(t)
		if _, _, code := runActIn(t, dst, "import", jsonl, "--json", "--no-commit"); code == 0 {
			t.Errorf("act import accepted update_field %s", tc.to)
		}
	}
}
