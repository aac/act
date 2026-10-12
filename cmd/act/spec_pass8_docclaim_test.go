package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doc-claim cover for the eighth stale pass over docs/spec.md (act-0c1c31),
// asserted against the real binary. It builds on the sixth and seventh
// passes' helpers (spec_pass6_docclaim_test.go, spec_pass7_docclaim_test.go).
//
// What was stale: 22 flags a Synopsis named had no flag bullet; `act init`
// was said to always emit JSON and to exit 2 on an existing tracker; three
// passages showed a `"kind"` key the binary never writes; the init, list and
// search JSON examples showed keys the binary does not emit; the error
// table's message column quoted wording the binary does not print; the
// `--sort` bullet named only the `:asc`/`:desc` suffix; and `act help errors`
// and the error table listed fewer codes than the binary emits.

// sp8ListIDs runs `act list --json <args>` and returns the ids in order.
func sp8ListIDs(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, append([]string{"list", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("act list --json %v: exit %d\nstdout=%s\nstderr=%s", args, code, out, stderr)
	}
	var res struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("act list --json %v: %v\n%s", args, err, out)
	}
	ids := []string{}
	for _, i := range res.Issues {
		ids = append(ids, i.ID)
	}
	return ids
}

// TestDocClaim_Spec_ListSortSyntax asserts the `--sort` bullet of the `act
// list` section: keys are comma-separated; a bare key is ascending; `-key`
// and `key:desc` are descending; `+key` and `key:asc` are the bare key; the
// default is `priority,-created_at`; `id` ascending is the last key unless
// the keys name `id`; an unknown field, an unknown direction or an empty key
// is exit 2.
func TestDocClaim_Spec_ListSortSyntax(t *testing.T) {
	dir := sp6Site(t)
	// Three issues: priorities 2, 1, 1, created in that order.
	p2 := createIssue(t, dir, "sp8 p2", "--priority", "2")
	p1old := createIssue(t, dir, "sp8 p1 old", "--priority", "1")
	p1new := createIssue(t, dir, "sp8 p1 new", "--priority", "1")
	lo, hi := p1old, p1new // the two p1 ids in id order
	if lo > hi {
		lo, hi = hi, lo
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: order %v, want %v", what, got, want)
		}
	}

	// Default is priority,-created_at: p1 before p2, newest first inside p1.
	eq("no --sort", sp8ListIDs(t, dir), p1new, p1old, p2)
	eq("--sort priority,-created_at", sp8ListIDs(t, dir, "--sort", "priority,-created_at"), p1new, p1old, p2)

	// A bare key is ascending; `id` is the added last key.
	eq("--sort priority", sp8ListIDs(t, dir, "--sort", "priority"), lo, hi, p2)
	eq("--sort created_at", sp8ListIDs(t, dir, "--sort", "created_at"), p2, p1old, p1new)

	// `-key` and `key:desc` are the same descending sort.
	eq("--sort -created_at", sp8ListIDs(t, dir, "--sort", "-created_at"), p1new, p1old, p2)
	eq("--sort created_at:desc", sp8ListIDs(t, dir, "--sort", "created_at:desc"), p1new, p1old, p2)
	eq("--sort -priority", sp8ListIDs(t, dir, "--sort", "-priority"), p2, lo, hi)
	eq("--sort priority:desc", sp8ListIDs(t, dir, "--sort", "priority:desc"), p2, lo, hi)

	// `+key` and `key:asc` are the bare key.
	eq("--sort +created_at", sp8ListIDs(t, dir, "--sort", "+created_at"), p2, p1old, p1new)
	eq("--sort created_at:asc", sp8ListIDs(t, dir, "--sort", "created_at:asc"), p2, p1old, p1new)

	// Several keys, and naming `id` replaces the added tie-breaker.
	eq("--sort priority,created_at", sp8ListIDs(t, dir, "--sort", "priority,created_at"), p1old, p1new, p2)
	eq("--sort priority,-id", sp8ListIDs(t, dir, "--sort", "priority,-id"), hi, lo, p2)
	eq("--sort priority:asc,id:desc", sp8ListIDs(t, dir, "--sort", "priority:asc,id:desc"), hi, lo, p2)

	// The four fields, and nothing else.
	for _, f := range []string{"priority", "created_at", "closed_at", "id"} {
		pass5Run(t, dir, 0, "", "list", "--json", "--sort", f)
	}
	for _, bad := range []string{"sp8-no-such-field", "title", "priority:sideways", "priority,,id", "priority,"} {
		pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--sort", bad)
	}
}

// sp8Object parses one JSON object from stdout.
func sp8Object(t *testing.T, what, doc string) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(doc)), &obj); err != nil {
		t.Fatalf("%s is not one JSON object: %v\n%s", what, err, doc)
	}
	return obj
}

// sp8Example returns the first ```json block that follows marker inside the
// spec section under heading.
func sp8Example(t *testing.T, heading, marker string) string {
	t.Helper()
	section := sp6Section(t, sp6Spec(t), heading)
	i := strings.Index(section, marker)
	if i < 0 {
		t.Fatalf("spec section %s has no %q", heading, marker)
	}
	rest := section[i:]
	j := strings.Index(rest, "```json\n")
	if j < 0 {
		t.Fatalf("spec section %s: no json block after %q", heading, marker)
	}
	rest = rest[j+len("```json\n"):]
	return rest[:strings.Index(rest, "\n```")]
}

// sp8FirstKeys returns the sorted keys of the first element of the JSON
// array raw, or "" when the array is empty.
func sp8FirstKeys(t *testing.T, what string, raw json.RawMessage) string {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("%s is not an array of objects: %v\n%s", what, err, raw)
	}
	if len(rows) == 0 {
		return ""
	}
	return sp7Keys(rows[0])
}

// sp8Envelope asserts that stdout is an error envelope with exactly the keys
// error, message and details, a string code equal to code, and returns the
// message and the sorted detail keys.
func sp8Envelope(t *testing.T, what, stdout, code string) (message, detailKeys string) {
	t.Helper()
	obj := sp8Object(t, what, stdout)
	if got := sp7Keys(obj); got != "details,error,message" {
		t.Errorf("%s: envelope keys %q, want details,error,message\n%s", what, got, stdout)
	}
	var slug string
	if err := json.Unmarshal(obj["error"], &slug); err != nil || slug != code {
		t.Errorf("%s: error is %s, want the string %q", what, obj["error"], code)
	}
	_ = json.Unmarshal(obj["message"], &message)
	var details map[string]json.RawMessage
	if err := json.Unmarshal(obj["details"], &details); err != nil {
		t.Errorf("%s: details is %s, want an object", what, obj["details"])
	}
	return message, sp7Keys(details)
}

// TestDocClaim_Spec_InitExitCodesAndOutputModes asserts the `act init`
// "Exit codes" line and the sentence that opens its "JSON output": init on
// an existing tracker is exit 1 with act_already_initialized (not 2), --force
// gets past it, outside a git working tree is exit 3, a tracker remote that
// already holds the tracker is exit 3 unless --force-new, an unknown flag is
// exit 2 — and JSON is printed only under --json.
func TestDocClaim_Spec_InitExitCodesAndOutputModes(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	host := brokencoHostRepo(t)

	// Without --json: human lines on stdout, no JSON.
	out, stderr, code := runActIn(t, host, "init")
	if code != 0 || !strings.HasPrefix(out, "Initialized .act/ at ") || !strings.Contains(linesOf(out)[0], " with node_id ") || strings.Contains(out, "{") {
		t.Fatalf("act init: exit %d; want human lines starting `Initialized .act/ at <path> with node_id <id>` and no JSON\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	// On an existing tracker: exit 1, act_already_initialized, both modes.
	out, _ = pass5Run(t, host, 1, "act_already_initialized", "init", "--json")
	msg, _ := sp8Envelope(t, "act init --json on an existing tracker", out, "act_already_initialized")
	if !strings.Contains(msg, "config.json already exists; pass --force to reinitialize") {
		t.Errorf("act init --json on an existing tracker: message %q", msg)
	}
	out, stderr, code = runActIn(t, host, "init")
	if code != 1 || out != "" || !strings.Contains(stderr, "already exists; pass --force") {
		t.Errorf("act init on an existing tracker: exit %d, want 1, empty stdout and the message on stderr\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	// --force gets past it; --json prints one object.
	out, _ = pass5Run(t, host, 0, "", "init", "--force", "--json")
	if obj := sp8Object(t, "act init --force --json", out); string(obj["ok"]) != "true" || string(obj["nested_committed"]) != "false" {
		t.Errorf("act init --force --json: want ok true and nested_committed false on a re-init\n%s", out)
	}

	// Outside a git working tree: 3.
	pass5Run(t, t.TempDir(), 3, "not_in_git", "init", "--json")

	// An unknown flag: 2.
	if _, _, code := runActIn(t, brokencoHostRepo(t), "init", "--sp8-no-such-flag"); code != 2 {
		t.Errorf("act init --sp8-no-such-flag: exit %d, want 2", code)
	}

	// A tracker remote that already holds this repo's tracker: 3 unless
	// --force-new. (--force is not the override.)
	bares := t.TempDir()
	brokencoTrackerBare(t, bares)
	fresh := brokencoHostRepo(t)
	pass5Run(t, fresh, 0, "", "list", "--json") // path proof: no remote configured yet
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
	pass5Run(t, fresh, 3, "tracker_not_checked_out", "init", "--json")
	pass5Run(t, fresh, 3, "tracker_not_checked_out", "init", "--force", "--json")
	if _, err := os.Stat(filepath.Join(fresh, ".act", "config.json")); err == nil {
		t.Fatalf("a refused act init left a config.json behind")
	}
	pass5Run(t, fresh, 0, "", "init", "--force-new", "--json")
	if _, err := os.Stat(filepath.Join(fresh, ".act", "config.json")); err != nil {
		t.Errorf("act init --force-new: no config.json: %v", err)
	}
}

// TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary asserts the JSON
// examples of the `act init`, `act list` and `act search` sections and the
// paragraph under each: every example parses, and its top-level keys — and
// for list and search the keys of its one row — are exactly those of real
// output; the keys the paragraphs call optional or absent are so.
func TestDocClaim_Spec_InitListSearchJSONExamplesMatchBinary(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	t.Setenv("ACT_NO_FETCH", "")

	t.Run("init", func(t *testing.T) {
		example := sp8Object(t, "the spec's init example", sp8Example(t, "### `act init`", "**JSON output**"))
		const always = "act_dir,gitignore_updated,hook_installed,host_committed,nested_committed,node_id,ok"
		const fresh = "act_dir,gitignore_updated,hook_installed,host_committed,host_files_uncommitted,nested_committed,node_id,ok"
		host := brokencoHostRepo(t)
		out, _ := pass5Run(t, host, 0, "", "init", "--json")
		first := sp8Object(t, "act init --json", out)
		if sp7Keys(first) != fresh || sp7Keys(example) != fresh {
			t.Errorf("act init --json on a fresh repo has keys %q; the spec's example has %q; want %q\n%s", sp7Keys(first), sp7Keys(example), fresh, out)
		}
		for k, want := range map[string]string{"ok": "true", "nested_committed": "true", "host_committed": "false", "gitignore_updated": "true", "hook_installed": "true", "host_files_uncommitted": `[".gitignore"]`} {
			if string(first[k]) != want || strings.ReplaceAll(string(example[k]), " ", "") != want {
				t.Errorf("init %s: binary %s, spec example %s, want %s", k, first[k], example[k], want)
			}
		}
		// A re-init writes nothing on the host side: exactly the seven.
		out, _ = pass5Run(t, host, 0, "", "init", "--force", "--json")
		again := sp8Object(t, "act init --force --json", out)
		if sp7Keys(again) != always {
			t.Errorf("act init --force --json has keys %q, want exactly the always-present seven %q", sp7Keys(again), always)
		}
		for _, k := range []string{"nested_committed", "gitignore_updated", "hook_installed", "host_committed"} {
			if string(again[k]) != "false" {
				t.Errorf("act init --force --json: %s is %s, want false on a re-init", k, again[k])
			}
		}
		// contributing_suggested: a public-looking remote, no --contributing.
		pub := brokencoHostRepo(t)
		sp6Git(t, "-C", pub, "remote", "add", "origin", "https://github.com/example/sp8.git")
		out, _ = pass5Run(t, pub, 0, "", "init", "--json")
		if obj := sp8Object(t, "act init --json with a public remote", out); string(obj["contributing_suggested"]) != "true" || obj["contributing_emitted"] != nil {
			t.Errorf("act init --json with a public-looking remote: want contributing_suggested true and no contributing_emitted\n%s", out)
		}
		if first["contributing_suggested"] != nil || first["contributing_emitted"] != nil || first["partial_failures"] != nil {
			t.Errorf("act init --json with no remote carries an optional key it has nothing to say for\n%s", out)
		}
		for _, gone := range []string{"writer_version", "schema_version"} {
			if first[gone] != nil || example[gone] != nil {
				t.Errorf("init output or the spec's example carries %s", gone)
			}
		}
	})

	dir := sp6Site(t)
	id := createIssue(t, dir, "sp8 needle in a haystack")
	createIssue(t, dir, "sp8 other")

	t.Run("list", func(t *testing.T) {
		example := sp8Object(t, "the spec's list example", sp8Example(t, "### `act list`", "**JSON output:**"))
		const wantTop = "count,issues,refresh,total,truncated"
		out, _ := pass5Run(t, dir, 0, "", "list", "--json")
		real := sp8Object(t, "act list --json", out)
		if sp7Keys(example) != wantTop || sp7Keys(real) != wantTop {
			t.Errorf("list top-level keys: binary %q, spec example %q, want %q", sp7Keys(real), sp7Keys(example), wantTop)
		}
		// An unclaimed row carries the five fixed keys and created_at.
		if got := sp8FirstKeys(t, "list issues", real["issues"]); got != "created_at,id,priority,status,title,type" {
			t.Errorf("an unclaimed list row has keys %q", got)
		}
		if string(real["count"]) != "2" || string(real["total"]) != "2" || string(real["truncated"]) != "false" {
			t.Errorf("act list --json: count %s total %s truncated %s, want 2 2 false", real["count"], real["total"], real["truncated"])
		}
		out, _ = pass5Run(t, dir, 0, "", "list", "--json", "--limit", "1")
		capped := sp8Object(t, "act list --json --limit 1", out)
		if string(capped["count"]) != "1" || string(capped["total"]) != "2" || string(capped["truncated"]) != "true" {
			t.Errorf("act list --json --limit 1: count %s total %s truncated %s, want 1 2 true", capped["count"], capped["total"], capped["truncated"])
		}
		if !strings.Contains(string(real["refresh"]), `"reason":"no_remote"`) || !strings.Contains(string(example["refresh"]), `"reason":"no_remote"`) {
			t.Errorf("list refresh: binary %s, example %s; want reason no_remote in a tracker with no origin", real["refresh"], example["refresh"])
		}
		out, _ = pass5Run(t, dir, 0, "", "list", "--json", "--status", "closed")
		if empty := sp8Object(t, "act list --json --status closed", out); string(empty["issues"]) != "[]" || sp7Keys(empty) != wantTop {
			t.Errorf("an empty listing: issues %s, keys %q; want [] and the same five keys", empty["issues"], sp7Keys(empty))
		}
		// The example's row is a claimed one: claim, and compare.
		pass5Run(t, dir, 0, "", "update", "--json", "--claim", id)
		out, _ = pass5Run(t, dir, 0, "", "list", "--json", "--status", "in_progress")
		claimed := sp8Object(t, "act list --json --status in_progress", out)
		rowKeys := sp8FirstKeys(t, "list issues", claimed["issues"])
		if exKeys := sp8FirstKeys(t, "the example's issues", example["issues"]); rowKeys != exKeys || rowKeys != "assignee,claimed_at,created_at,id,priority,status,title,type" {
			t.Errorf("a claimed list row has keys %q; the spec example's row has %q", rowKeys, exKeys)
		}
		pass5Run(t, dir, 0, "", "update", "--json", "--unclaim", id)
	})

	t.Run("search", func(t *testing.T) {
		example := sp8Object(t, "the spec's search example", sp8Example(t, "### `act search <query>`", "**JSON output:**"))
		const wantTop = "count,matches,refresh"
		out, _ := pass5Run(t, dir, 0, "", "search", "--json", "needle")
		real := sp8Object(t, "act search --json", out)
		if sp7Keys(example) != wantTop || sp7Keys(real) != wantTop {
			t.Errorf("search top-level keys: binary %q, spec example %q, want %q", sp7Keys(real), sp7Keys(example), wantTop)
		}
		rowKeys := sp8FirstKeys(t, "search matches", real["matches"])
		if exKeys := sp8FirstKeys(t, "the example's matches", example["matches"]); rowKeys != exKeys || rowKeys != "id,snippet,status,title" {
			t.Errorf("a search match has keys %q; the spec example's match has %q", rowKeys, exKeys)
		}
		if string(real["count"]) != "1" || !strings.Contains(string(real["matches"]), "[needle]") || !strings.Contains(string(example["matches"]), "[needle]") {
			t.Errorf("act search --json needle: count %s, matches %s; want one match whose snippet wraps the term in [ ]", real["count"], real["matches"])
		}
		out, _ = pass5Run(t, dir, 0, "", "search", "--json", "sp8nosuchterm")
		if none := sp8Object(t, "act search --json with no match", out); string(none["matches"]) != "[]" || string(none["count"]) != "0" || sp7Keys(none) != wantTop {
			t.Errorf("a search with no match: %s; want matches [] and count 0 under the same three keys", out)
		}
	})
}

// TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey asserts the envelope sentence
// under "Universal exit codes", the cycle envelope quoted in the `act dep
// add` "Exit codes" line, the MCP "Per-command tools" sentence, and the
// migrate payload in "Op type payloads": `error` is a string code, the
// envelope's keys are exactly error, message and details, and the spec shows
// no "kind" key anywhere.
func TestDocClaim_Spec_ErrorEnvelopeHasNoKindKey(t *testing.T) {
	spec := sp6Spec(t)
	if strings.Contains(spec, `"kind"`) {
		t.Errorf(`docs/spec.md still shows a "kind" key`)
	}
	dir := sp6Site(t)
	a := createIssue(t, dir, "sp8 a")
	b := createIssue(t, dir, "sp8 b")

	// The cycle envelope, as the dep add section quotes it.
	pass5Run(t, dir, 0, "", "dep", "add", "--json", a, b)
	out, stderr := pass5Run(t, dir, 1, "cycle", "dep", "add", "--json", b, a)
	msg, keys := sp8Envelope(t, "act dep add (cycle) --json", out, "cycle")
	if want := "act dep add: cycle detected: " + b + " -> " + a + " -> " + b; msg != want || keys != "path" || stderr != "" {
		t.Errorf("cycle envelope: message %q, details keys %q, stderr %q; want message %q and details.path", msg, keys, stderr, want)
	}
	var env struct {
		Details struct {
			Path []string `json:"path"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || strings.Join(env.Details.Path, ",") != b+","+a+","+b {
		t.Errorf("cycle envelope: details.path %v (%v), want [%s %s %s]", env.Details.Path, err, b, a, b)
	}
	// Human mode: the message alone, on stderr.
	out, stderr, code := runActIn(t, dir, "dep", "add", b, a)
	if code != 1 || out != "" || strings.TrimSpace(stderr) != msg {
		t.Errorf("act dep add (cycle), human mode: exit %d, stdout %q, stderr %q; want the message alone on stderr", code, out, stderr)
	}

	// Other failures, one per exit code, have the same three keys.
	for _, tc := range []struct {
		exit int
		code string
		args []string
	}{
		{3, "issue_not_found", []string{"show", "--json", pass5NoSuchID}},
		{2, "bad_flag", []string{"list", "--json", "--sort", "sp8-no-such-field"}},
		{2, "blocked_requires_dep", []string{"update", "--json", "--status", "blocked", b}},
		{1, "dep_not_found", []string{"update", "--json", "--dep-rm", a, b}},
	} {
		out, _ := pass5Run(t, dir, tc.exit, tc.code, tc.args...)
		sp8Envelope(t, "act "+strings.Join(tc.args, " "), out, tc.code)
	}

	// The exception: a doctor run that fails on its findings prints the
	// findings document.
	lock := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	out, _, code = runActIn(t, dir, "doctor", "--json", "--check", "stale-git-lock")
	if doc := sp8Object(t, "act doctor --json with a finding", out); code != 1 || sp7Keys(doc) != "count,findings,remote_status" {
		t.Errorf("act doctor --json with an error finding: exit %d, keys %q; want 1 and the findings document, not an envelope", code, sp7Keys(doc))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatalf("remove lock: %v", err)
	}

	// MCP: an error is a tool result with isError true whose text is the
	// envelope.
	out, stderr, code = pass5MCP(t, dir, nil, pass5MCPInit,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_show","arguments":{"id":"`+pass5NoSuchID+`"}}}`)
	if code != 0 || !strings.Contains(out, `"isError":true`) || !strings.Contains(out, `{\"error\":\"issue_not_found\",\"message\":`) || strings.Contains(out, `\"kind\"`) {
		t.Errorf("MCP act_show of an unknown id: exit %d; want an isError result whose text is the issue_not_found envelope\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	// The migrate payload: the keys `act migrate` writes.
	payloads := sp6Section(t, spec, "### Op type payloads")
	if !strings.Contains(payloads, `{ "from_version": 1, "to_version": 2, "transform_id": "v1-to-v2" }`) {
		t.Errorf("the Op type payloads block does not show the migrate payload with transform_id")
	}
	src, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "op", "migrate.go"))
	if err != nil {
		t.Fatalf("read internal/op/migrate.go: %v", err)
	}
	for _, tag := range []string{"`json:\"from_version\"`", "`json:\"to_version\"`", "`json:\"transform_id\"`", `fmt.Sprintf("v%d-to-v%d", fromVersion, toVersion)`} {
		if !strings.Contains(string(src), tag) {
			t.Errorf("internal/op/migrate.go no longer contains %s; the migrate payload in the spec was written from it", tag)
		}
	}
}
