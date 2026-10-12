package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Doc-claim cover for the "On-disk layout" section of docs/spec.md
// (act-8a0322).
//
// What was stale: the tree listed a `.lock` file nothing writes, and the
// sentence under it said `.act/index.db` must be in `.gitignore` and
// "everything else under `.act/` is committed". The tree now lists every
// top-level path in three labelled groups, and this test compares each group
// with the code's own lists — the same ones nx2_toplevel_paths_test.go
// classifies against — so the spec and that classification cannot drift
// apart.

// sp7LayoutGroups parses the tree under "### On-disk layout". It returns the
// names at the top of `.act/` (trailing slash removed) keyed by the group
// they sit under: "tracked", "excluded" or "untracked", from the comment line
// that opens each group, and "" for names listed before the first group.
func sp7LayoutGroups(t *testing.T) map[string][]string {
	t.Helper()
	section := sp6Between(t, sp6SpecBody(t), "### On-disk layout\n", "\n#### ")
	tree := sp6Between(t, section, "```\n", "\n```")
	i := strings.Index(tree, "\n  .act/")
	if i < 0 {
		t.Fatalf("the layout tree has no `.act/` line:\n%s", tree)
	}
	groups := map[string][]string{}
	group := ""
	for _, line := range strings.Split(tree[i+1:], "\n")[1:] {
		// Top-level entries are indented exactly four spaces.
		if !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		body := strings.TrimPrefix(line, "    ")
		if strings.HasPrefix(body, "# ") {
			word, _, ok := strings.Cut(strings.TrimPrefix(body, "# "), ":")
			if !ok {
				t.Fatalf("layout tree: group comment %q has no `<group>:` label", line)
			}
			group = word
			continue
		}
		name := strings.TrimSuffix(strings.Fields(body)[0], "/")
		groups[group] = append(groups[group], name)
	}
	return groups
}

// sp7SameSet fails when got and want differ as sets, or got repeats a name.
func sp7SameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, "\n") != strings.Join(w, "\n") {
		t.Errorf("%s:\n  spec tree: %v\n  code:      %v", what, g, w)
	}
}

// TestDocClaim_Spec_LayoutTreeMatchesTopLevelPaths asserts the layout tree
// and the paragraph under it:
//
//   - "tracked" is exactly the names nx2TopLevel classes as tracked by design;
//   - "excluded" is exactly nestedExcludeEntries, the lines `act init` writes
//     to .act/.git/info/exclude;
//   - "untracked" is exactly the two quarantine directories;
//   - the only other name is `.git`, and `.lock` is nowhere;
//
// and then, on a real store, that the groups describe what is on disk: the
// host .gitignore carries `.act/`, the nested repo's committed top-level
// names are all "tracked", and everything else act left at the top of `.act/`
// is "excluded" and ignored by the nested repo.
func TestDocClaim_Spec_LayoutTreeMatchesTopLevelPaths(t *testing.T) {
	groups := sp7LayoutGroups(t)

	var tracked, quarantine []string
	for name, e := range nx2TopLevel {
		switch e.class {
		case nx2Tracked:
			tracked = append(tracked, name)
		case nx2Quarantine:
			quarantine = append(quarantine, name)
		}
	}
	var excluded []string
	for _, e := range nestedExcludeEntries {
		excluded = append(excluded, strings.TrimPrefix(e, "/"))
	}
	if len(tracked) < 5 || len(excluded) < 10 || len(quarantine) != 2 {
		t.Fatalf("the code's lists look wrong: tracked %v, excluded %v, quarantine %v", tracked, excluded, quarantine)
	}
	sp7SameSet(t, `the "tracked" group vs the paths nx2TopLevel classes as tracked`, groups["tracked"], tracked)
	sp7SameSet(t, `the "excluded" group vs nestedExcludeEntries`, groups["excluded"], excluded)
	sp7SameSet(t, `the "untracked" group vs the quarantine directories`, groups["untracked"], quarantine)
	sp7SameSet(t, "names listed before the first group", groups[""], []string{".git"})
	for g := range groups {
		if g != "" && g != "tracked" && g != "excluded" && g != "untracked" {
			t.Errorf("layout tree has a group %q this test does not know", g)
		}
	}
	// The path the old tree named: declared in config.LayoutPaths, written
	// by nothing (nx2TopLevel records the same finding).
	if e, ok := nx2TopLevel[".lock"]; !ok || e.class != nx2NotTopLevel {
		t.Errorf("nx2TopLevel no longer classes .lock as not-a-top-level-path (%+v); if something writes it now, the layout tree needs it", e)
	}

	// A real store.
	host := nx2InitStore(t)
	mustCreate(t, host, "sp7 layout probe")
	if out, code := RunList(host, ListOptions{}); code != 0 {
		t.Fatalf("RunList: code=%d out=%+v", code, out)
	}
	ignore, err := os.ReadFile(filepath.Join(host, ".gitignore"))
	if err != nil || !strings.Contains("\n"+string(ignore), "\n.act/\n") {
		t.Errorf("host .gitignore after act init has no `.act/` line (err %v):\n%s", err, ignore)
	}
	inGroup := func(g, name string) bool {
		for _, n := range groups[g] {
			if n == name {
				return true
			}
		}
		return false
	}
	nested := filepath.Join(host, ".act")
	out, err := exec.Command("git", "-C", nested, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files in the nested repo: %v", err)
	}
	committed := map[string]bool{}
	for _, p := range strings.Fields(string(out)) {
		top, _, _ := strings.Cut(p, "/")
		committed[top] = true
	}
	for _, want := range []string{"config.json", "ops", "hooks"} {
		if !committed[want] {
			t.Fatalf("precondition: the nested repo should track %s after init + create; tracked top-level names: %v", want, committed)
		}
	}
	for top := range committed {
		if !inGroup("tracked", top) {
			t.Errorf("the nested repo tracks %s, which the layout tree does not list under \"tracked\"", top)
		}
	}
	entries, err := os.ReadDir(nested)
	if err != nil {
		t.Fatalf("read .act: %v", err)
	}
	var working []string
	for _, e := range entries {
		if e.Name() == ".git" || committed[e.Name()] || inGroup("tracked", e.Name()) {
			continue
		}
		working = append(working, e.Name())
		if !inGroup("excluded", e.Name()) {
			t.Errorf("act left .act/%s on disk, uncommitted; the layout tree does not list it under \"excluded\"", e.Name())
		}
	}
	if len(working) < 2 {
		t.Fatalf("precondition: init + create + list should leave at least .write.lock and index.db; found %v", working)
	}
	for path, pattern := range nx2CheckIgnore(t, host, working) {
		if pattern == "" {
			t.Errorf(".act/%s is listed as excluded, but no line of .act/.git/info/exclude matches it", path)
		}
	}
}
