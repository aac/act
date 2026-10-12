package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wfxClaimFieldFlags is every `act update` flag that writes a field op, each
// with a value the flag parser accepts. `--claim` writes only its own op, so
// each of these next to `--claim` has nowhere to go.
func wfxClaimFieldFlags(t *testing.T, other string) [][]string {
	t.Helper()
	body := filepath.Join(t.TempDir(), "wfx-body.txt")
	if err := os.WriteFile(body, []byte("wfx body"), 0o644); err != nil {
		t.Fatalf("write %s: %v", body, err)
	}
	return [][]string{
		{"--machine", "wfx-box"}, {"--machine", ""},
		{"--priority", "1"}, {"--title", "x"}, {"--description", "x"}, {"--accept", "x"}, {"--type", "bug"},
		{"--status", "open"}, {"--parent", other}, {"--assignee", "x"}, {"--description-file", body},
		{"--description-append", "x"}, {"--description-append-file", body}, {"--accept-add", "x"},
		{"--accept-rm", "0"}, {"--dep-rm", other}, {"--ext-rm", "wfx:ref"},
	}
}

// TestDocClaim_Spec_UpdateClaimRefusesEveryFieldFlag asserts the sentence
// under the claim-win example of the `act update` section: `--claim` next to
// any field flag — `--machine` included — is exit 2 `bad_flag` naming the
// flag, and the refused call neither claims the issue nor changes the field
// (act-fcf9ca: `--claim --machine X` used to print the claim-win result, exit
// 0, and drop the label).
func TestDocClaim_Spec_UpdateClaimRefusesEveryFieldFlag(t *testing.T) {
	dir := sp6Site(t)
	other := createIssue(t, dir, "wfx other")
	for _, flag := range wfxClaimFieldFlags(t, other) {
		id := createIssue(t, dir, "wfx claim "+flag[0])
		before := sp8Show(t, dir, id)
		out, _ := pass5Run(t, dir, 2, "bad_flag", append([]string{"update", "--json", "--claim", id}, flag...)...)
		name := flag[0]
		if strings.HasSuffix(name, "-file") {
			// The file flags are read into the flag they are the file form of.
			name = strings.TrimSuffix(name, "-file")
		}
		if !strings.Contains(out, "--claim is mutually exclusive with: "+name) {
			t.Errorf("act update --claim %s %q: want the mutually-exclusive message naming %s\n%s", flag[0], flag[1], name, out)
		}
		after := sp8Show(t, dir, id)
		if st := sp8Str(after["status"]); st != "open" {
			t.Errorf("act update --claim %s %q left the issue %q, want open (not claimed)", flag[0], flag[1], st)
		}
		for _, key := range []string{"machine", "priority", "title", "description", "accept", "type", "parent", "assignee"} {
			if string(before[key]) != string(after[key]) {
				t.Errorf("act update --claim %s %q changed %s from %s to %s", flag[0], flag[1], key, before[key], after[key])
			}
		}
	}

	// The claim on its own still works, and a pin set beforehand survives it.
	id := createIssue(t, dir, "wfx pinned then claimed", "--machine", "wfx-box")
	pass5Run(t, dir, 0, "", "update", "--json", "--claim", id)
	if shown := sp8Show(t, dir, id); sp8Str(shown["status"]) != "in_progress" || sp8Str(shown["machine"]) != "wfx-box" {
		t.Errorf("after act update --claim on a pinned issue: status %s, machine %s; want in_progress and wfx-box", shown["status"], shown["machine"])
	}
}
