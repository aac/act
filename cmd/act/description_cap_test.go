package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// TestDocClaim_DescriptionCap_FileAndAppendFlags pins docs/spec.md "Length
// caps" for description at the `act` argv boundary (act-993498):
// --description-file (create and update) and --description-append-file
// accept exactly 1048576 bytes and reject 1048577 naming the cap, and an
// inline --description-append is capped on its MERGED result — the
// description the op would write — not on the fragment. The numbers are the
// spec's, as literals, so moving op.MaxDescriptionLen alone fails here.
func TestDocClaim_DescriptionCap_FileAndAppendFlags(t *testing.T) {
	dir := blocksSite(t)
	atCapFile := writeTempFile(t, "at.md", strings.Repeat("f", 1048576))
	overCapFile := writeTempFile(t, "over.md", strings.Repeat("f", 1048577))
	const fileCapMsg = "1048576-byte description cap"

	// create --description-file
	if out, stderr, code := runActIn(t, dir, "create", "file at cap", "--description-file", atCapFile, "--json"); code != 0 {
		t.Fatalf("create --description-file at 1048576 bytes: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	out, _, code := runActIn(t, dir, "create", "file over cap", "--description-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("create --description-file at 1048577 bytes: exit %d (want 2), out=%s (want %q)", code, out, fileCapMsg)
	}

	id := createBlocksIssue(t, dir, "cap target")

	// update --description-file
	out, _, code = runActIn(t, dir, "update", id, "--description-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("update --description-file at 1048577 bytes: exit %d (want 2), out=%s", code, out)
	}
	// --description-append-file
	out, _, code = runActIn(t, dir, "update", id, "--description-append-file", overCapFile, "--json")
	if code != 2 || !strings.Contains(out, fileCapMsg) {
		t.Fatalf("--description-append-file at 1048577 bytes: exit %d (want 2), out=%s", code, out)
	}

	// Inline --description-append, capped on the merged result: 1048572
	// existing bytes + "\n\n" + "xy" is exactly 1048576.
	seed := writeTempFile(t, "seed.md", strings.Repeat("s", 1048572))
	if out, stderr, code := runActIn(t, dir, "update", id, "--description-file", seed, "--json"); code != 0 {
		t.Fatalf("seed description: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	if out, stderr, code := runActIn(t, dir, "update", id, "--description-append", "xy", "--json"); code != 0 {
		t.Fatalf("--description-append landing on exactly 1048576 bytes: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	out, _, code = runActIn(t, dir, "update", id, "--description-append", "q", "--json")
	if code == 0 {
		t.Fatalf("--description-append past 1048576 bytes accepted: %s", out)
	}
	var env struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("parse error envelope: %v\n%s", err, out)
	}
	if want := "merged description length 1048579 > 1048576 bytes"; !strings.Contains(env.Message, want) {
		t.Fatalf("--description-append over-cap error does not name the merged length and cap (%q): %s", want, env.Message)
	}
}

// TestDocClaim_DescriptionCap_OneExitCode pins docs/spec.md "Length caps":
// an over-cap description is rejected the same way on every `act create` /
// `act update` path — exit 2, error "bad_flag", the code and key the title
// cap already uses (act-940461). Before, --description-file and
// --description-append-file said exit 2 bad_flag while inline --description
// and an over-cap merged --description-append fell through to the op
// validator's exit 1 payload_invalid. The file paths run through the binary;
// the inline paths through cli.RunCreate/RunUpdate, which is what cmd/act
// hands the inline flags to (a 1 MiB argv token exceeds ARG_MAX).
func TestDocClaim_DescriptionCap_OneExitCode(t *testing.T) {
	dir := blocksSite(t)
	overCap := strings.Repeat("f", 1048577)
	overCapFile := writeTempFile(t, "over.md", overCap)
	const wantCode, wantKey = 2, "bad_flag"

	errKey := func(label, out string) string {
		t.Helper()
		var env struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("%s: parse error envelope: %v\n%s", label, err, out)
		}
		return env.Error
	}
	check := func(label string, code int, key string) {
		t.Helper()
		if code != wantCode || key != wantKey {
			t.Errorf("%s: exit %d error %q, want exit %d error %q", label, code, key, wantCode, wantKey)
		}
	}

	out, _, code := runActIn(t, dir, "create", "file over cap", "--description-file", overCapFile, "--json")
	check("create --description-file", code, errKey("create --description-file", out))

	cout, code := cli.RunCreate(dir, cli.CreateOptions{Title: "inline over cap", Type: "task", Description: overCap})
	ce, _ := cout.(cli.CreateErrorOutput)
	check("RunCreate inline description", code, ce.Error)

	id := createBlocksIssue(t, dir, "cap target")

	out, _, code = runActIn(t, dir, "update", id, "--description-file", overCapFile, "--json")
	check("update --description-file", code, errKey("update --description-file", out))

	out, _, code = runActIn(t, dir, "update", id, "--description-append-file", overCapFile, "--json")
	check("update --description-append-file", code, errKey("update --description-append-file", out))

	uout, code := cli.RunUpdate(dir, cli.UpdateOptions{ID: id, Description: &overCap})
	ue, _ := uout.(cli.UpdateErrorOutput)
	check("RunUpdate inline description", code, ue.Error)

	// --description-append whose MERGED result is over the cap, though the
	// fragment alone is tiny: 1048576 existing bytes + "\n\n" + "q".
	atCap := strings.Repeat("s", 1048576)
	if uout, code = cli.RunUpdate(dir, cli.UpdateOptions{ID: id, Description: &atCap}); code != 0 {
		t.Fatalf("seed a 1048576-byte description: code=%d out=%+v", code, uout)
	}
	frag := "q"
	uout, code = cli.RunUpdate(dir, cli.UpdateOptions{ID: id, DescriptionAppend: &frag})
	ue, _ = uout.(cli.UpdateErrorOutput)
	check("RunUpdate --description-append merged over cap", code, ue.Error)
	out, _, code = runActIn(t, dir, "update", id, "--description-append", "q", "--json")
	check("update --description-append merged over cap", code, errKey("update --description-append", out))
}
