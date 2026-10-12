package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// ---- Item 1: the 22 flag bullets that sp7NoBulletYet used to excuse. ----

// sp8Show returns `act show <id> --json` as a key -> raw value map.
func sp8Show(t *testing.T, dir, id string) map[string]json.RawMessage {
	t.Helper()
	out, _ := pass5Run(t, dir, 0, "", "show", "--json", id)
	return sp8Object(t, "act show --json "+id, out)
}

// sp8Str unquotes a raw JSON string; "" when absent or not a string.
func sp8Str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// sp8ReadyIDs runs `act ready --json <args>` and returns the ids, sorted,
// joined with commas, plus refresh.reason.
func sp8ReadyIDs(t *testing.T, dir string, args ...string) (ids, reason string) {
	t.Helper()
	out, stderr, code := runActIn(t, dir, append([]string{"ready", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("act ready --json %v: exit %d\nstdout=%s\nstderr=%s", args, code, out, stderr)
	}
	var res struct {
		Ready []struct {
			ID string `json:"id"`
		} `json:"ready"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("act ready --json %v: %v\n%s", args, err, out)
	}
	var list []string
	for _, r := range res.Ready {
		list = append(list, r.ID)
	}
	sort.Strings(list)
	return strings.Join(list, ","), sp6RefreshReason(t, out)
}

// sp8HostWithCommit makes a host repo that has an identity and one commit.
func sp8HostWithCommit(t *testing.T) string {
	t.Helper()
	host := brokencoHostRepo(t)
	sp6Git(t, "-C", host, "config", "user.email", "sp8@example.com")
	sp6Git(t, "-C", host, "config", "user.name", "sp8")
	sp6Git(t, "-C", host, "config", "commit.gpgsign", "false")
	sp6Git(t, "-C", host, "commit", "-q", "--allow-empty", "-m", "sp8: first")
	return host
}

// TestDocClaim_Spec_FlagBulletsInit asserts the --contributing, --commit-host
// and --json bullets of the `act init` section. (--force-new is asserted by
// TestDocClaim_Spec_InitExitCodesAndOutputModes.)
func TestDocClaim_Spec_FlagBulletsInit(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	const marker = "<!-- act:contributing-stanza:start -->"
	count := func(dir string) int {
		body, err := os.ReadFile(filepath.Join(dir, "CONTRIBUTING.md"))
		if err != nil {
			return -1
		}
		return strings.Count(string(body), marker)
	}
	hostFiles := func(dir string) string {
		out, err := sp7GitOut("-C", dir, "status", "--porcelain")
		if err != nil {
			t.Fatalf("git status: %v", err)
		}
		return out
	}

	// Without --contributing: no CONTRIBUTING.md, and nothing committed.
	plain := sp8HostWithCommit(t)
	out, _ := pass5Run(t, plain, 0, "", "init", "--json")
	if obj := sp8Object(t, "act init --json", out); count(plain) != -1 || string(obj["host_committed"]) != "false" || obj["contributing_emitted"] != nil {
		t.Errorf("act init --json: CONTRIBUTING.md stanza count %d, output %s; want no file and host_committed false", count(plain), out)
	}
	if st := hostFiles(plain); !strings.Contains(st, ".gitignore") {
		t.Errorf("act init without --commit-host: host status %q; want .gitignore left uncommitted", st)
	}
	if n, _ := sp7GitOut("-C", plain, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("act init without --commit-host: host has %s commits, want the 1 it started with", n)
	}
	// A public-looking remote without the flag: a suggestion, still no file.
	pub := brokencoHostRepo(t)
	sp6Git(t, "-C", pub, "remote", "add", "origin", "git@github.com:example/sp8.git")
	human, _, code := runActIn(t, pub, "init")
	if code != 0 || count(pub) != -1 || !strings.Contains(human, "act init --force --contributing") {
		t.Errorf("act init with a public-looking remote: exit %d, stanza count %d; want no CONTRIBUTING.md and a suggestion naming --contributing\n%s", code, count(pub), human)
	}

	// --contributing, no file yet: created, uncommitted.
	fresh := sp8HostWithCommit(t)
	out, _ = pass5Run(t, fresh, 0, "", "init", "--json", "--contributing")
	obj := sp8Object(t, "act init --json --contributing", out)
	if count(fresh) != 1 || string(obj["contributing_emitted"]) != "true" || string(obj["host_files_uncommitted"]) != `[".gitignore","CONTRIBUTING.md"]` || string(obj["host_committed"]) != "false" {
		t.Errorf("act init --contributing: stanza count %d, output %s; want one stanza, contributing_emitted, both files uncommitted", count(fresh), out)
	}
	// ... and written once.
	out, _ = pass5Run(t, fresh, 0, "", "init", "--json", "--force", "--contributing")
	if obj := sp8Object(t, "second act init --contributing", out); count(fresh) != 1 || obj["contributing_emitted"] != nil {
		t.Errorf("a second act init --contributing: stanza count %d, output %s; want still one and no contributing_emitted", count(fresh), out)
	}
	// An existing CONTRIBUTING.md keeps its content.
	kept := brokencoHostRepo(t)
	if err := os.WriteFile(filepath.Join(kept, "CONTRIBUTING.md"), []byte("# sp8 house rules\n"), 0o644); err != nil {
		t.Fatalf("write CONTRIBUTING.md: %v", err)
	}
	pass5Run(t, kept, 0, "", "init", "--json", "--contributing")
	if body, _ := os.ReadFile(filepath.Join(kept, "CONTRIBUTING.md")); !strings.HasPrefix(string(body), "# sp8 house rules\n") || count(kept) != 1 || !strings.Contains(string(body), "Act-Id:") {
		t.Errorf("act init --contributing on an existing file: want the old content kept and one Act-Id stanza appended\n%s", body)
	}

	// --commit-host: one host commit holding exactly the files written.
	both := sp8HostWithCommit(t)
	out, _ = pass5Run(t, both, 0, "", "init", "--json", "--contributing", "--commit-host")
	obj = sp8Object(t, "act init --json --contributing --commit-host", out)
	files, _ := sp7GitOut("-C", both, "show", "--name-only", "--format=", "HEAD")
	n, _ := sp7GitOut("-C", both, "rev-list", "--count", "HEAD")
	if string(obj["host_committed"]) != "true" || obj["host_files_uncommitted"] != nil || n != "2" || strings.Join(strings.Fields(files), ",") != ".gitignore,CONTRIBUTING.md" || hostFiles(both) != "" {
		t.Errorf("act init --contributing --commit-host: output %s, host commits %s, files in HEAD %q, status %q; want host_committed, 2 commits, both files, a clean tree", out, n, files, hostFiles(both))
	}
	only := sp8HostWithCommit(t)
	pass5Run(t, only, 0, "", "init", "--json", "--commit-host")
	if files, _ := sp7GitOut("-C", only, "show", "--name-only", "--format=", "HEAD"); strings.TrimSpace(files) != ".gitignore" {
		t.Errorf("act init --commit-host: files in the host commit %q, want .gitignore alone", files)
	}
	// No host commit yet: nothing is committed.
	empty := brokencoHostRepo(t)
	out, _ = pass5Run(t, empty, 0, "", "init", "--json", "--commit-host")
	if obj := sp8Object(t, "act init --commit-host on an empty host", out); string(obj["host_committed"]) != "false" || string(obj["host_files_uncommitted"]) != `[".gitignore"]` {
		t.Errorf("act init --commit-host on a host with no commit: %s; want host_committed false and .gitignore uncommitted", out)
	}
	if _, err := sp7GitOut("-C", empty, "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		t.Errorf("act init --commit-host made the host repo's first commit")
	}
	// The run wrote no host file: nothing to commit.
	out, _ = pass5Run(t, only, 0, "", "init", "--json", "--force", "--commit-host")
	if obj := sp8Object(t, "act init --force --commit-host", out); string(obj["host_committed"]) != "false" {
		t.Errorf("a re-init that wrote no host file reports host_committed true: %s", out)
	}
	if n, _ := sp7GitOut("-C", only, "rev-list", "--count", "HEAD"); n != "2" {
		t.Errorf("a re-init with --commit-host and nothing written: host has %s commits, want 2", n)
	}
}

// TestDocClaim_Spec_FlagBulletsCreateDescription asserts the --description
// and --description-file bullets of the `act create` section.
func TestDocClaim_Spec_FlagBulletsCreateDescription(t *testing.T) {
	dir := sp6Site(t)
	inline := createIssue(t, dir, "sp8 inline", "--description", "sp8 inline body")
	if got := sp8Str(sp8Show(t, dir, inline)["description"]); got != "sp8 inline body" {
		t.Errorf("create --description: show reports description %q", got)
	}
	empty := createIssue(t, dir, "sp8 empty", "--description", "")
	bare := createIssue(t, dir, "sp8 bare")
	if e, b := sp8Show(t, dir, empty), sp8Show(t, dir, bare); sp7Keys(e) != sp7Keys(b) || sp8Str(e["description"]) != "" {
		t.Errorf("create --description \"\": show keys %q, without the flag %q; want the same, with no description", sp7Keys(e), sp7Keys(b))
	}

	path := filepath.Join(t.TempDir(), "sp8-body.txt")
	if err := os.WriteFile(path, []byte("sp8 file body\nline two — ü"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	fromFile := createIssue(t, dir, "sp8 file", "--description-file", path)
	if got := sp8Str(sp8Show(t, dir, fromFile)["description"]); got != "sp8 file body\nline two — ü" {
		t.Errorf("create --description-file: show reports description %q", got)
	}

	cmd := exec.Command(actBinary(t), "create", "sp8 stdin", "--description-file", "-", "--json")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("sp8 stdin body")
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("act create --description-file -: %v\n%s", err, stdout)
	}
	if got := sp8Str(sp8Show(t, dir, sp8Str(sp8Object(t, "create", string(stdout))["id"]))["description"]); got != "sp8 stdin body" {
		t.Errorf("create --description-file -: show reports description %q", got)
	}

	pass5Run(t, dir, 2, "bad_flag", "create", "--json", "--description", "x", "--description-file", path, "sp8 both")
	pass5Run(t, dir, 3, "file_not_found", "create", "--json", "--description-file", filepath.Join(t.TempDir(), "sp8-missing.txt"), "sp8 missing")
	if out, _ := pass5Run(t, dir, 0, "", "search", "--json", "sp8 both"); !strings.Contains(out, `"count":0`) {
		t.Errorf("a refused create left an issue behind\n%s", out)
	}
}

// TestDocClaim_Spec_FlagBulletsExternalDeps asserts the bullets that share
// the external-dep gate: `act dep add --external`, `act update --ext-rm`,
// `act update --force` and `act close --force`.
func TestDocClaim_Spec_FlagBulletsExternalDeps(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "sp8 gated")
	other := createIssue(t, dir, "sp8 free")
	extDeps := func() string { return string(sp8Show(t, dir, id)["external_deps"]) }
	opsOf := func(suffix string) int {
		n := 0
		_ = filepath.Walk(filepath.Join(dir, ".act", "ops", id), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, suffix) {
				n++
			}
			return nil
		})
		return n
	}

	// --external: repeatable, one op per ref, and its own JSON result.
	out, _ := pass5Run(t, dir, 0, "", "dep", "add", "--json", id, "--external", "linear:ENG-123", "--external", "sp8:two")
	if strings.TrimSpace(out) != `{"ok":true,"issue":"`+id+`","external":["linear:ENG-123","sp8:two"],"committed":true}` {
		t.Errorf("act dep add --external --json printed %s", out)
	}
	if extDeps() != `["linear:ENG-123","sp8:two"]` || opsOf("-add_external_dep.json") != 2 {
		t.Errorf("after dep add --external x2: external_deps %s, %d add_external_dep op files; want both refs and 2 ops", extDeps(), opsOf("-add_external_dep.json"))
	}
	// Usage errors.
	pass5Run(t, dir, 2, "bad_flag", "dep", "add", "--json", id, other, "--external", "sp8:x")
	pass5Run(t, dir, 2, "bad_flag", "dep", "add", "--json", id, "--external", "sp8:x", "--blocked-by", other)
	pass5Run(t, dir, 2, "bad_flag", "dep", "add", "--json", id, "--external", "sp8:x", "--blocks", other)
	pass5Run(t, dir, 2, "bad_flag", "dep", "add", "--json", id, "--external", "sp8:x", "--type", "relates")
	pass5Run(t, dir, 2, "bad_flag", "dep", "add", "--json", id, "--external", "")

	// While it carries a ref: out of ready, and close and claim refuse.
	if ids, _ := sp8ReadyIDs(t, dir); ids != other {
		t.Errorf("act ready with an external dep on %s: %q, want only %s", id, ids, other)
	}
	pass5Run(t, dir, 2, "blocked_by_external_dep", "close", "--json", id)
	pass5Run(t, dir, 2, "blocked_by_external_dep", "update", "--json", "--claim", id)
	if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "open" {
		t.Fatalf("a refused close/claim changed the status to %q", st)
	}

	// update --force: only with --claim; it lets the claim through and warns.
	pass5Run(t, dir, 2, "bad_flag", "update", "--json", "--force", id)
	pass5Run(t, dir, 0, "", "update", "--json", "--force", "--priority", "1", id)
	if extDeps() != `["linear:ENG-123","sp8:two"]` {
		t.Errorf("update --force --priority changed the external deps: %s", extDeps())
	}
	_, stderr := pass5Run(t, dir, 0, "", "update", "--json", "--claim", "--force", id)
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "linear:ENG-123") || !strings.Contains(stderr, "sp8:two") {
		t.Errorf("update --claim --force: stderr does not carry a WARNING naming each bypassed dep\n%s", stderr)
	}
	if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "in_progress" {
		t.Errorf("update --claim --force: status %q, want in_progress", st)
	}
	pass5Run(t, dir, 0, "", "update", "--json", "--unclaim", id)

	// --ext-rm: one op per ref, absent refs included; empty and --claim are 2.
	pass5Run(t, dir, 2, "bad_flag", "update", "--json", "--ext-rm", "", id)
	pass5Run(t, dir, 2, "bad_flag", "update", "--json", "--claim", "--ext-rm", "sp8:two", id)
	out, _ = pass5Run(t, dir, 0, "", "update", "--json", "--ext-rm", "linear:ENG-123", "--ext-rm", "sp8:never-attached", id)
	if !strings.Contains(out, `"ops_written":2`) || opsOf("-remove_external_dep.json") != 2 || extDeps() != `["sp8:two"]` {
		t.Errorf("update --ext-rm of one attached and one absent ref: %s, %d remove ops, external_deps %s; want 2 ops and only sp8:two left", out, opsOf("-remove_external_dep.json"), extDeps())
	}
	pass5Run(t, dir, 2, "blocked_by_external_dep", "close", "--json", id)

	// close --force: closes through the gate and warns.
	_, stderr = pass5Run(t, dir, 0, "", "close", "--json", "--force", "--no-doctor", id)
	if !strings.Contains(stderr, "WARNING") || !strings.Contains(stderr, "sp8:two") {
		t.Errorf("close --force: stderr does not carry a WARNING naming the bypassed dep\n%s", stderr)
	}
	if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "closed" {
		t.Errorf("close --force: status %q, want closed", st)
	}

	// With the last ref cleared, the gate is gone without --force.
	free := createIssue(t, dir, "sp8 cleared")
	pass5Run(t, dir, 0, "", "dep", "add", "--json", free, "--external", "sp8:three")
	pass5Run(t, dir, 0, "", "update", "--json", "--ext-rm", "sp8:three", free)
	if ids, _ := sp8ReadyIDs(t, dir); !strings.Contains(ids, free) {
		t.Errorf("act ready after --ext-rm cleared the only ref: %q lacks %s", ids, free)
	}
	pass5Run(t, dir, 0, "", "close", "--json", "--no-doctor", free)
}

// TestDocClaim_Spec_FlagBulletsUnclaim asserts the --unclaim bullet of the
// `act update` section.
func TestDocClaim_Spec_FlagBulletsUnclaim(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "sp8 claimed")
	unclaimOps := func() int {
		n := 0
		_ = filepath.Walk(filepath.Join(dir, ".act", "ops", id), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, "-unclaim.json") {
				n++
			}
			return nil
		})
		return n
	}
	pass5Run(t, dir, 0, "", "update", "--json", "--claim", id)
	if s := sp8Show(t, dir, id); sp8Str(s["status"]) != "in_progress" || sp8Str(s["assignee"]) == "" {
		t.Fatalf("seed: after --claim: status %q assignee %q", sp8Str(s["status"]), sp8Str(s["assignee"]))
	}
	pass5Run(t, dir, 2, "bad_flag", "update", "--json", "--claim", "--unclaim", id)
	pass5Run(t, dir, 0, "", "update", "--json", "--unclaim", id)
	if s := sp8Show(t, dir, id); sp8Str(s["status"]) != "open" || s["assignee"] != nil || unclaimOps() != 1 {
		t.Errorf("after --unclaim: status %q, assignee %s, %d unclaim ops; want open, no assignee, 1 op", sp8Str(s["status"]), s["assignee"], unclaimOps())
	}
	// On an issue that is not in_progress: an op is written, nothing changes.
	out, _ := pass5Run(t, dir, 0, "", "update", "--json", "--unclaim", id)
	if s := sp8Show(t, dir, id); sp8Str(s["status"]) != "open" || unclaimOps() != 2 || !strings.Contains(out, `"ops_written":1`) {
		t.Errorf("a second --unclaim: status %q, %d unclaim ops, output %s; want open, 2 ops, ops_written 1", sp8Str(s["status"]), unclaimOps(), out)
	}
}

// TestDocClaim_Spec_FlagBulletsCloseNoCodeNoDoctor asserts the --no-code and
// --no-doctor bullets of the `act close` section.
func TestDocClaim_Spec_FlagBulletsCloseNoCodeNoDoctor(t *testing.T) {
	dir := sp6Site(t)
	const warning = "act close: no host commit with 'Act-Id: "
	plain := createIssue(t, dir, "sp8 plain")
	noCode := createIssue(t, dir, "sp8 no-code")
	noDoctor := createIssue(t, dir, "sp8 no-doctor")
	noCommit := createIssue(t, dir, "sp8 no-commit")

	// The post-close check: one stderr line, exit 0, close done.
	_, stderr := pass5Run(t, dir, 0, "", "close", "--json", "--reason", "x", plain)
	if want := warning + plain + "' trailer found in last 50 commits"; !strings.HasPrefix(stderr, want) || len(linesOf(stderr)) != 1 {
		t.Errorf("act close: stderr %q; want one line beginning %q", stderr, want)
	}
	// --no-code does not silence it; it marks the close.
	_, stderr = pass5Run(t, dir, 0, "", "close", "--json", "--no-code", "--reason", "x", noCode)
	if !strings.HasPrefix(stderr, warning+noCode) {
		t.Errorf("act close --no-code: stderr %q; want the post-close warning still", stderr)
	}
	if s, p := sp8Show(t, dir, noCode), sp8Show(t, dir, plain); string(s["closed_no_code"]) != "true" || p["closed_no_code"] != nil {
		t.Errorf("closed_no_code: --no-code close %s, plain close %s; want true and absent", s["closed_no_code"], p["closed_no_code"])
	}
	// --no-doctor silences it; so does --no-commit.
	if _, stderr = pass5Run(t, dir, 0, "", "close", "--json", "--no-doctor", "--reason", "x", noDoctor); stderr != "" {
		t.Errorf("act close --no-doctor: stderr %q, want none", stderr)
	}
	if _, stderr = pass5Run(t, dir, 0, "", "close", "--json", "--no-commit", "--reason", "x", noCommit); strings.Contains(stderr, warning) {
		t.Errorf("act close --no-commit: stderr %q carries the post-close warning", stderr)
	}
	for _, id := range []string{plain, noCode, noDoctor, noCommit} {
		if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "closed" {
			t.Errorf("%s: status %q after its close, want closed", id, st)
		}
	}
	// A host commit carrying the trailer: no warning.
	traced := createIssue(t, dir, "sp8 traced")
	sp6Git(t, "-C", dir, "commit", "-q", "--allow-empty", "-m", "sp8: work\n\nAct-Id: "+traced)
	if _, stderr = pass5Run(t, dir, 0, "", "close", "--json", traced); stderr != "" {
		t.Errorf("act close after a host commit with the trailer: stderr %q, want none", stderr)
	}

	// The close op's payload, and orphan-close. An issue whose id no commit
	// message names — created and closed under --no-commit, then committed
	// by hand — is what orphan-close reports, unless it was closed --no-code.
	site := sp6Site(t)
	bare := createIssue(t, site, "sp8 orphan", "--no-commit")
	marked := createIssue(t, site, "sp8 orphan no-code", "--no-commit")
	pass5Run(t, site, 0, "", "close", "--json", "--no-commit", bare)
	pass5Run(t, site, 0, "", "close", "--json", "--no-commit", "--no-code", marked)
	nested := filepath.Join(site, ".act")
	sp6Git(t, "-C", nested, "add", "ops")
	sp6Git(t, "-C", nested, "-c", "user.email=sp8@example.com", "-c", "user.name=sp8", "commit", "-q", "--no-verify", "-m", "sp8: by hand")
	payload := func(id string) string {
		var body string
		_ = filepath.Walk(filepath.Join(nested, "ops", id), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, "-close.json") {
				b, _ := os.ReadFile(path)
				body = string(b)
			}
			return nil
		})
		return body
	}
	if !strings.Contains(payload(marked), `"no_code":true`) || strings.Contains(payload(bare), "no_code") {
		t.Errorf("close op payloads: --no-code %s; plain %s; want no_code:true only on the first", payload(marked), payload(bare))
	}
	res, code := sp6RunDoctor(t, site, "--check", "orphan-close")
	if code != 0 || len(res.Findings) != 1 || !sp6Has(res, "orphan-close", "warn") || !strings.Contains(res.Findings[0].Message, bare) || strings.Contains(res.Findings[0].Message, marked) {
		t.Errorf("act doctor --check orphan-close: exit %d, findings %+v; want one warning, about %s and not %s", code, res.Findings, bare, marked)
	}
}

// TestDocClaim_Spec_FlagBulletsDepAddDirectional asserts the --blocked-by and
// --blocks bullets of the `act dep add` section.
func TestDocClaim_Spec_FlagBulletsDepAddDirectional(t *testing.T) {
	dir := sp6Site(t)
	a := createIssue(t, dir, "sp8 a")
	b := createIssue(t, dir, "sp8 b")
	c := createIssue(t, dir, "sp8 c")
	d := createIssue(t, dir, "sp8 d")
	edge := func(args ...string) (child, parent, typ string) {
		out, _ := pass5Run(t, dir, 0, "", append([]string{"dep", "add", "--json"}, args...)...)
		obj := sp8Object(t, "act dep add --json", out)
		return sp8Str(obj["child"]), sp8Str(obj["parent"]), sp8Str(obj["type"])
	}
	// <a> --blocked-by <b> is the edge `dep add <a> <b>` writes.
	if child, parent, typ := edge(a, "--blocked-by", b); child != a || parent != b || typ != "blocks" {
		t.Errorf("dep add a --blocked-by b: child %s parent %s type %s; want a blocked by b", child, parent, typ)
	}
	if child, parent, _ := edge(c, d); child != c || parent != d {
		t.Errorf("dep add c d: child %s parent %s; want c blocked by d", child, parent)
	}
	// <a> --blocks <c> is the edge `dep add <c> <a>` writes.
	if child, parent, typ := edge(a, "--blocks", c); child != c || parent != a || typ != "blocks" {
		t.Errorf("dep add a --blocks c: child %s parent %s type %s; want c blocked by a", child, parent, typ)
	}
	if got := string(sp8Show(t, dir, a)["blocked_by"]); got != `["`+b+`"]` {
		t.Errorf("show a: blocked_by %s, want [%s]", got, b)
	}
	if got := string(sp8Show(t, dir, a)["blocks"]); got != `["`+c+`"]` {
		t.Errorf("show a: blocks %s, want [%s]", got, c)
	}
	if ids, _ := sp8ReadyIDs(t, dir); strings.Contains(ids, a) || strings.Contains(ids, c) || !strings.Contains(ids, b) || !strings.Contains(ids, d) {
		t.Errorf("act ready: %q; want b and d ready, a and c blocked", ids)
	}
	// Usage errors.
	for _, args := range [][]string{
		{b, "--blocked-by", d, "--blocks", c},
		{b, d, "--blocked-by", c},
		{b, d, "--blocks", c},
		{b, "--blocked-by", d, "--type", "relates"},
		{b, "--blocks", d, "--type", "supersedes"},
		{"--blocked-by", d},
	} {
		pass5Run(t, dir, 2, "bad_flag", append([]string{"dep", "add", "--json"}, args...)...)
	}
	pass5Run(t, dir, 0, "", "dep", "add", "--json", b, "--blocked-by", d, "--type", "blocks")
}

// TestDocClaim_Spec_FlagBulletsReadyMineAsFresh asserts the --mine, --as,
// --fresh and --no-cache bullets of the `act ready` section.
func TestDocClaim_Spec_FlagBulletsReadyMineAsFresh(t *testing.T) {
	dir := sp6Site(t)
	mine := createIssue(t, dir, "sp8 mine")
	theirs := createIssue(t, dir, "sp8 theirs")
	nobody := createIssue(t, dir, "sp8 nobody")
	cfg, err := os.ReadFile(filepath.Join(dir, ".act", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	node := sp8Str(sp8Object(t, ".act/config.json", string(cfg))["node_id"])
	if node == "" {
		t.Fatalf("config.json has no node_id: %s", cfg)
	}
	pass5Run(t, dir, 0, "", "update", "--json", "--assignee", node, mine)
	pass5Run(t, dir, 0, "", "update", "--json", "--assignee", "sp8-other-node", theirs)
	all := []string{mine, theirs, nobody}
	sort.Strings(all)

	if ids, _ := sp8ReadyIDs(t, dir); ids != strings.Join(all, ",") {
		t.Fatalf("seed: act ready: %q, want all three", ids)
	}
	if ids, _ := sp8ReadyIDs(t, dir, "--mine"); ids != mine {
		t.Errorf("act ready --mine: %q, want only %s (assignee = this checkout's node_id %s)", ids, mine, node)
	}
	if ids, _ := sp8ReadyIDs(t, dir, "--mine", "--as", "sp8-other-node"); ids != theirs {
		t.Errorf("act ready --mine --as sp8-other-node: %q, want only %s", ids, theirs)
	}
	if ids, _ := sp8ReadyIDs(t, dir, "--mine", "--as", "sp8-nobody"); ids != "" {
		t.Errorf("act ready --mine --as sp8-nobody: %q, want none", ids)
	}
	pass5Run(t, dir, 2, "bad_flag", "ready", "--json", "--as", "sp8-other-node")

	// --fresh / --no-cache need an origin and a warm cache to show anything.
	nested := filepath.Join(dir, ".act")
	origin := filepath.Join(t.TempDir(), "sp8-origin.git")
	sp6Git(t, "clone", "-q", "--bare", nested, origin)
	sp6Git(t, "-C", nested, "remote", "add", "origin", origin)
	branch, err := sp7GitOut("-C", nested, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	sp6Git(t, "-C", nested, "push", "-q", "-u", "origin", branch)
	sp6Git(t, "-C", nested, "config", "act.readCacheTTLSeconds", "3600")
	if _, reason := sp8ReadyIDs(t, dir); reason == "hit" || reason == "fresh_flag" {
		t.Fatalf("seed: the first read with an origin reports %q, want a fetch", reason)
	}
	if _, reason := sp8ReadyIDs(t, dir); reason != "hit" {
		t.Fatalf("seed: a second read inside the window reports %q, want hit", reason)
	}
	for _, flag := range []string{"--fresh", "--no-cache"} {
		out, _ := pass5Run(t, dir, 0, "", "ready", "--json", flag)
		if !strings.Contains(out, `"refresh":{"reason":"fresh_flag","fetched":true`) {
			t.Errorf("act ready %s inside the window: want refresh.reason fresh_flag and fetched true\n%s", flag, out)
		}
		if _, reason := sp8ReadyIDs(t, dir); reason != "hit" {
			t.Errorf("a plain read after %s reports %q, want hit", flag, reason)
		}
		pass5Run(t, dir, 2, "bad_flag", "ready", "--json", flag, "--no-fetch")
	}
}

// TestDocClaim_Spec_FlagBulletsNextAllMachines asserts the --all-machines
// bullet of the `act next` section.
func TestDocClaim_Spec_FlagBulletsNextAllMachines(t *testing.T) {
	dir := sp6Site(t)
	pinned := createIssue(t, dir, "sp8 pinned", "--machine", "sp8-elsewhere")
	t.Setenv("ACT_MACHINE", "sp8-here")
	wouldClaim := func(args ...string) string {
		out, _ := pass5Run(t, dir, 0, "", append([]string{"next", "--json"}, args...)...)
		var res struct {
			WouldClaim struct {
				ID string `json:"id"`
			} `json:"would_claim"`
		}
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("act next --json %v: %v\n%s", args, err, out)
		}
		return res.WouldClaim.ID
	}
	if got := wouldClaim("--peek"); got != "" {
		t.Errorf("act next --peek on a machine the only issue is not pinned to: would_claim %q, want none", got)
	}
	if got := wouldClaim("--peek", "--all-machines"); got != pinned {
		t.Errorf("act next --peek --all-machines: would_claim %q, want %s", got, pinned)
	}
	pass5Run(t, dir, 2, "bad_flag", "next", "--json", "--all-machines")
	if s := sp8Show(t, dir, pinned); sp8Str(s["status"]) != "open" || s["assignee"] != nil {
		t.Errorf("after the peeks and the refused claim: status %q, assignee %s; want open and unassigned", sp8Str(s["status"]), s["assignee"])
	}
}

// TestDocClaim_Spec_FlagBulletsMigrateFromTo asserts the --from and --to
// bullets of the "Op-schema migration" section: both required and positive,
// --to greater than --from.
func TestDocClaim_Spec_FlagBulletsMigrateFromTo(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "sp8 first")
	for _, args := range [][]string{
		{}, {"--from", "1"}, {"--to", "2"},
		{"--from", "0", "--to", "2"}, {"--from", "-1", "--to", "2"},
		{"--from", "2", "--to", "1"}, {"--from", "2", "--to", "2"},
	} {
		pass5Run(t, dir, 2, "bad_input", append([]string{"migrate"}, args...)...)
	}
	// A well-formed pair gets past the flag checks (to the registry lookup).
	pass5Run(t, dir, 5, "migration_not_found", "migrate", "--from", "1", "--to", "2")
}
