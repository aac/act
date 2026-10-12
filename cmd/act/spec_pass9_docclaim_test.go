package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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
