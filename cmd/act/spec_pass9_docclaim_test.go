package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// Doc-claim cover for the ninth stale pass over docs/spec.md (act-74cf82),
// asserted against the real binary. It builds on the earlier passes' helpers
// (spec_pass5..8_docclaim_test.go).
//
// What was stale: the `act init` section said init creates no commit, that
// `--force` rewrites config.json only if absent, and that init creates an
// empty index.db; the JSON examples of create, show, update, close and
// version showed keys the binary does not print; the envelope sentence named
// too few of the failures that omit `details`; and a comment in
// internal/cli/depadd.go said an ambiguous prefix "always returned 3".

// TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2 asserts the id_ambiguous
// row of the error table for `act dep add`, and the comment above
// resolveDepID in internal/cli/depadd.go: an ambiguous child or parent prefix
// is exit 2 with id_ambiguous, an id that matches nothing is exit 3 with
// issue_not_found.
func TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2(t *testing.T) {
	dir := sp6Site(t)
	a := createIssue(t, dir, "sp9 a")
	b := createIssue(t, dir, "sp9 b")
	prefix := pass5Twin(t, dir, a)
	if strings.HasPrefix(b, prefix) {
		t.Skipf("the two ids share the prefix %s; nothing unambiguous to pair it with", prefix)
	}
	for _, args := range [][]string{
		{prefix, b},
		{b, prefix},
		{b, "--blocked-by", prefix},
		{b, "--blocks", prefix},
	} {
		out, _ := pass5Run(t, dir, 2, "id_ambiguous", append([]string{"dep", "add", "--json"}, args...)...)
		if _, keys := sp8Envelope(t, "act dep add "+strings.Join(args, " "), out, "id_ambiguous"); keys != "candidates,prefix" {
			t.Errorf("act dep add %v: details keys %q, want candidates,prefix\n%s", args, keys, out)
		}
	}
	pass5Run(t, dir, 3, "issue_not_found", "dep", "add", "--json", pass5NoSuchID, b)
	pass5Run(t, dir, 3, "issue_not_found", "dep", "add", "--json", b, pass5NoSuchID)
	if out, _ := pass5Run(t, dir, 0, "", "show", "--json", b); !strings.Contains(out, `"blocked_by":[]`) || !strings.Contains(out, `"blocks":[]`) {
		t.Errorf("a refused dep add left an edge behind\n%s", out)
	}

	src, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "cli", "depadd.go"))
	if err != nil {
		t.Fatalf("read internal/cli/depadd.go: %v", err)
	}
	if strings.Contains(string(src), "always returned 3 for id_ambiguous") {
		t.Errorf("internal/cli/depadd.go still says an ambiguous prefix has always returned 3")
	}
}

// sp9Files lists the regular files under <host>/.act, relative to it, sorted,
// leaving out the nested repo's own .git.
func sp9Files(t *testing.T, host string) string {
	t.Helper()
	root := filepath.Join(host, ".act")
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return strings.Join(files, ",")
}

// sp9NodeID is the node_id the spec's `act init` Behavior paragraph gives
// for a host repo: the first 8 hex characters of sha256(machine id ||
// user.email), the machine id being /etc/machine-id or else the hostname.
func sp9NodeID(t *testing.T, host string) string {
	t.Helper()
	machine := ""
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		machine = strings.TrimSpace(string(data))
	}
	if machine == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			t.Skipf("no /etc/machine-id and no hostname (%v); the expected node_id cannot be worked out", err)
		}
		machine = h
	}
	email, _ := sp7GitOut("-C", host, "config", "user.email")
	sum := sha256.Sum256([]byte(machine + email))
	return hex.EncodeToString(sum[:4])
}

// sp9Config reads <host>/.act/config.json as raw bytes and as a map from key
// to value, string values unquoted.
func sp9Config(t *testing.T, host string) (string, map[string]string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(host, ".act", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	vals := map[string]string{}
	for k, v := range sp8Object(t, ".act/config.json", string(body)) {
		vals[k] = strings.Trim(string(v), `"`)
	}
	return string(body), vals
}

// sp9Exists reports whether <host>/.act/<rel> exists.
func sp9Exists(host, rel string) bool {
	_, err := os.Stat(filepath.Join(host, ".act", filepath.FromSlash(rel)))
	return err == nil
}

// TestDocClaim_Spec_InitWritesAndCommits asserts the Behavior paragraphs and
// the "Side effects" line of the `act init` section: what init creates under
// .act/ (and that index.db is not part of it), the four keys of config.json
// and how node_id is derived, the one commit in the nested repo, and that the
// host repo gets files but no commit unless --commit-host.
func TestDocClaim_Spec_InitWritesAndCommits(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	t.Setenv("ACT_NO_FETCH", "")
	host := sp8HostWithCommit(t)
	nested := filepath.Join(host, ".act")
	pass5Run(t, host, 0, "", "init", "--json")

	// What is on disk: three files, four directories, no index.db.
	if got := sp9Files(t, host); got != "config.json,hooks/close.sample,ops/.gitkeep" {
		t.Errorf("files under .act/ after act init: %q, want config.json, hooks/close.sample and ops/.gitkeep", got)
	}
	for _, dir := range []string{"ops", "snapshots", "hooks", "imports"} {
		if info, err := os.Stat(filepath.Join(nested, dir)); err != nil || !info.IsDir() {
			t.Errorf("act init did not create the directory .act/%s (%v)", dir, err)
		}
	}

	// config.json: exactly four keys.
	raw, cfg := sp9Config(t, host)
	if keys := sp7Keys(sp8Object(t, ".act/config.json", raw)); keys != "created_at,last_hlc,node_id,version" {
		t.Errorf("config.json keys %q, want created_at,last_hlc,node_id,version\n%s", keys, raw)
	}
	if cfg["version"] != "0.1.0" || cfg["last_hlc"] != `{"logical":0,"wall":0}` {
		t.Errorf("config.json: version %q, last_hlc %s; want 0.1.0 and a zero last_hlc", cfg["version"], cfg["last_hlc"])
	}
	if want := sp9NodeID(t, host); cfg["node_id"] != want {
		t.Errorf("config.json node_id %q, want %q = sha256(machine id || user.email)[0:8]", cfg["node_id"], want)
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", cfg["created_at"]); err != nil {
		t.Errorf("config.json created_at %q is not a millisecond UTC timestamp: %v", cfg["created_at"], err)
	}

	// The nested repo: one commit, on main, holding everything init wrote.
	subjects, err := sp7GitOut("-C", nested, "log", "--format=%s")
	if err != nil || subjects != "act init: nested act state bootstrap" {
		t.Errorf("nested repo log after act init: %q (%v); want the one commit `act init: nested act state bootstrap`", subjects, err)
	}
	if branch, _ := sp7GitOut("-C", nested, "rev-parse", "--abbrev-ref", "HEAD"); branch != "main" {
		t.Errorf("nested repo branch %q, want main", branch)
	}
	if st, _ := sp7GitOut("-C", nested, "status", "--porcelain"); st != "" {
		t.Errorf("nested repo has uncommitted changes right after act init: %q", st)
	}
	if tree, _ := sp7GitOut("-C", nested, "ls-tree", "-r", "--name-only", "HEAD"); strings.Join(strings.Fields(tree), ",") != "config.json,hooks/close.sample,ops/.gitkeep" {
		t.Errorf("the nested repo's first commit holds %q", tree)
	}

	// The host repo: .gitignore and the hook written, nothing committed.
	if n, _ := sp7GitOut("-C", host, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("host repo has %s commits after act init, want the 1 it started with", n)
	}
	if body, _ := os.ReadFile(filepath.Join(host, ".gitignore")); !strings.Contains("\n"+string(body), "\n.act/\n") {
		t.Errorf("host .gitignore after act init lacks the line .act/: %q", body)
	}
	if body, _ := os.ReadFile(filepath.Join(host, ".git", "hooks", "pre-commit")); !strings.Contains(string(body), "refusing to commit .act/ paths to the host repo") {
		t.Errorf("host pre-commit hook after act init lacks the .act/ block: %q", body)
	}
	// --commit-host is what makes a host commit.
	committed := sp8HostWithCommit(t)
	pass5Run(t, committed, 0, "", "init", "--json", "--commit-host")
	if n, _ := sp7GitOut("-C", committed, "rev-list", "--count", "HEAD"); n != "2" {
		t.Errorf("host repo has %s commits after act init --commit-host, want 2", n)
	}

	// index.db comes from the first command that reads the index.
	if sp9Exists(host, "index.db") {
		t.Fatalf("act init created index.db")
	}
	pass5Run(t, host, 0, "", "list", "--json")
	if !sp9Exists(host, "index.db") {
		t.Errorf("act list on a new tracker did not create index.db")
	}
}

// TestDocClaim_Spec_InitForceRewritesConfig asserts the --force bullet and
// the --force edge case of the `act init` section: a re-run puts back what is
// missing, always rewrites config.json (node_id derived again, created_at
// new), makes no commit in a nested repo that has one — so the rewrite is an
// uncommitted change there — and leaves op files and snapshots alone.
func TestDocClaim_Spec_InitForceRewritesConfig(t *testing.T) {
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	t.Setenv("ACT_NO_FETCH", "")
	host := sp8HostWithCommit(t)
	nested := filepath.Join(host, ".act")
	pass5Run(t, host, 0, "", "init", "--json")
	id := createIssue(t, host, "sp9 kept")
	opPath := pass5CreateOp(t, host, id)
	opBefore, err := os.ReadFile(opPath)
	if err != nil {
		t.Fatalf("read %s: %v", opPath, err)
	}
	snapshot := filepath.Join(nested, "snapshots", "sp9-snapshot.json")
	if err := os.WriteFile(snapshot, []byte(`{"sp9":true}`), 0o644); err != nil {
		t.Fatalf("write %s: %v", snapshot, err)
	}
	rawBefore, before := sp9Config(t, host)
	commitsBefore, _ := sp7GitOut("-C", nested, "rev-list", "--count", "HEAD")

	// Take away one of each thing a re-run puts back, and change the email
	// node_id is derived from.
	for _, rel := range []string{"imports", "ops/.gitkeep", "hooks/close.sample"} {
		if err := os.Remove(filepath.Join(nested, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("remove .act/%s: %v", rel, err)
		}
	}
	sp6Git(t, "-C", host, "config", "user.email", "sp9-other@example.com")
	time.Sleep(5 * time.Millisecond) // created_at has millisecond precision

	// Without --force nothing is touched.
	pass5Run(t, host, 1, "act_already_initialized", "init", "--json")
	if raw, _ := sp9Config(t, host); raw != rawBefore || sp9Exists(host, "imports") {
		t.Fatalf("a refused act init changed .act/")
	}

	out, _ := pass5Run(t, host, 0, "", "init", "--force", "--json")
	if obj := sp8Object(t, "act init --force --json", out); string(obj["nested_committed"]) != "false" {
		t.Errorf("act init --force --json: nested_committed %s, want false\n%s", obj["nested_committed"], out)
	}
	for _, rel := range []string{"imports", "ops/.gitkeep", "hooks/close.sample"} {
		if !sp9Exists(host, rel) {
			t.Errorf("act init --force did not put back .act/%s", rel)
		}
	}

	// config.json is rewritten although it was there.
	rawAfter, after := sp9Config(t, host)
	if keys := sp7Keys(sp8Object(t, ".act/config.json", rawAfter)); keys != "created_at,last_hlc,node_id,version" {
		t.Errorf("config.json keys after --force: %q", keys)
	}
	if want := sp9NodeID(t, host); after["node_id"] != want || after["node_id"] == before["node_id"] {
		t.Errorf("node_id after --force with a new user.email: %q (was %q); want it derived again, %q", after["node_id"], before["node_id"], want)
	}
	if after["created_at"] == before["created_at"] {
		t.Errorf("created_at after --force is still %q; want the time of the re-run", after["created_at"])
	}

	// No new nested commit; the rewrite sits uncommitted.
	if n, _ := sp7GitOut("-C", nested, "rev-list", "--count", "HEAD"); n != commitsBefore || n != "2" {
		t.Errorf("nested repo has %s commits after --force, had %s; want 2 both times (init, create)", n, commitsBefore)
	}
	if changed, _ := sp7GitOut("-C", nested, "diff", "--name-only", "HEAD"); changed != "config.json" {
		t.Errorf("tracked files changed and uncommitted in the nested repo after --force: %q; want config.json alone", changed)
	}

	// Op files and snapshots are as they were.
	if opAfter, err := os.ReadFile(opPath); err != nil || string(opAfter) != string(opBefore) {
		t.Errorf("act init --force changed or removed the op file %s (%v)", opPath, err)
	}
	if body, err := os.ReadFile(snapshot); err != nil || string(body) != `{"sp9":true}` {
		t.Errorf("act init --force changed or removed a snapshot (%v): %q", err, body)
	}
	if got := sp8Str(sp8Show(t, host, id)["title"]); got != "sp9 kept" {
		t.Errorf("after --force the issue reads back with title %q", got)
	}
}

// sp9ExampleKeys returns the sorted top-level keys of the first JSON example
// that follows marker in the spec section under heading.
func sp9ExampleKeys(t *testing.T, heading, marker string) string {
	t.Helper()
	return sp7Keys(sp8Object(t, "the example under "+marker+" in "+heading, sp8Example(t, heading, marker)))
}

// sp9JSON runs act with args, wants exit 0, and returns the trimmed stdout
// and its top-level keys.
func sp9JSON(t *testing.T, dir string, args ...string) (string, map[string]string) {
	t.Helper()
	out, _ := pass5Run(t, dir, 0, "", args...)
	vals := map[string]string{}
	for k, v := range sp8Object(t, "act "+strings.Join(args, " "), out) {
		vals[k] = string(v)
	}
	return strings.TrimSpace(out), vals
}

// sp9KeysOf returns the sorted keys of vals, comma-joined.
func sp9KeysOf(vals map[string]string) string {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary
// asserts the JSON examples of the `act create`, `act show`, `act update`,
// `act close` and `act version` sections and the paragraph under each: every
// example parses, its top-level keys are exactly those of real output for the
// same kind of call, and the keys the paragraphs call optional or absent are
// so.
func TestDocClaim_Spec_CreateShowUpdateCloseVersionJSONExamplesMatchBinary(t *testing.T) {
	dir := sp6Site(t)
	_, cfg := sp9Config(t, dir)
	node := cfg["node_id"]
	// noCommit is a second tracker for the --no-commit calls, which leave an
	// uncommitted op file behind.
	noCommit := sp6Site(t)

	t.Run("create", func(t *testing.T) {
		const want = "committed,id,ok,op_id,prefix,pushed,title"
		if got := sp9ExampleKeys(t, "### `act create <title>`", "**JSON output:**"); got != want {
			t.Errorf("the spec's create example has keys %q, want %q", got, want)
		}
		_, plain := sp9JSON(t, dir, "create", "--json", "sp9 created")
		if sp9KeysOf(plain) != want || plain["ok"] != "true" || plain["committed"] != "true" || plain["pushed"] != "false" || plain["title"] != `"sp9 created"` || plain["prefix"] != plain["id"] {
			t.Errorf("act create --json: %v; want exactly the keys %q, committed, not pushed, and prefix equal to a six-hex id", plain, want)
		}
		if _, pushed := sp9JSON(t, dir, "create", "--json", "--push", "sp9 pushed"); sp9KeysOf(pushed) != want || pushed["pushed"] != "true" {
			t.Errorf("act create --json --push in a tracker with no remote: %v; want pushed true under the same seven keys", pushed)
		}
		if _, nc := sp9JSON(t, noCommit, "create", "--json", "--no-commit", "sp9 uncommitted"); sp9KeysOf(nc) != want || nc["committed"] != "false" {
			t.Errorf("act create --json --no-commit: %v; want committed false under the same seven keys", nc)
		}
		// warnings: only when --parent names a closed issue.
		closed := createIssue(t, dir, "sp9 closed parent")
		pass5Run(t, dir, 0, "", "close", "--json", "--no-doctor", closed)
		if _, warned := sp9JSON(t, dir, "create", "--json", "--parent", closed, "sp9 child of closed"); sp9KeysOf(warned) != want+",warnings" || warned["warnings"] != `["parent_closed"]` {
			t.Errorf("act create --json --parent <closed>: %v; want the seven keys plus warnings [\"parent_closed\"]", warned)
		}
	})

	t.Run("show", func(t *testing.T) {
		const eleven = "accept,blocked_by,blocks,commits,created_at,id,priority,refresh,status,title,type"
		const example = "accept,blocked_by,blocks,commits,created_at,deps,description,id,parent,priority,refresh,status,title,type"
		if got := sp9ExampleKeys(t, "### `act show <id>`", "**JSON output:**"); got != example {
			t.Errorf("the spec's show example has keys %q, want %q", got, example)
		}
		plain := createIssue(t, dir, "sp9 plain")
		out, shown := sp9JSON(t, dir, "show", "--json", plain)
		if sp9KeysOf(shown) != eleven || shown["accept"] != "[]" || shown["blocked_by"] != "[]" || shown["blocks"] != "[]" || shown["commits"] != "[]" || strings.Contains(out, "null") {
			t.Errorf("act show --json of an issue with a title and nothing else: keys %q; want %q, the lists as [] and no null\n%s", sp9KeysOf(shown), eleven, out)
		}
		// The example's issue: a parent, a blocker, a criterion, a body.
		full := createIssue(t, dir, "sp9 full", "--parent", plain, "--blocked-by", plain, "--accept", "sp9 criterion", "--description", "sp9 body", "--priority", "1")
		out, shown = sp9JSON(t, dir, "show", "--json", full)
		if sp9KeysOf(shown) != example || shown["deps"] != `[{"edge_type":"blocks","parent":"`+plain+`"}]` || shown["accept"] != `["sp9 criterion"]` || shown["parent"] != `"`+plain+`"` {
			t.Errorf("act show --json of an issue like the example's: keys %q, want %q\n%s", sp9KeysOf(shown), example, out)
		}
		for _, gone := range []string{"ok", "issue", "ops_count", "acceptance_criteria", "ops"} {
			if _, has := shown[gone]; has {
				t.Errorf("act show --json carries the key %q", gone)
			}
		}
		// machine, on a pinned issue.
		pinned := createIssue(t, dir, "sp9 pinned", "--machine", "sp9-box")
		if _, shown = sp9JSON(t, dir, "show", "--json", pinned); sp9KeysOf(shown) != strings.Replace(eleven, "id,", "id,machine,", 1) || shown["machine"] != `"sp9-box"` {
			t.Errorf("act show --json of a pinned issue: keys %q; want the eleven plus machine", sp9KeysOf(shown))
		}
		// ops, only with --include-ops.
		if _, shown = sp9JSON(t, dir, "show", "--json", "--include-ops", plain); sp9KeysOf(shown) != strings.Replace(eleven, "id,", "id,ops,", 1) || !strings.HasPrefix(shown["ops"], `[{"op_version":1,`) || !strings.Contains(shown["ops"], `"op_type":"create"`) {
			t.Errorf("act show --json --include-ops: keys %q; want the eleven plus ops, the create op first", sp9KeysOf(shown))
		}
		// assignee and claimed_at, once claimed.
		pass5Run(t, dir, 0, "", "update", "--json", "--claim", plain)
		if _, shown = sp9JSON(t, dir, "show", "--json", plain); sp9KeysOf(shown) != "accept,assignee,blocked_by,blocks,claimed_at,commits,created_at,id,priority,refresh,status,title,type" || shown["assignee"] != `"`+node+`"` {
			t.Errorf("act show --json of a claimed issue: keys %q, assignee %s; want the eleven plus assignee (%s) and claimed_at", sp9KeysOf(shown), shown["assignee"], node)
		}
		// closed_at, closed_reason and closed_by_node, once closed.
		pass5Run(t, dir, 0, "", "close", "--json", "--no-doctor", "--reason", "sp9 done", plain)
		if _, shown = sp9JSON(t, dir, "show", "--json", plain); sp9KeysOf(shown) != "accept,assignee,blocked_by,blocks,claimed_at,closed_at,closed_by_node,closed_reason,commits,created_at,id,priority,refresh,status,title,type" || shown["closed_reason"] != `"sp9 done"` || shown["closed_by_node"] != `"`+node+`"` {
			t.Errorf("act show --json of a closed issue: keys %q; want closed_at, closed_reason and closed_by_node added", sp9KeysOf(shown))
		}
		// A tombstoned issue: id and tombstoned, nothing else.
		gone := createIssue(t, dir, "sp9 deleted")
		pass5Run(t, dir, 0, "", "delete", "--json", gone)
		if out, _ = sp9JSON(t, dir, "show", "--json", gone); out != `{"id":"`+gone+`","tombstoned":true}` {
			t.Errorf("act show --json of a tombstoned issue printed %s", out)
		}
	})

	t.Run("update", func(t *testing.T) {
		const heading = "### `act update <id>`"
		const plainKeys = "committed,id,ops_written"
		const winKeys = "claimed,id,ok,ops_written,winner"
		if got := sp9ExampleKeys(t, heading, "**JSON output (an update without `--claim`):**"); got != plainKeys {
			t.Errorf("the spec's plain update example has keys %q, want %q", got, plainKeys)
		}
		if got := sp9ExampleKeys(t, heading, "**JSON output (claim win):**"); got != winKeys {
			t.Errorf("the spec's claim-win example has keys %q, want %q", got, winKeys)
		}
		if got := sp9ExampleKeys(t, heading, "**JSON output (claim loss):**"); got != "claimed,error,id,ok,reason,winner" {
			t.Errorf("the spec's claim-loss example has keys %q", got)
		}
		id := createIssue(t, dir, "sp9 updated")
		// Two field flags: two ops, one result.
		if out, _ := sp9JSON(t, dir, "update", "--json", "--priority", "0", "--assignee", "sp9-me", id); out != `{"id":"`+id+`","ops_written":2,"committed":true}` {
			t.Errorf("act update --json --priority 0 --assignee sp9-me printed %s", out)
		}
		pass5Run(t, dir, 0, "", "update", "--json", "--assignee", "", id)
		// already_blocked: the fourth key, and committed false with no op.
		waiting := createIssue(t, dir, "sp9 waiting", "--blocked-by", id)
		if out, _ := sp9JSON(t, dir, "update", "--json", "--status", "blocked", waiting); out != `{"id":"`+waiting+`","ops_written":0,"committed":false,"already_blocked":true}` {
			t.Errorf("act update --json --status blocked on an issue with a blocker printed %s", out)
		}
		// --claim refuses every field flag.
		body := filepath.Join(t.TempDir(), "sp9-body.txt")
		if err := os.WriteFile(body, []byte("sp9 body"), 0o644); err != nil {
			t.Fatalf("write %s: %v", body, err)
		}
		for _, flag := range [][]string{
			{"--title", "x"}, {"--status", "open"}, {"--priority", "1"}, {"--type", "bug"}, {"--parent", waiting},
			{"--assignee", "x"}, {"--machine", "sp9-box"}, {"--description", "x"}, {"--description-file", body}, {"--description-append", "x"},
			{"--description-append-file", body}, {"--accept", "x"}, {"--accept-add", "x"}, {"--accept-rm", "0"},
			{"--dep-rm", waiting}, {"--ext-rm", "sp9:ref"},
		} {
			out, _ := pass5Run(t, dir, 2, "bad_flag", append([]string{"update", "--json", "--claim", id}, flag...)...)
			if !strings.Contains(out, "--claim is mutually exclusive with: ") {
				t.Errorf("act update --claim %s: want the mutually-exclusive message\n%s", flag[0], out)
			}
		}
		if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "open" {
			t.Fatalf("a refused --claim left the issue %q", st)
		}
		// The claim itself.
		out, won := sp9JSON(t, dir, "update", "--json", "--claim", id)
		if out != `{"ok":true,"claimed":true,"id":"`+id+`","winner":"`+node+`","ops_written":["claim"]}` || sp9KeysOf(won) != winKeys {
			t.Errorf("act update --json --claim printed %s; want the claim-win result with winner %s", out, node)
		}
		if st := sp8Str(sp8Show(t, dir, id)["status"]); st != "in_progress" {
			t.Errorf("after --claim: status %s, want in_progress", st)
		}
		// The loss shape cannot be provoked from one checkout; hold the
		// example to the source that builds it.
		src, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "cli", "update.go"))
		if err != nil {
			t.Fatalf("read internal/cli/update.go: %v", err)
		}
		for _, lit := range []string{"Error:   ErrClaimLost,", `Reason:  "lost-race",`, "OpsWritten []string `json:\"ops_written,omitempty\"`"} {
			if !strings.Contains(string(src), lit) {
				t.Errorf("internal/cli/update.go no longer contains %s; the claim-loss example was written from it", lit)
			}
		}
		// --no-commit: committed false, the op still counted.
		quiet := createIssue(t, noCommit, "sp9 quiet")
		if out, _ := sp9JSON(t, noCommit, "update", "--json", "--no-commit", "--priority", "1", quiet); out != `{"id":"`+quiet+`","ops_written":1,"committed":false}` {
			t.Errorf("act update --json --no-commit --priority 1 printed %s", out)
		}
	})

	t.Run("close", func(t *testing.T) {
		const want = "commit_marker,committed,id,ops_written,reason"
		if got := sp9ExampleKeys(t, "### `act close <id>`", "**JSON output:**"); got != want {
			t.Errorf("the spec's close example has keys %q, want %q", got, want)
		}
		id := createIssue(t, dir, "sp9 to close")
		if out, _ := sp9JSON(t, dir, "close", "--json", "--no-doctor", "--reason", "shipped", id); out != `{"id":"`+id+`","ops_written":1,"committed":true,"commit_marker":"Act-Id: `+id+`","reason":"shipped"}` {
			t.Errorf("act close --json --reason shipped printed %s", out)
		}
		// closed_at and closed_reason are show's to report.
		if shown := sp8Show(t, dir, id); sp8Str(shown["closed_reason"]) != "shipped" || sp8Str(shown["closed_at"]) == "" {
			t.Errorf("act show after the close: closed_reason %s, closed_at %s", shown["closed_reason"], shown["closed_at"])
		}
		if out, _ := sp9JSON(t, dir, "close", "--json", "--reason", "again", id); out != `{"id":"`+id+`","already_closed":true}` {
			t.Errorf("act close --json of a closed issue printed %s", out)
		}
		bare := createIssue(t, dir, "sp9 no reason")
		if _, closed := sp9JSON(t, dir, "close", "--json", "--no-doctor", bare); sp9KeysOf(closed) != want || closed["reason"] != `""` {
			t.Errorf("act close --json without --reason: %v; want the five keys and reason \"\"", closed)
		}
		quiet := createIssue(t, noCommit, "sp9 quiet close")
		if _, closed := sp9JSON(t, noCommit, "close", "--json", "--no-commit", quiet); sp9KeysOf(closed) != want || closed["committed"] != "false" {
			t.Errorf("act close --json --no-commit: %v; want the five keys and committed false", closed)
		}
	})

	t.Run("version", func(t *testing.T) {
		const want = "binary_version,go_version,platform,writer_version"
		if got := sp9ExampleKeys(t, "### `act version`", "**JSON output:**"); got != want {
			t.Errorf("the spec's version example has keys %q, want %q", got, want)
		}
		if strings.Contains(sp6Spec(t), `"binary_version":"0.1.0"`) {
			t.Errorf(`docs/spec.md still shows "binary_version":"0.1.0"`)
		}
		// Anywhere, repo or not: four keys, indented.
		out, ver := sp9JSON(t, t.TempDir(), "version", "--json")
		if sp9KeysOf(ver) != want || !strings.HasPrefix(out, "{\n  \"binary_version\": \"dev\",\n  \"go_version\": ") {
			t.Errorf("act version --json: keys %q; want %q, indented, binary_version dev in a test build\n%s", sp9KeysOf(ver), want, out)
		}
		if ver["writer_version"] != `"0.1.0"` || ver["go_version"] != `"`+runtime.Version()+`"` || ver["platform"] != `"`+runtime.GOOS+"/"+runtime.GOARCH+`"` {
			t.Errorf("act version --json: %v; want writer_version 0.1.0 and this build's go_version and platform", ver)
		}
		// --check-repo: a fifth key, "" before the first op.
		fresh := sp6Site(t)
		if _, ver = sp9JSON(t, fresh, "version", "--json", "--check-repo"); sp9KeysOf(ver) != "binary_version,go_version,max_op_version,platform,writer_version" || ver["max_op_version"] != `""` {
			t.Errorf("act version --json --check-repo in a tracker with no op: %v; want the four keys plus max_op_version \"\"", ver)
		}
		createIssue(t, fresh, "sp9 first op")
		if _, ver = sp9JSON(t, fresh, "version", "--json", "--check-repo"); ver["max_op_version"] != `"0.1.0"` {
			t.Errorf("act version --json --check-repo after one op: max_op_version %s, want \"0.1.0\"", ver["max_op_version"])
		}
	})
}

// sp9Bare asserts that stdout is a failure with exactly the keys error and
// message — no details — and the given code.
func sp9Bare(t *testing.T, what, stdout, code string) {
	t.Helper()
	obj := sp8Object(t, what, stdout)
	if got := sp7Keys(obj); got != "error,message" || sp8Str(obj["error"]) != code || sp8Str(obj["message"]) == "" {
		t.Errorf("%s: keys %q, error %s; want exactly error and message, the code %q and a message\n%s", what, got, obj["error"], code, stdout)
	}
}

// TestDocClaim_Spec_EnvelopeKeysAlwaysPresent asserts the paragraph under
// the envelope shape in "1. Error handling" and the "Output" line of the
// `act migrate` section: error and message are in every envelope; details is
// too, except in the failures `act migrate` reports and in four of the
// failures of `act version --check-repo`; and a flag error from `act migrate`
// prints no JSON at all.
func TestDocClaim_Spec_EnvelopeKeysAlwaysPresent(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "sp9 first")
	noAct := brokencoHostRepo(t) // a git repo with no .act/
	notGit := t.TempDir()

	// The rule: three keys, details an object, {} when there is nothing.
	out, _ := pass5Run(t, dir, 3, "issue_not_found", "show", "--json", pass5NoSuchID)
	sp8Envelope(t, "act show of an unknown id", out, "issue_not_found")
	out, _ = pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--sort", "sp9-no-such-field")
	if _, keys := sp8Envelope(t, "act list --sort <unknown>", out, "bad_flag"); keys != "" || !strings.Contains(out, `"details":{}`) {
		t.Errorf("an envelope with nothing to add: details keys %q; want \"details\":{}\n%s", keys, out)
	}
	out, _ = pass5Run(t, notGit, 3, "not_in_git", "list", "--json")
	sp8Envelope(t, "act list outside a git repo", out, "not_in_git")

	// act migrate: error and message alone, on one stdout line, no --json.
	for _, tc := range []struct {
		where string
		exit  int
		code  string
		args  []string
	}{
		{notGit, 3, "not_in_git", []string{"--from", "1", "--to", "2"}},
		{dir, 2, "bad_input", []string{"--from", "2", "--to", "1"}},
		{dir, 2, "bad_input", nil},
		{dir, 5, "migration_not_found", []string{"--from", "1", "--to", "2"}},
	} {
		out, stderr := pass5Run(t, tc.where, tc.exit, tc.code, append([]string{"migrate"}, tc.args...)...)
		sp9Bare(t, "act migrate "+strings.Join(tc.args, " "), out, tc.code)
		if len(linesOf(out)) != 1 || stderr != "" {
			t.Errorf("act migrate %v: want one stdout line and an empty stderr\nstdout=%s\nstderr=%s", tc.args, out, stderr)
		}
	}
	// Every failure RunMigrate returns is the two-field MigrateError.
	src, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "op", "migrate.go"))
	if err != nil {
		t.Fatalf("read internal/op/migrate.go: %v", err)
	}
	if !strings.Contains(string(src), "type MigrateError struct {\n\tError   string `json:\"error\"`\n\tMessage string `json:\"message\"`\n}") {
		t.Errorf("internal/op/migrate.go: MigrateError is no longer the two fields error and message; the spec says act migrate prints no details")
	}
	// A flag error prints no JSON.
	for _, args := range [][]string{{"--sp9-no-such-flag"}, {"--from", "sp9", "--to", "2"}} {
		out, stderr, code := runActIn(t, dir, append([]string{"migrate"}, args...)...)
		if code != 2 || out != "" || !strings.Contains(stderr, "Usage of migrate:") {
			t.Errorf("act migrate %v: exit %d; want 2, an empty stdout and the flag usage text on stderr\nstdout=%s\nstderr=%s", args, code, out, stderr)
		}
	}

	// act version --check-repo: not_in_git and no_repo without details.
	out, _ = pass5Run(t, notGit, 3, "not_in_git", "version", "--check-repo", "--json")
	sp9Bare(t, "act version --check-repo outside a git repo", out, "not_in_git")
	out, _ = pass5Run(t, noAct, 3, "no_repo", "version", "--check-repo", "--json")
	sp9Bare(t, "act version --check-repo in a repo with no .act/", out, "no_repo")
	// walk_failed: an op directory that cannot be read.
	if os.Geteuid() != 0 {
		shard := filepath.Join(dir, ".act", "ops", id)
		if err := os.Chmod(shard, 0); err != nil {
			t.Fatalf("chmod %s: %v", shard, err)
		}
		out, _ = pass5Run(t, dir, 3, "walk_failed", "version", "--check-repo", "--json")
		if err := os.Chmod(shard, 0o755); err != nil {
			t.Fatalf("chmod %s back: %v", shard, err)
		}
		sp9Bare(t, "act version --check-repo over an unreadable op directory", out, "walk_failed")
	}
	// stat_failed is not provoked; hold the spec to the source that builds it.
	vsrc, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "cli", "version.go"))
	if err != nil {
		t.Fatalf("read internal/cli/version.go: %v", err)
	}
	if !strings.Contains(string(vsrc), "return map[string]any{\n\t\t\t\"error\":   \"stat_failed\",\n\t\t\t\"message\": err.Error(),\n\t\t}, 3") {
		t.Errorf("internal/cli/version.go no longer returns stat_failed as error and message alone")
	}

	// ... and its other two failures with details.
	pass5Rewrite(t, pass5CreateOp(t, dir, id), pass5WriterRe, `"writer_version":"99.0.0"`)
	out, _ = pass5Run(t, dir, 4, "version_skew", "version", "--check-repo", "--json")
	if _, keys := sp8Envelope(t, "act version --check-repo on skew", out, "version_skew"); keys != "binary_version,max_op_version,writer_version" {
		t.Errorf("version_skew details keys %q\n%s", keys, out)
	}
	bares := t.TempDir()
	brokencoTrackerBare(t, bares)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
	out, _ = pass5Run(t, noAct, 3, "tracker_not_checked_out", "version", "--check-repo", "--json")
	if _, keys := sp8Envelope(t, "act version --check-repo with an uncloned tracker", out, "tracker_not_checked_out"); keys != "recover,repo_root,source,tracker_remote" {
		t.Errorf("tracker_not_checked_out details keys %q\n%s", keys, out)
	}
}
