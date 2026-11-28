package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for `act show` naming an answered ask (act-b9adc9).

// askBatchStandIn puts a stand-in `ask` on PATH for a binary-level test.
// It answers, only when run in site (else it fails like ask with no
// store):
//
//	ask show <id> --json   cats <fixtures>/<id>.json, exit 3 if absent
//	ask list --all --json  a JSON array of every fixture, in name order
//
// Each invocation appends its argv to <fixtures>/../calls.log, so a test
// can count execs. writeItem writes a fixture in ask's --json item shape.
func askBatchStandIn(t *testing.T, site string) (writeItem func(id, status, note, resolvedAt string), callLog string) {
	t.Helper()
	base := t.TempDir()
	fixtures := filepath.Join(base, "items")
	binDir := filepath.Join(base, "bin")
	for _, d := range []string{fixtures, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callLog = filepath.Join(base, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"$ASKBATCH_CALLS\"\n" +
		"[ \"$(pwd -P)\" = \"$ASKBATCH_SITE\" ] || { echo 'ask not initialized' >&2; exit 5; }\n" +
		"if [ \"$1\" = list ] && [ \"$2\" = --all ] && [ \"$3\" = --json ]; then\n" +
		"  printf '['; sep=''\n" +
		"  for f in \"$ASKBATCH_FIXTURES\"/*.json; do [ -f \"$f\" ] || continue; printf '%s' \"$sep\"; cat \"$f\"; sep=','; done\n" +
		"  printf ']\\n'; exit 0\n" +
		"fi\n" +
		"[ \"$1\" = show ] && [ \"$3\" = --json ] || exit 2\n" +
		"[ -f \"$ASKBATCH_FIXTURES/$2.json\" ] || { echo \"id $2 not found\" >&2; exit 3; }\n" +
		"cat \"$ASKBATCH_FIXTURES/$2.json\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "ask"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	realSite, err := filepath.EvalSymlinks(site)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASKBATCH_SITE", realSite)
	t.Setenv("ASKBATCH_FIXTURES", fixtures)
	t.Setenv("ASKBATCH_CALLS", callLog)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeItem = func(id, status, note, resolvedAt string) {
		if err := os.WriteFile(filepath.Join(fixtures, id+".json"), []byte(askFreedItem(id, status, note, resolvedAt)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return writeItem, callLog
}

// TestDocClaim_AskFreed_ShowNamesFreeingAsk pins the docs/spec.md `act
// show` claim at the binary boundary: while the ask is open, neither the
// text nor the --json output says it was answered; once resolved, the
// text marks the dep line and prints the answer, --json carries freed_by
// with the resolution, and the act_show MCP tool does the same. The dep
// stays listed under external_deps throughout.
func TestDocClaim_AskFreed_ShowNamesFreeingAsk(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")
	writeItem, _ := askBatchStandIn(t, site)

	createOut, _ := mustRunAct(t, site, 0, "create", "needs the key", "--json")
	id := pickIDFromJSON(t, createOut)
	mustRunAct(t, site, 0, "dep", "add", id, "--external", "ask-1a2b")
	mustRunAct(t, site, 0, "dep", "add", id, "--external", "linear:ENG-9")

	type showJSON struct {
		ExternalDeps []string   `json:"external_deps"`
		FreedBy      []AskFreed `json:"freed_by"`
	}
	readJSON := func() (showJSON, string) {
		out, _ := mustRunAct(t, site, 0, "show", id, "--json")
		var s showJSON
		if err := json.Unmarshal([]byte(out), &s); err != nil {
			t.Fatalf("show --json: %v\n%s", err, out)
		}
		return s, out
	}

	writeItem("ask-1a2b", "open", "", "")
	text, _ := mustRunAct(t, site, 0, "show", id)
	if !strings.Contains(text, "external_dep: ask-1a2b\n") || strings.Contains(text, "freed") || strings.Contains(text, "answer:") {
		t.Errorf("show text with the ask open must list the dep plainly:\n%s", text)
	}
	s, raw := readJSON()
	if strings.Contains(raw, "freed_by") {
		t.Errorf("show --json with the ask open carries freed_by:\n%s", raw)
	}
	if len(s.ExternalDeps) != 2 {
		t.Errorf("external_deps = %v, want both refs", s.ExternalDeps)
	}

	writeItem("ask-1a2b", "resolved", "Use the staging key.\nIt is in 1Password.", askFreedResolvedAt)
	text, _ = mustRunAct(t, site, 0, "show", id)
	for _, want := range []string{
		"external_dep: ask-1a2b [freed: ask-1a2b was resolved; no longer blocks]\n",
		"  answer: Use the staging key.\n",
		"          It is in 1Password.\n",
		"external_dep: linear:ENG-9\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("show text after resolve missing %q:\n%s", want, text)
		}
	}
	s, raw = readJSON()
	want := AskFreed{Ref: "ask-1a2b", AskID: "ask-1a2b", Status: "resolved", Resolution: "Use the staging key.\nIt is in 1Password.", ResolvedAt: askFreedResolvedAt}
	if len(s.FreedBy) != 1 || s.FreedBy[0] != want {
		t.Errorf("show --json freed_by = %+v, want [%+v]\n%s", s.FreedBy, want, raw)
	}
	if len(s.ExternalDeps) != 2 || s.ExternalDeps[0] != "ask-1a2b" {
		t.Errorf("external_deps after resolve = %v, want the ask ref still attached", s.ExternalDeps)
	}

	cmd := exec.Command(actBinaryPath, "mcp")
	cmd.Dir = site
	cmd.Stdin = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_show","arguments":{"id":"` + id + `"}}}` + "\n")
	mcpOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("act mcp: %v\n%s", err, mcpOut)
	}
	for _, w := range []string{`freed_by`, `staging key`} {
		if !strings.Contains(string(mcpOut), w) {
			t.Errorf("act_show result missing %q:\n%s", w, mcpOut)
		}
	}
}
