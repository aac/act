package cli

import (
	"strings"
	"testing"
)

// TestDocClaim_DescriptionCap_Inline pins docs/spec.md "Length caps" for
// description on the inline write paths (act-993498): `act create
// --description` and `act update --description` accept exactly 1048576
// bytes and reject 1048577 with an error naming the cap. Driven through
// RunCreate/RunUpdate — what cmd/act hands the inline flag to — because a
// 1 MiB argv token exceeds the OS argument limit (ARG_MAX is 1 MiB on
// macOS, MAX_ARG_STRLEN 128 KiB on Linux), so no subprocess can carry it.
// Before the cap lived in the op validators these paths had no cap at all.
func TestDocClaim_DescriptionCap_Inline(t *testing.T) {
	root := makeCreateRepo(t)
	atCap := strings.Repeat("c", 1048576)
	overCap := atCap + "c"
	const wantMsg = "length 1048577 > 1048576 bytes"

	out, code := RunCreate(root, CreateOptions{Title: "at cap", Type: "task", Description: atCap})
	if code != 0 {
		t.Fatalf("create with a 1048576-byte description: code=%d out=%+v", code, out)
	}
	id := out.(CreateResult).ID

	out, code = RunCreate(root, CreateOptions{Title: "over cap", Type: "task", Description: overCap})
	if code == 0 {
		t.Fatalf("create with a 1048577-byte description accepted: %+v", out)
	}
	if e, _ := out.(CreateErrorOutput); !strings.Contains(e.Message, "create.description "+wantMsg) {
		t.Errorf("create over-cap error does not name the cap: %+v", out)
	}

	out, code = RunUpdate(root, UpdateOptions{ID: id, Description: &overCap})
	if code == 0 {
		t.Fatalf("update with a 1048577-byte description accepted: %+v", out)
	}
	if e, _ := out.(UpdateErrorOutput); !strings.Contains(e.Message, "update_field.description "+wantMsg) {
		t.Errorf("update over-cap error does not name the cap: %+v", out)
	}
	shorter := atCap[:10]
	if out, code = RunUpdate(root, UpdateOptions{ID: id, Description: &shorter}); code != 0 {
		t.Fatalf("update with a short description: code=%d out=%+v", code, out)
	}
	if out, code = RunUpdate(root, UpdateOptions{ID: id, Description: &atCap}); code != 0 {
		t.Fatalf("update with a 1048576-byte description: code=%d out=%+v", code, out)
	}
}
