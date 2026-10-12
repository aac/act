package cli

// act-70569e — every path act itself can write at the top of `.act/` is
// either tracked in the nested repo by design, or matched by the nested
// repo's `.git/info/exclude`.
//
// The names are gathered from the code rather than from a second
// hand-written list, so a new working file forces a decision here:
//
//  1. every field of config.LayoutPaths (the layout's own table);
//  2. every dotted string literal in non-test Go source (`".write.lock"`,
//     `".failed-ops"`, ...) — all of act's working files so far are
//     dotfiles, and a new one will be spelled as such a literal;
//  3. every `os.CreateTemp` pattern literal (the atomic-write temp files);
//  4. every literal that extends a layout file name (`"index.db-wal"`).
//
// Each gathered name must appear in nx2TopLevel with a class. The class
// is then checked against a real nested repo with `git check-ignore`.
//
// WHAT THIS DOES NOT SEE: a new top-level file that is not a dotfile,
// not in LayoutPaths, and built by concatenation or a format string
// (as `index.db.malformed-<ts>` is). Those are hand-listed in
// nx2Composed, and TestNX2_FreshStoreTopLevel below catches any that a
// plain init + write + read + doctor actually leaves on disk.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
)

type nx2Class int

const (
	// nx2Excluded: machine-local working state. Must be ignored by the
	// nested repo's info/exclude.
	nx2Excluded nx2Class = iota
	// nx2Tracked: travels with the tracker. Must NOT be ignored.
	nx2Tracked
	// nx2Quarantine: a directory act moves op files into. act never
	// stages it, and it is deliberately left visible in `git -C .act
	// status` — see the note on the two entries below.
	nx2Quarantine
	// nx2NotTopLevel: a gathered literal that is not a name at the top
	// of `.act/` at all (a file extension, a path elsewhere).
	nx2NotTopLevel
)

type nx2Entry struct {
	class nx2Class
	// sample is the path, relative to `.act/`, handed to git. Empty
	// means the name itself. A glob needs a concrete sample.
	sample string
	why    string
}

// nx2TopLevel classifies every gathered name. Keys are spelled through
// the code's own constant where one is exported to this package.
var nx2TopLevel = map[string]nx2Entry{
	// --- tracked by design -------------------------------------------
	"ops":         {class: nx2Tracked, sample: "ops/act-000000/2026-10/x-create.json", why: "the op log; every write stages its op file (gitops.StageOpFile)"},
	"config.json": {class: nx2Tracked, why: "written by init before the nested repo's first commit, which stages it (init.go bootstrapNestedRepo)"},
	"hooks":       {class: nx2Tracked, sample: "hooks/close", why: "init.go: close.sample is written so it 'lands in the nested repo's initial commit and thus travels into worktree workers'"},
	"snapshots":   {class: nx2Tracked, sample: "snapshots/act-000000.json", why: "compaction writes, stages and commits snapshot files into the nested repo (compact.go)"},
	"imports":     {class: nx2Tracked, sample: "imports/2026-10-12T00-00-00.000Z.json", why: "the importer stages and commits the mapping file; id resolution on every clone reads it (importer.go step 6)"},

	// --- excluded: machine-local working state -----------------------
	gitops.WriteLockFile:            {class: nx2Excluded, why: "flock target for the write lock"},
	".slow-writes":                  {class: nx2Excluded, why: "per-machine slow-write log"},
	".pending-pushes":               {class: nx2Excluded, why: "per-machine list of unpushed ops"},
	SyncLogFilename:                 {class: nx2Excluded, why: "per-machine sync log"},
	".compact.lock":                 {class: nx2Excluded, why: "flock target for compaction"},
	"index.db":                      {class: nx2Excluded, why: "derived SQLite read cache"},
	"index.db-wal":                  {class: nx2Excluded, why: "SQLite sidecar"},
	"index.db-shm":                  {class: nx2Excluded, why: "SQLite sidecar"},
	"index.db-journal":              {class: nx2Excluded, why: "SQLite sidecar"},
	"fold-checkpoint.json":          {class: nx2Excluded, why: "derived cache keyed to this checkout's ops tree (spec §5); removed with index.db when HEAD moves (cache.go)"},
	".doctor-rebuild.db":            {class: nx2Excluded, why: "doctor's scratch index for the divergence check"},
	BootstrapMetaFileName:           {class: nx2Excluded, why: "diagnostic stamp bootstrap-worker drops in a worker's own copy; names an absolute source path"},
	".tmp-*":                        {class: nx2Excluded, sample: ".tmp-123456", why: "os.CreateTemp pattern for the .slow-writes / .pending-pushes atomic rewrite, which runs in the state root"},
	"config-*.json.tmp":             {class: nx2Excluded, sample: "config-123456.json.tmp", why: "os.CreateTemp pattern for config.WriteConfig, in the state root"},
	"index.db.malformed-*":          {class: nx2Excluded, sample: "index.db.malformed-1760000000000000000", why: "doctor --fix-index's backup of a corrupt cache"},
	".doctor-rebuild.db-wal":        {class: nx2Excluded, why: "SQLite sidecar of doctor's scratch index (index.Open uses WAL)"},
	".doctor-rebuild.db-shm":        {class: nx2Excluded, why: "SQLite sidecar of doctor's scratch index"},
	SyncLogFilename + ".tmp":        {class: nx2Excluded, why: "temp file of the sync-log rewrite (remote_sync.go)"},
	"fold-checkpoint.json" + ".tmp": {class: nx2Excluded, why: "temp file of fold.WriteCheckpoint"},

	// --- quarantine: untracked, left visible --------------------------
	// act never stages either directory, and the code's comments say
	// they stay out of any tracked tree. Whether they should also be
	// hidden from `git -C .act status` is not answered by the code: that
	// listing is the one place an operator who did not see the original
	// error would notice a stranded op. They are kept OFF the exclude
	// list until that is decided; flip the class here when it is.
	failedOpQuarantineDir: {class: nx2Quarantine, sample: failedOpQuarantineDir + "/2026-10-12T00-00-00.000Z/ops/x.json", why: "ops whose commit failed, parked for the README recovery runbook (failed_op.go)"},
	".collisions":         {class: nx2Quarantine, sample: ".collisions/2026-10-12T00-00-00.000Z/ops/x.json", why: "local op files that differed from origin's (gitops/collision.go collisionQuarantineDir, unexported)"},

	// --- gathered literals that are not top-level `.act/` names ------
	".lock":              {class: nx2NotTopLevel, why: "config.LayoutPaths.CompactLock: declared but written by nothing; compaction locks .compact.lock"},
	".act":               {class: nx2NotTopLevel, why: "the state directory itself"},
	".act.bootstrap":     {class: nx2NotTopLevel, why: "bootstrap-worker staging dir, a sibling of .act/ in the host tree"},
	".git":               {class: nx2NotTopLevel, why: "git's own directory"},
	".gitignore":         {class: nx2NotTopLevel, why: "the host repo's ignore file"},
	".gitkeep":           {class: nx2NotTopLevel, why: "ops/.gitkeep, one level down and tracked"},
	".config":            {class: nx2NotTopLevel, why: "XDG config dir under $HOME"},
	".json":              {class: nx2NotTopLevel, why: "file extension"},
	".go":                {class: nx2NotTopLevel, why: "file extension"},
	".tmp":               {class: nx2NotTopLevel, why: "suffix; the names built from it are in nx2Composed"},
	".op-*.json.tmp":     {class: nx2NotTopLevel, why: "os.CreateTemp pattern used inside ops/<id>/<month>/"},
	".compact-*.tmp":     {class: nx2NotTopLevel, why: "os.CreateTemp pattern used inside snapshots/ and ops/"},
	".import-*.json.tmp": {class: nx2NotTopLevel, why: "os.CreateTemp pattern used inside imports/"},
	".url":               {class: nx2NotTopLevel, why: "git config key suffix (remote.<name>.url)"},
	".fetch":             {class: nx2NotTopLevel, why: "git config key suffix (remote.<name>.fetch)"},
	".receivepack":       {class: nx2NotTopLevel, why: "git config key suffix (remote.<name>.receivepack)"},
}

// nx2Composed are top-level names no single literal spells: the code
// builds them by concatenation or a format string, so the source scan
// cannot find them. Hand-listed for that reason only; each must still be
// classified in nx2TopLevel.
var nx2Composed = []string{
	"index.db.malformed-*",          // doctor.go: fmt.Sprintf("%s.malformed-%d", paths.IndexDB, ...)
	".doctor-rebuild.db-wal",        // SQLite, from index.Open(".doctor-rebuild.db")
	".doctor-rebuild.db-shm",        // same
	SyncLogFilename + ".tmp",        // remote_sync.go: syncLogPath + ".tmp"
	"fold-checkpoint.json" + ".tmp", // fold/checkpoint.go: path + ".tmp"
}

var nx2DotLiteral = regexp.MustCompile(`^\.[a-z][a-z0-9.-]*$`)

// nx2GatherNames returns every candidate name with one place it came
// from, for the failure message.
func nx2GatherNames(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	add := func(name, where string) {
		if _, ok := found[name]; !ok {
			found[name] = where
		}
	}

	// 1. The layout table.
	layout := reflect.ValueOf(config.Layout(string(filepath.Separator) + "nx2"))
	var layoutBases []string
	for i := 0; i < layout.NumField(); i++ {
		field := layout.Type().Field(i).Name
		if field == "Root" {
			continue
		}
		base := filepath.Base(layout.Field(i).String())
		layoutBases = append(layoutBases, base)
		add(base, "config.LayoutPaths."+field)
	}

	// 2-4. Literals in non-test source.
	root := repoRootForDocClaim(t)
	fset := token.NewFileSet()
	files := 0
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			if perr != nil {
				return perr
			}
			files++
			rel, _ := filepath.Rel(root, p)
			lit := func(e ast.Expr) (string, string, bool) {
				bl, ok := e.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					return "", "", false
				}
				s, uerr := strconv.Unquote(bl.Value)
				if uerr != nil {
					return "", "", false
				}
				return s, rel + ":" + strconv.Itoa(fset.Position(bl.Pos()).Line), true
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "CreateTemp" || len(x.Args) != 2 {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
						return true
					}
					if s, where, ok := lit(x.Args[1]); ok {
						add(s, where+" (os.CreateTemp pattern)")
					}
				case *ast.BasicLit:
					s, where, ok := lit(x)
					if !ok {
						return true
					}
					if nx2DotLiteral.MatchString(s) {
						add(s, where)
						return true
					}
					for _, base := range layoutBases {
						if strings.Contains(base, ".") && strings.HasPrefix(s, base) && s != base && !strings.ContainsAny(s, "/ ") {
							add(s, where)
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", top, err)
		}
	}
	if files < 50 {
		t.Fatalf("source scan read only %d files under %s; it is not looking at the codebase", files, root)
	}

	for _, name := range nx2Composed {
		add(name, "nx2Composed")
	}
	return found
}

// nx2CheckIgnore asks the nested repo which exclude pattern, if any,
// matches each path. The returned map holds the matching pattern, or ""
// for a path nothing ignores.
func nx2CheckIgnore(t *testing.T, host string, paths []string) map[string]string {
	t.Helper()
	args := append([]string{"-C", filepath.Join(host, ".act"), "check-ignore", "-v", "-n", "--"}, paths...)
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		// Exit 1 means "none of the paths is ignored", which is an
		// answer, not a failure.
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("git check-ignore: %v\n%s", err, out)
		}
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		// <source>:<linenum>:<pattern>\t<path>; all three empty when
		// nothing matched.
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("unparseable check-ignore line %q in:\n%s", line, out)
		}
		parts := strings.SplitN(meta, ":", 3)
		if len(parts) != 3 {
			t.Fatalf("unparseable check-ignore line %q in:\n%s", line, out)
		}
		got[path] = parts[2]
	}
	for _, p := range paths {
		if _, ok := got[p]; !ok {
			t.Fatalf("git check-ignore gave no verdict for %q; output:\n%s", p, out)
		}
	}
	return got
}

func nx2InitStore(t *testing.T) string {
	t.Helper()
	host := makeRepo(t)
	if out, code := RunInit(host, InitOptions{MachineID: "m", GitEmail: "u@example.com"}); code != 0 {
		t.Fatalf("RunInit: code=%d out=%+v", code, out)
	}
	return host
}

// TestNX2_EveryTopLevelPathIsClassified: each name the code can write at
// the top of `.act/` is classified, and git agrees with the class.
func TestNX2_EveryTopLevelPathIsClassified(t *testing.T) {
	names := nx2GatherNames(t)

	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	for _, n := range sorted {
		if _, ok := nx2TopLevel[n]; !ok {
			t.Errorf("%q (%s) is not classified. If act writes it at the top of .act/, decide whether it "+
				"travels with the tracker or is machine-local working state: add it to nx2TopLevel, and if it is "+
				"working state add it to nestedExcludeEntries in nested_exclude.go and to the nested-exclude row of docs/spec.md.",
				n, names[n])
		}
	}
	for n := range nx2TopLevel {
		if _, ok := names[n]; !ok {
			t.Errorf("nx2TopLevel classifies %q but the code no longer names it; remove the entry", n)
		}
	}

	host := nx2InitStore(t)
	sampleOf := func(n string) string {
		if s := nx2TopLevel[n].sample; s != "" {
			return s
		}
		return n
	}
	var paths []string
	for _, n := range sorted {
		if e, ok := nx2TopLevel[n]; ok && e.class != nx2NotTopLevel {
			paths = append(paths, sampleOf(n))
		}
	}
	verdict := nx2CheckIgnore(t, host, paths)

	usedPattern := map[string]bool{}
	for _, n := range sorted {
		e, ok := nx2TopLevel[n]
		if !ok || e.class == nx2NotTopLevel {
			continue
		}
		pattern := verdict[sampleOf(n)]
		switch e.class {
		case nx2Excluded:
			if pattern == "" {
				t.Errorf("%q is machine-local working state (%s) but no line of .act/.git/info/exclude matches .act/%s; add it to nestedExcludeEntries",
					n, e.why, sampleOf(n))
			}
			usedPattern[pattern] = true
		case nx2Tracked:
			if pattern != "" {
				t.Errorf("%q is tracked by design (%s) but exclude line %q hides .act/%s", n, e.why, pattern, sampleOf(n))
			}
		case nx2Quarantine:
			if pattern != "" {
				t.Errorf("%q is a quarantine directory kept visible in the nested status, but exclude line %q hides .act/%s; "+
					"if that is now the decision, change its class in nx2TopLevel", n, pattern, sampleOf(n))
			}
		}
	}

	// The reverse: no exclude line without a classified path behind it.
	for _, p := range nestedExcludeEntries {
		if !usedPattern[p] {
			t.Errorf("nestedExcludeEntries has %q, which matches no path classified as working state in nx2TopLevel", p)
		}
	}
}

// TestNX2_FreshStoreTopLevel: after init, one write that quarantines
// nothing, a read and a doctor run, everything actually on disk at the
// top of `.act/` is tracked by design or ignored, and the nested status
// is empty.
func TestNX2_FreshStoreTopLevel(t *testing.T) {
	host := nx2InitStore(t)
	mustCreate(t, host, "nx2 probe")
	if out, code := RunList(host, ListOptions{}); code != 0 {
		t.Fatalf("RunList: code=%d out=%+v", code, out)
	}
	if out, code := RunDoctor(host, DoctorOptions{}); code != 0 {
		t.Fatalf("RunDoctor: code=%d out=%+v", code, out)
	}

	entries, err := os.ReadDir(filepath.Join(host, ".act"))
	if err != nil {
		t.Fatalf("read .act: %v", err)
	}
	var onDisk, untracked []string
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		onDisk = append(onDisk, e.Name())
		if nx2TopLevel[e.Name()].class != nx2Tracked || nx2TopLevel[e.Name()].why == "" {
			untracked = append(untracked, e.Name())
		}
	}
	// Guard against passing because nothing was written.
	for _, want := range []string{gitops.WriteLockFile, "index.db", "config.json", "ops"} {
		if _, err := os.Stat(filepath.Join(host, ".act", want)); err != nil {
			t.Fatalf("precondition: .act/%s should exist after init + create + list; on disk: %v", want, onDisk)
		}
	}
	for path, pattern := range nx2CheckIgnore(t, host, untracked) {
		if pattern == "" {
			t.Errorf("act left .act/%s on disk; it is neither tracked by design (nx2TopLevel) nor matched by .act/.git/info/exclude", path)
		}
	}

	cmd := exec.Command("git", "-C", filepath.Join(host, ".act"), "status", "--short", "--untracked-files=all")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status in nested repo: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("nested repo status after init + create + list + doctor should be empty; on disk %v; status:\n%s", onDisk, out)
	}
}
