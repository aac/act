package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	if doc := sp8Object(t, "act doctor --json with a finding", out); code != 1 || sp7Keys(doc) != "count,findings,node_id,remote_status" {
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
	node := idNodeIDOf(t, dir)
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

// ---- Items 5 and 7: the error table and `act help errors`. ----

// sp8Emitted is the result of scanning the source for error-code emitters.
type sp8Emitted struct {
	// sites maps each emitted code to the places that emit it, as
	// "path:line" relative to the repo root.
	sites map[string][]string
	// consts maps every declared `Err* = "slug"` string constant to its
	// slug, emitted or not.
	consts map[string]string
}

var (
	// An operand that names a code: a string literal slug, or an Err*
	// identifier (optionally cli.-qualified).
	sp8CodeOperand = `("[a-z][a-z0-9_]*"|(?:cli\.)?Err[A-Za-z0-9]+)`
	// The five shapes in which the source puts a code into an envelope:
	//   Error: X / "error": X   — a struct or map literal's error field
	//   cli.New(X, ...)          — the Envelope constructor (bare New( inside
	//                              package cli); the operand may be on the
	//                              next line
	//   errEnvelope(X, ...)      — the MCP server's envelope helper
	//   writeToolError(_, _, X   — the MCP server's tool-error writer
	//   errTag := X / errTag = X — `act import`, which picks the code into a
	//                              variable first
	sp8EmitShapes = []*regexp.Regexp{
		// At least one blank after the colon: gofmt always writes one in a
		// Go literal, and compact JSON quoted in a usage string
		// (`{"error":"x"}`) has none — that is an example, not an emitter.
		regexp.MustCompile(`(?:\bError:|"error":)[ \t]+` + sp8CodeOperand),
		regexp.MustCompile(`\bcli\.New\(\s*` + sp8CodeOperand),
		regexp.MustCompile(`\berrEnvelope\(\s*` + sp8CodeOperand),
		regexp.MustCompile(`\bwriteToolError\([^,\n]+,[^,\n]+,\s*` + sp8CodeOperand),
		regexp.MustCompile(`\berrTag\s*:?=\s*` + sp8CodeOperand),
	}
	sp8BareNew   = regexp.MustCompile(`(?:^|[^.\w])New\(\s*` + sp8CodeOperand)
	sp8ConstDecl = regexp.MustCompile(`(?m)^\s*(?:const\s+)?(Err[A-Za-z0-9]+)\s*=\s*"([a-z][a-z0-9_]*)"`)
)

// sp8EmittedCodes scans every non-test Go file under cmd/ and internal/
// (except cmd/act/help.go, which is the help text under test) for the emit
// shapes above. Comment lines are blanked first, so a code named only in a
// comment is not an emitter; so is a code that is only declared, compared
// against, or named in a flag's help string.
func sp8EmittedCodes(t *testing.T) sp8Emitted {
	t.Helper()
	root := sp6RepoRoot(t)
	type file struct{ rel, body string }
	var files []file
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if rel == "cmd/act/help.go" {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			lines := strings.Split(string(raw), "\n")
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "//") {
					lines[i] = ""
				}
			}
			files = append(files, file{rel, strings.Join(lines, "\n")})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	if len(files) < 50 {
		t.Fatalf("scanned only %d Go files under cmd/ and internal/", len(files))
	}
	res := sp8Emitted{sites: map[string][]string{}, consts: map[string]string{}}
	for _, f := range files {
		for _, m := range sp8ConstDecl.FindAllStringSubmatch(f.body, -1) {
			res.consts[m[1]] = m[2]
		}
	}
	for _, f := range files {
		shapes := sp8EmitShapes
		if strings.HasPrefix(f.rel, "internal/cli/") {
			shapes = append(append([]*regexp.Regexp{}, shapes...), sp8BareNew)
		}
		for _, re := range shapes {
			for _, loc := range re.FindAllStringSubmatchIndex(f.body, -1) {
				operand := f.body[loc[2]:loc[3]]
				var slug string
				if strings.HasPrefix(operand, `"`) {
					slug = strings.Trim(operand, `"`)
				} else if slug = res.consts[strings.TrimPrefix(operand, "cli.")]; slug == "" {
					continue // an Err* identifier that is not a code constant
				}
				line := 1 + strings.Count(f.body[:loc[2]], "\n")
				res.sites[slug] = append(res.sites[slug], f.rel+":"+strconv.Itoa(line))
			}
		}
	}
	return res
}

// sp8MCPOnly reports whether every emitter of a code is in internal/mcp.
func sp8MCPOnly(sites []string) bool {
	for _, s := range sites {
		if !strings.HasPrefix(s, "internal/mcp/") {
			return false
		}
	}
	return len(sites) > 0
}

var (
	sp8ExitLine   = regexp.MustCompile(`(?:\}\)?, |^\s*return |^\s*return [^\n]*, |^\s*os\.Exit\()(\d+)\)?$`)
	sp8ReturnLine = regexp.MustCompile(`^\s*return\b`)
	sp8ExitAhead  = regexp.MustCompile(`^\s*return [^\n]*\b(\d+), &?[A-Za-z.]+\{$`)
)

// sp8SiteExit returns the process exit code the source returns with the
// error emitted at site ("path:line"): the integer that ends the first
// `}, N` / `return N` / `return ..., N` / `os.Exit(N)` line at or after the
// site, looking no further than the first return statement and at most 14
// lines. ok is false when that statement returns something other than an
// integer literal (the caller decides the code).
func sp8SiteExit(t *testing.T, site string) (exit int, ok bool) {
	t.Helper()
	i := strings.LastIndex(site, ":")
	body, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), site[:i]))
	if err != nil {
		t.Fatalf("read %s: %v", site[:i], err)
	}
	n, _ := strconv.Atoi(site[i+1:])
	lines := strings.Split(string(body), "\n")
	// A helper that returns the code ahead of the envelope:
	// `return "", 2, XErrorOutput{` on the line above the error field.
	if n >= 2 {
		if m := sp8ExitAhead.FindStringSubmatch(lines[n-2]); m != nil {
			exit, _ = strconv.Atoi(m[1])
			return exit, true
		}
	}
	for j := n - 1; j < len(lines) && j < n+14; j++ {
		if m := sp8ExitLine.FindStringSubmatch(lines[j]); m != nil {
			exit, _ = strconv.Atoi(m[1])
			return exit, true
		}
		if sp8ReturnLine.MatchString(lines[j]) {
			return 0, false
		}
	}
	return 0, false
}

// sp8ErrorRow is one row of the spec's CLI error table.
type sp8ErrorRow struct {
	code    string
	exits   []int    // the exit cell's integers; nil when the cell is "varies"
	format  string   // the backticked message format; "" when the cell has none
	details []string // the detail keys named ahead of any parenthesis
}

// sp8ErrorTables parses the two tables under "### 1. Error handling": the
// CLI table's rows, and the codes of the MCP-only table.
func sp8ErrorTables(t *testing.T) (rows []sp8ErrorRow, mcp []string) {
	t.Helper()
	section := sp6Section(t, sp6Spec(t), "### 1. Error handling")
	cut := strings.Index(section, "**Codes only the MCP server emits.**")
	if cut < 0 {
		t.Fatalf("the Error handling section has no MCP-only table")
	}
	tick := regexp.MustCompile("`([^`]*)`")
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` +\\| ([^|]+?) +\\| (.*) \\| ([^|]*) \\|$").FindAllStringSubmatch(section[:cut], -1) {
		row := sp8ErrorRow{code: m[1]}
		if m[2] != "varies" {
			for _, f := range strings.Split(m[2], ", ") {
				n, err := strconv.Atoi(f)
				if err != nil {
					t.Fatalf("error table row %s: exit cell %q is neither integers nor `varies`", row.code, m[2])
				}
				row.exits = append(row.exits, n)
			}
		}
		if !strings.HasPrefix(m[3], "no message") {
			if f := tick.FindStringSubmatch(m[3]); f != nil {
				row.format = f[1]
			}
		}
		keys, _, _ := strings.Cut(strings.TrimSpace(m[4]), " (")
		if keys != "none" {
			for _, k := range tick.FindAllStringSubmatch(keys, -1) {
				row.details = append(row.details, k[1])
			}
			if len(row.details) == 0 {
				t.Fatalf("error table row %s: details cell %q is neither `none` nor backticked keys", row.code, m[4])
			}
		}
		sort.Strings(row.details)
		rows = append(rows, row)
	}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` +\\|").FindAllStringSubmatch(section[cut:], -1) {
		mcp = append(mcp, m[1])
	}
	if len(rows) < 60 || len(mcp) < 3 {
		t.Fatalf("parsed %d CLI rows and %d MCP rows from the spec's error tables", len(rows), len(mcp))
	}
	return rows, mcp
}

// sp8HelpRows returns the codes `act help errors` lists: those ahead of the
// "MCP-only codes" line, and those after it.
func sp8HelpRows(t *testing.T) (cli, mcp []string) {
	t.Helper()
	help, stderr, code := runAct(t, "help", "errors")
	help += stderr
	i := strings.Index(help, "ERROR CODES")
	j := strings.Index(help, "NO ACT STATE IN THIS CHECKOUT")
	if code != 0 || i < 0 || j < i {
		t.Fatalf("act help errors: exit %d; could not find the ERROR CODES section", code)
	}
	section := help[i:j]
	k := strings.Index(section, "MCP-only codes")
	if k < 0 {
		t.Fatalf("act help errors has no MCP-only codes block")
	}
	row := regexp.MustCompile(`(?m)^    ([a-z_]+) +\S`)
	for _, m := range row.FindAllStringSubmatch(section[:k], -1) {
		cli = append(cli, m[1])
	}
	for _, m := range row.FindAllStringSubmatch(section[k:], -1) {
		mcp = append(mcp, m[1])
	}
	return cli, mcp
}

// sp8SameCodes fails when listed (a doc's rows) and emitted (the source's
// codes) differ as sets, naming what each side lacks, or listed repeats one.
func sp8SameCodes(t *testing.T, where string, listed []string, emitted map[string]bool) {
	t.Helper()
	seen := map[string]bool{}
	for _, c := range listed {
		if seen[c] {
			t.Errorf("%s lists %s twice", where, c)
		}
		seen[c] = true
		if !emitted[c] {
			t.Errorf("%s lists %s, which no source line emits there; drop the row", where, c)
		}
	}
	var missing []string
	for c := range emitted {
		if !seen[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s has no row for %v, which the source emits; add the rows", where, missing)
	}
}

// TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet asserts the
// rule stated above the spec's error table and at the foot of the code list
// in `act help errors`: each lists every error code the source emits and no
// other.
//
// "Emits" is what sp8EmittedCodes finds: a non-test source line under cmd/
// or internal/ that puts the code in an envelope's error field. A code whose
// every emitter is in internal/mcp is MCP-only and belongs in each doc's
// MCP-only block; every other code belongs in the main list. A constant
// with no emitter (ambiguous_id, op_invalid, hlc_drift, index_corrupt) has
// no row anywhere.
func TestDocClaim_ErrorCodes_HelpAndSpecListExactlyTheEmittedSet(t *testing.T) {
	em := sp8EmittedCodes(t)
	cli, mcp := map[string]bool{}, map[string]bool{}
	for code, sites := range em.sites {
		if sp8MCPOnly(sites) {
			mcp[code] = true
		} else {
			cli[code] = true
		}
	}
	// The scan sees each emit shape, and tells an emitted code from a dead one.
	for code, min := range map[string]int{
		"not_in_git": 10, "bad_flag": 20, // Error: / "error": literals
		"write_lock_timeout": 5, // an Err* constant
		"cycle":              1, // cli.New( with the code on the next line
		"import_failed":      1, // errTag := "..."
		"bad_args":           5, // errEnvelope(
		"method_not_allowed": 1, // writeToolError(
	} {
		if len(em.sites[code]) < min {
			t.Fatalf("the source scan found %d emitters of %s, want at least %d: %v", len(em.sites[code]), code, min, em.sites[code])
		}
	}
	if len(cli) < 60 || len(mcp) < 3 || !mcp["unknown_tool"] || mcp["internal"] || !cli["internal"] {
		t.Fatalf("the source scan looks wrong: %d CLI codes, %d MCP-only codes (%v)", len(cli), len(mcp), mcp)
	}
	for _, dead := range []string{"ambiguous_id", "op_invalid", "hlc_drift", "index_corrupt", "redact_target_not_found", "push_exhausted"} {
		if len(em.sites[dead]) != 0 {
			t.Errorf("%s is emitted now (%v); it needs a row in both lists", dead, em.sites[dead])
		}
	}
	for _, konst := range []string{"ErrAmbiguousID", "ErrOpInvalid", "ErrHLCDrift", "ErrIndexCorrupt"} {
		if em.consts[konst] == "" {
			t.Errorf("%s is no longer declared; drop it from this test's list of constants nothing emits", konst)
		}
	}

	rows, specMCP := sp8ErrorTables(t)
	var specCLI []string
	for _, r := range rows {
		specCLI = append(specCLI, r.code)
	}
	helpCLI, helpMCP := sp8HelpRows(t)
	sp8SameCodes(t, "the spec's error table", specCLI, cli)
	sp8SameCodes(t, "the spec's MCP-only table", specMCP, mcp)
	sp8SameCodes(t, "act help errors", helpCLI, cli)
	sp8SameCodes(t, "act help errors, MCP-only block", helpMCP, mcp)

	// The MCP-only codes, as tool results.
	dir := sp6Site(t)
	call := func(flags []string, name, args string) string {
		out, stderr, code := pass5MCP(t, dir, flags, pass5MCPInit,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
		if code != 0 || !strings.Contains(out, `"isError":true`) {
			t.Errorf("MCP %s %s %v: exit %d, want an isError tool result\nstdout=%s\nstderr=%s", name, args, flags, code, out, stderr)
		}
		return out
	}
	for _, tc := range []struct {
		code, name, args string
		flags            []string
		message          string
	}{
		{"unknown_tool", "act_sp8_no_such_tool", `{}`, nil, `unknown tool \\\"act_sp8_no_such_tool\\\"`},
		{"bad_args", "act_show", `{"id":7}`, nil, ""},
		{"read_only_violation", "act_next", `{"read_only":true}`, nil, "act_next: server or call is read-only"},
		{"method_not_allowed", "act_create", `{"title":"sp8"}`, []string{"--read-only"}, `server is read-only; tool \\\"act_create\\\" not permitted`},
	} {
		if out := call(tc.flags, tc.name, tc.args); !strings.Contains(out, `{\"error\":\"`+tc.code+`\",\"message\":\"`+tc.message) {
			t.Errorf("MCP %s %s %v: want the %s envelope with message %q\n%s", tc.name, tc.args, tc.flags, tc.code, tc.message, out)
		}
	}
	if out, _ := pass5Run(t, dir, 0, "", "list", "--json", "--all"); !strings.Contains(out, `"count":0`) {
		t.Errorf("a write tool refused by --read-only still wrote an issue\n%s", out)
	}
}

// sp8Provoker makes the binary fail with one error code and returns its
// stdout and exit code. bare marks the commands that print their failure
// without a details key.
type sp8Provoker struct {
	run  func(t *testing.T) (stdout string, exit int)
	bare bool
}

// sp8BadOpVersion rewrites id's create op to carry an op_version no binary
// reads, which makes the fold reject it.
func sp8BadOpVersion(t *testing.T, dir, id string) {
	t.Helper()
	pass5Rewrite(t, pass5CreateOp(t, dir, id), regexp.MustCompile(`"op_version":\s*1\b`), `"op_version":99`)
}

// sp8In runs act in dir and returns stdout and the exit code.
func sp8In(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	out, _, code := runActIn(t, dir, args...)
	return out, code
}

// sp8Provokers maps an error code to a cheap way of making the binary emit
// it. Every row of the spec's error table is either here or in
// sp8NotProvoked.
var sp8Provokers = map[string]sp8Provoker{
	"not_in_git": {run: func(t *testing.T) (string, int) {
		brokencoIsolateEnv(t)
		return sp8In(t, t.TempDir(), "list", "--json")
	}},
	"act_not_initialized": {run: func(t *testing.T) (string, int) {
		brokencoIsolateEnv(t)
		return sp8In(t, brokencoHostRepo(t), "create", "--json", "sp8")
	}},
	"issue_not_found": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "show", "--json", pass5NoSuchID)
	}},
	"id_ambiguous": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		return sp8In(t, dir, "show", "--json", pass5Twin(t, dir, createIssue(t, dir, "sp8")))
	}},
	"version_skew": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		pass5Rewrite(t, pass5CreateOp(t, dir, createIssue(t, dir, "sp8")), pass5WriterRe, `"writer_version":"99.0.0"`)
		return sp8In(t, dir, "version", "--check-repo", "--json")
	}},
	"cycle_detected": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "sp8")
		return sp8In(t, dir, "update", "--json", "--parent", id, id)
	}},
	"dep_not_found": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		return sp8In(t, dir, "update", "--json", "--dep-rm", createIssue(t, dir, "sp8 a"), createIssue(t, dir, "sp8 b"))
	}},
	"hook_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		hook := filepath.Join(dir, ".act", "hooks", "create")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\necho sp8-refused >&2\nexit 7\n"), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}
		return sp8In(t, dir, "create", "--json", "sp8")
	}},
	"import_invalid_jsonl": {run: func(t *testing.T) (string, int) {
		bad := filepath.Join(t.TempDir(), "sp8-bad.jsonl")
		if err := os.WriteFile(bad, []byte("not json\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", bad, err)
		}
		return sp8In(t, sp6Site(t), "import", "--json", bad)
	}},
	"import_failed": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "import", "--json", filepath.Join(t.TempDir(), "sp8-missing.jsonl"))
	}},
	"remote_unreachable": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "state", "import", "--json", "--from-remote", "file://"+filepath.Join(t.TempDir(), "sp8-missing.git"), t.TempDir())
	}},
	"stat_failed": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "state", "import", "--json", filepath.Join(t.TempDir(), "sp8-no-such-dir"))
	}},
	"target_not_empty": {run: func(t *testing.T) (string, int) {
		target := t.TempDir()
		if err := os.MkdirAll(filepath.Join(target, ".act"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, ".act", "sp8-junk"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return sp8In(t, sp6Site(t), "state", "import", "--json", target)
	}},
	"act_state_not_initialized": {run: func(t *testing.T) (string, int) {
		brokencoIsolateEnv(t)
		src := t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, ".act"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return sp8In(t, t.TempDir(), "state", "import", "--json", "--from-cwd", src)
	}},
	"worker_state_not_found": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "state", "export", "--json", t.TempDir())
	}},
	"blocked_by_external_dep": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "sp8")
		pass5Run(t, dir, 0, "", "dep", "add", "--json", "--external", "sp8:X-1", id)
		return sp8In(t, dir, "close", "--json", id)
	}},
	"tracker_not_checked_out": {run: func(t *testing.T) (string, int) {
		brokencoIsolateEnv(t)
		host := brokencoHostRepo(t)
		bares := t.TempDir()
		brokencoTrackerBare(t, bares)
		t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
		return sp8In(t, host, "create", "--json", "sp8")
	}},
	"blocked_requires_dep": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		return sp8In(t, dir, "update", "--json", "--status", "blocked", createIssue(t, dir, "sp8"))
	}},
	"act_already_initialized": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "init", "--json")
	}},
	"bad_flag": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "list", "--json", "--sort", "sp8-no-such-field")
	}},
	"bad_input": {bare: true, run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "migrate", "--from", "2", "--to", "1")
	}},
	"migration_not_found": {bare: true, run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "migrate", "--from", "1", "--to", "2")
	}},
	"no_repo": {bare: true, run: func(t *testing.T) (string, int) {
		brokencoIsolateEnv(t)
		return sp8In(t, brokencoHostRepo(t), "version", "--check-repo", "--json")
	}},
	"config_read_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		if err := os.WriteFile(filepath.Join(dir, ".act", "config.json"), []byte("{sp8"), 0o644); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
		return sp8In(t, dir, "create", "--json", "sp8")
	}},
	"cycle": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		a, b := createIssue(t, dir, "sp8 a"), createIssue(t, dir, "sp8 b")
		pass5Run(t, dir, 0, "", "dep", "add", "--json", a, b)
		return sp8In(t, dir, "dep", "add", "--json", b, a)
	}},
	"file_not_found": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "create", "--json", "--description-file", filepath.Join(t.TempDir(), "sp8-missing.txt"), "sp8")
	}},
	"fold_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "sp8")
		sp8BadOpVersion(t, dir, id)
		return sp8In(t, dir, "show", "--json", id)
	}},
	"ops_read_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "sp8")
		sp8BadOpVersion(t, dir, id)
		return sp8In(t, dir, "log", "--json", id)
	}},
	"index_rebuild_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		sp8BadOpVersion(t, dir, createIssue(t, dir, "sp8"))
		return sp8In(t, dir, "list", "--json")
	}},
	"index_open_failed": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		createIssue(t, dir, "sp8")
		matches, _ := filepath.Glob(filepath.Join(dir, ".act", "index.db*"))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				t.Fatalf("remove %s: %v", m, err)
			}
		}
		if err := os.Mkdir(filepath.Join(dir, ".act", "index.db"), 0o755); err != nil {
			t.Fatalf("mkdir index.db: %v", err)
		}
		return sp8In(t, dir, "list", "--json")
	}},
	"has_descendants": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		parent := createIssue(t, dir, "sp8 parent")
		createIssue(t, dir, "sp8 child", "--parent", parent)
		return sp8In(t, dir, "delete", "--json", "--reason", "sp8", parent)
	}},
	"tombstoned": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "sp8")
		pass5Run(t, dir, 0, "", "delete", "--json", "--reason", "sp8", id)
		return sp8In(t, dir, "show", "--json", "--commit-marker", id)
	}},
	"invalid_flag": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "search", "--json", "--in", "sp8-bogus", "sp8")
	}},
	"stale_git_lock": {run: func(t *testing.T) (string, int) {
		dir := sp6Site(t)
		if err := os.WriteFile(filepath.Join(dir, ".act", ".git", "index.lock"), nil, 0o644); err != nil {
			t.Fatalf("plant lock: %v", err)
		}
		return sp8In(t, dir, "create", "--json", "sp8")
	}},
	"upstream_not_configured": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "remote", "sync", "--json")
	}},
	"upstream_public": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "remote", "add-upstream", "--json", "https://sp8.public.example.com/tracker.git")
	}},
	"push_failed": {run: func(t *testing.T) (string, int) {
		return sp8In(t, sp6Site(t), "remote", "add-upstream", "--json", filepath.Join(t.TempDir(), "sp8-missing-upstream.git"))
	}},
}

// sp8NotProvoked lists the rows no test provokes, with why. Their exit codes
// are read from the return statements at their emit sites, and the literal
// parts of their message format and their detail keys are looked up in the
// source.
var sp8NotProvoked = map[string]string{
	"claim_lost":                  "needs two writers racing through a shared remote",
	"claim_unconfirmed":           "prints the claim result, not an envelope; TestDocClaim_Clr_UnreachableOriginClaimIsUnconfirmed runs it",
	"bootstrap_timeout":           "needs a clone that outlasts its budget",
	"bootstrap_validation_failed": "needs an import whose result fails its own check",
	"claim_failed":                "needs the claim's write or pull to fail",
	"doctor_failed":               "needs `act remote enable` on a tracker with a doctor error",
	"fts_parse":                   "queries are quoted before FTS5 sees them, so none is rejected",
	"query_failed":                "needs SQLite to fail a well-formed query",
	"index_query_failed":          "needs SQLite to fail a query on an index that opened",
	"internal":                    "needs a result of a type the command does not handle",
	"unknown":                     "the fallback for a failure payload with no code",
	"op_filename_collision":       "needs a worker op file whose bytes differ from the host's",
	"write_lock_timeout":          "needs another process holding .act/.write.lock for the whole wait",
	"io_failure":                  "needs .act/ops to be unreadable",
	"transform_failed":            "no migration is registered, so no transform runs",
	"walk_failed":                 "needs .act/ops to fail mid-walk",
	"ops_scan_failed":             "needs .act/ops to be unreadable",
	"ops_walk_failed":             "needs .act/ops to fail mid-walk",
	"marshal_failed":              "needs json.Marshal to fail on act's own structs",
	"envelope_invalid":            "needs act to build an op envelope its own validator rejects",
	"payload_invalid":             "flag checks reject bad values as bad_flag before a payload is built",
	"hash_failed":                 "needs hashing act's own envelope to fail",
	"nonce_failed":                "needs the system random source to fail",
	"id_collision":                "needs eight id collisions in a row",
	"init_dirs_failed":            "needs an unwritable repo root",
	"init_hooks_failed":           "needs an unwritable .act/hooks",
	"init_ops_failed":             "needs an unwritable .act/ops",
	"nested_init_failed":          "needs git init or the first commit to fail inside .act/",
	"write_config_failed":         "needs an unwritable .act/",
	"write_failed":                "needs an op write to fail for a reason other than a lock",
	"branch_failed":               "needs git to refuse the --branch checkout",
	"stage_failed":                "needs git add to fail for a reason other than a stale lock",
	"commit_failed":               "needs git commit to fail for a reason other than a stale lock",
}

// sp8Source is every non-test Go file under cmd/ and internal/, joined.
func sp8Source(t *testing.T) string {
	t.Helper()
	root := sp6RepoRoot(t)
	var b strings.Builder
	for _, top := range []string{"cmd", "internal"} {
		_ = filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				body, _ := os.ReadFile(path)
				b.Write(body)
			}
			return nil
		})
	}
	return b.String()
}

var sp8Placeholder = regexp.MustCompile(`<[^<>]*>`)

// sp8FormatRegexp turns a message format from the table into a regexp that
// matches a whole message: each <placeholder> is one or more characters.
func sp8FormatRegexp(format string) *regexp.Regexp {
	var parts []string
	for _, lit := range sp8Placeholder.Split(format, -1) {
		parts = append(parts, regexp.QuoteMeta(lit))
	}
	return regexp.MustCompile(`(?s)^` + strings.Join(parts, `.+`) + `$`)
}

// TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource asserts every row of
// the spec's CLI error table, every column.
//
// Exit: the row's exit codes are exactly those the source returns with the
// code — read from the return statement at each emit site — plus the one a
// provoked run shows. A code whose emit sites all leave the exit to their
// caller must be provoked.
//
// Message and details: a row with a provoker is run, and the binary's
// message must match the row's format and its details keys must be the
// row's. For a row that cannot be provoked cheaply, every literal stretch
// of the format and every detail key must appear in the source.
func TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource(t *testing.T) {
	em := sp8EmittedCodes(t)
	source := sp8Source(t)
	rows, _ := sp8ErrorTables(t)
	for _, row := range rows {
		row := row
		t.Run(row.code, func(t *testing.T) {
			p, provoked := sp8Provokers[row.code]
			why, excused := sp8NotProvoked[row.code]
			if provoked == excused {
				t.Fatalf("row %s must be in exactly one of sp8Provokers and sp8NotProvoked (provoked %v, excused %q)", row.code, provoked, why)
			}

			want := map[int]bool{}
			for _, site := range em.sites[row.code] {
				if strings.HasPrefix(site, "internal/mcp/") {
					continue
				}
				if n, ok := sp8SiteExit(t, site); ok {
					want[n] = true
				}
			}
			if provoked {
				out, exit := p.run(t)
				want[exit] = true
				obj := sp8Object(t, "the "+row.code+" failure", out)
				wantKeys := "details,error,message"
				if p.bare {
					wantKeys = "error,message"
				}
				if sp7Keys(obj) != wantKeys || sp8Str(obj["error"]) != row.code {
					t.Fatalf("provoking %s: exit %d, keys %q, error %s; want keys %q and that code\n%s", row.code, exit, sp7Keys(obj), obj["error"], wantKeys, out)
				}
				msg := sp8Str(obj["message"])
				if row.format == "" {
					t.Errorf("row %s is provoked but its message cell gives no backticked format; the binary says %q", row.code, msg)
				} else if !sp8FormatRegexp(row.format).MatchString(msg) {
					t.Errorf("row %s: the binary's message does not match the row's format\n  format:  %s\n  message: %s", row.code, row.format, msg)
				}
				var details map[string]json.RawMessage
				_ = json.Unmarshal(obj["details"], &details)
				if got := sp7Keys(details); got != strings.Join(row.details, ",") {
					t.Errorf("row %s: the binary's details keys are %q, the row lists %q\n%s", row.code, got, strings.Join(row.details, ","), out)
				}
			} else {
				for _, lit := range sp8Placeholder.Split(row.format, -1) {
					if len(lit) >= 6 && !strings.Contains(source, lit) {
						t.Errorf("row %s: no source file contains %q, a literal part of the row's message format %q", row.code, lit, row.format)
					}
				}
				for _, k := range row.details {
					if !strings.Contains(source, `"`+k+`"`) {
						t.Errorf("row %s: no source file names the details key %q", row.code, k)
					}
				}
			}

			if row.exits == nil {
				if row.code != "unknown" || len(want) != 0 {
					t.Errorf("row %s has exit `varies`, but the source returns %v with it", row.code, want)
				}
				return
			}
			if len(want) == 0 {
				t.Fatalf("row %s: no emit site returns a literal exit code and nothing provokes it; add a provoker", row.code)
			}
			got := map[int]bool{}
			for _, n := range row.exits {
				got[n] = true
			}
			for n := range want {
				if !got[n] {
					t.Errorf("row %s lists exit %v; the binary also returns %d with it (emit sites %v)", row.code, row.exits, n, em.sites[row.code])
				}
			}
			for n := range got {
				if !want[n] {
					t.Errorf("row %s lists exit %d, which no emit site returns and no provoked run shows", row.code, n)
				}
			}
		})
	}
	// The sentence under the table's intro: with two exit codes, the command
	// decides. The same unopenable index is exit 1 from list, 3 from search.
	t.Run("index_open_failed from list and from search", func(t *testing.T) {
		dir := sp6Site(t)
		createIssue(t, dir, "sp8")
		matches, _ := filepath.Glob(filepath.Join(dir, ".act", "index.db*"))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				t.Fatalf("remove %s: %v", m, err)
			}
		}
		if err := os.Mkdir(filepath.Join(dir, ".act", "index.db"), 0o755); err != nil {
			t.Fatalf("mkdir index.db: %v", err)
		}
		pass5Run(t, dir, 1, "index_open_failed", "list", "--json")
		pass5Run(t, dir, 3, "index_open_failed", "search", "--json", "sp8")
	})
	for code := range sp8Provokers {
		if len(em.sites[code]) == 0 {
			t.Errorf("sp8Provokers has %s, which the source no longer emits", code)
		}
	}
	for code := range sp8NotProvoked {
		if len(em.sites[code]) == 0 {
			t.Errorf("sp8NotProvoked has %s, which the source no longer emits", code)
		}
	}
}

// sp8RowChecked reports whether the eighth pass's row test covers code — it
// has a provoker or a recorded reason for having none. The seventh pass's
// exit-code test defers to it for the rows it does not provoke itself.
func sp8RowChecked(code string) bool {
	_, provoked := sp8Provokers[code]
	_, excused := sp8NotProvoked[code]
	return provoked || excused
}
