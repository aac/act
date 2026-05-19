// docclaim-sweep is a first-cut static analyzer that extracts behavior
// claims from cmd/act/*.go flag definitions and reports which are (or
// aren't) registered in internal/cli/docs_sweep_test.go's docClaimRegistry.
//
// Usage:
//
//	go run ./cmd/docclaim-sweep            # human-readable report
//	go run ./cmd/docclaim-sweep --json     # machine-readable JSON
//	go run ./cmd/docclaim-sweep --orphan-only  # only flags missing a registry entry
//
// What it catches: a new `fs.String(name, default, "help string")` (or
// fs.Bool / fs.Int / fs.Duration etc.) in cmd/act/*.go whose help string
// is not yet referenced as a `claimPattern` substring in any
// docClaimRegistry entry. The "agent added a flag and forgot to register
// the claim" failure mode.
//
// What it does NOT catch (out of scope; see act-2415 options 2 and 3):
//   - Behavior claims that live in cmd/act/help.go const-string blocks
//     ("must", "will", "guarantees", numbered loop steps).
//   - Behavior claims that live in README.md fenced shell-example
//     blocks (subcommand invocations needing a doctest).
//   - Behavior claims in docs/spec-v2.md prose.
//   - Help strings that are non-literal expressions (e.g. fmt.Sprintf).
//
// Boundary the analyzer asserts at: the literal help-string argument to
// the flag-definition call. If the help string is a constant or computed
// expression, the analyzer records "<non-literal>" and the entry will
// always appear orphan (acceptable first-cut behavior; a follow-up could
// resolve simple const references).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// flagDef is one fs.<Type>(name, default, help) call site discovered in
// cmd/act/*.go. Position is 1-indexed line, for human report rendering.
type flagDef struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	FuncName string `json:"func"` // fs.String / fs.Bool / ...
	FlagName string `json:"flag"`
	Help     string `json:"help"`
}

// claimEntry is a (claimPattern, registry-entry-name) pair extracted
// from docs_sweep_test.go. We only need the pattern for matching; the
// name is carried for diagnostic output.
type claimEntry struct {
	Name    string
	Pattern string
}

// report is what we emit; `--json` produces this as JSON.
type report struct {
	Total   int             `json:"total_flags"`
	Claimed int             `json:"claimed"`
	Orphans int             `json:"orphans"`
	NonLit  int             `json:"non_literal_help"`
	Flags   []flagReportRow `json:"flags"`
}

type flagReportRow struct {
	flagDef
	Claimed   bool     `json:"claimed"`
	MatchedBy []string `json:"matched_by,omitempty"` // registry entry names whose pattern is a substring of this help
}

func main() {
	asJSON := flag.Bool("json", false, "emit JSON instead of a human table")
	orphanOnly := flag.Bool("orphan-only", false, "only list flags with no matching registry entry")
	cmdDir := flag.String("cmd-dir", "cmd/act", "directory whose *.go files are scanned (relative to repo root)")
	registryFile := flag.String("registry", "internal/cli/docs_sweep_test.go", "registry file to parse for claimPatterns")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}

	flagDefs, err := extractFlagDefs(filepath.Join(root, *cmdDir))
	if err != nil {
		fail(err)
	}

	claims, err := extractClaimPatterns(filepath.Join(root, *registryFile))
	if err != nil {
		fail(err)
	}

	rep := buildReport(flagDefs, claims)

	if *asJSON {
		if *orphanOnly {
			rep.Flags = filterOrphans(rep.Flags)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fail(err)
		}
		return
	}
	renderHuman(os.Stdout, rep, *orphanOnly)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "docclaim-sweep:", err)
	os.Exit(1)
}

// repoRoot walks up from cwd looking for go.mod (the project root sentinel).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}

// extractFlagDefs walks the given directory (non-recursively), parses
// each non-test *.go file, and returns every fs.<Type>(name, default,
// help) call site it finds.
func extractFlagDefs(dir string) ([]flagDef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []flagDef
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "fs" {
				return true
			}
			fn := sel.Sel.Name
			// Match the type-keyed flag definers: String, Bool, Int, Int64,
			// Uint, Uint64, Float64, Duration. Skip *Var forms (StringVar,
			// BoolVar, IntVar, etc.) — they take (ptr, name, default, help)
			// and the help-string is typically duplicated from the matching
			// non-Var call. Skipping them avoids double-counting shorthand
			// aliases like `fs.IntVar(priority, "p", 2, "issue priority (shorthand)")`.
			if !isFlagDefiner(fn) {
				return true
			}
			if len(call.Args) < 3 {
				return true
			}
			flagName := litStringOr(call.Args[0], "<non-literal>")
			help := litStringOr(call.Args[len(call.Args)-1], "<non-literal>")
			pos := fset.Position(call.Pos())
			out = append(out, flagDef{
				File:     filepath.Base(path),
				Line:     pos.Line,
				FuncName: "fs." + fn,
				FlagName: flagName,
				Help:     help,
			})
			return true
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func isFlagDefiner(name string) bool {
	switch name {
	case "String", "Bool", "Int", "Int64", "Uint", "Uint64", "Float64", "Duration":
		return true
	}
	return false
}

// litStringOr returns the unquoted string value of expr if it is a basic
// string literal; otherwise returns def.
func litStringOr(expr ast.Expr, def string) string {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return def
	}
	// strconv.Unquote handles "double" and `back` quoted forms.
	if len(bl.Value) >= 2 {
		return unquote(bl.Value)
	}
	return def
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '`' && s[len(s)-1] == '`') {
			// For "..." strings we strip the quotes and process the simple
			// escapes we care about (\" and \\); for `...` strings we just
			// strip the backticks. Anything more exotic falls through.
			inner := s[1 : len(s)-1]
			if s[0] == '"' {
				inner = strings.ReplaceAll(inner, `\"`, `"`)
				inner = strings.ReplaceAll(inner, `\\`, `\`)
			}
			return inner
		}
	}
	return s
}

// extractClaimPatterns parses docs_sweep_test.go and pulls out every
// claimPattern string literal from struct composite literals shaped like
// `{ name: "...", docFile: "...", claimPattern: "...", testName: "..." }`.
// We don't need the full registry — only the patterns and their names.
func extractClaimPatterns(path string) ([]claimEntry, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []claimEntry
	ast.Inspect(file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var name, pattern string
		var hasPattern bool
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			val := litStringOr(kv.Value, "")
			switch key.Name {
			case "name":
				name = val
			case "claimPattern":
				pattern = val
				hasPattern = true
			}
		}
		if hasPattern && pattern != "" {
			out = append(out, claimEntry{Name: name, Pattern: pattern})
		}
		return true
	})
	return out, nil
}

func buildReport(flags []flagDef, claims []claimEntry) report {
	rep := report{Total: len(flags)}
	for _, f := range flags {
		row := flagReportRow{flagDef: f}
		if f.Help == "<non-literal>" {
			rep.NonLit++
		}
		for _, c := range claims {
			// A flag-help is "claimed" if any registry pattern is a substring
			// of the help (or vice-versa for very short patterns, though we
			// stick to substring-in-help to match the sweep test's semantics).
			if c.Pattern != "" && strings.Contains(f.Help, c.Pattern) {
				row.MatchedBy = append(row.MatchedBy, c.Name)
			}
		}
		row.Claimed = len(row.MatchedBy) > 0
		if row.Claimed {
			rep.Claimed++
		} else {
			rep.Orphans++
		}
		rep.Flags = append(rep.Flags, row)
	}
	return rep
}

func filterOrphans(rows []flagReportRow) []flagReportRow {
	out := rows[:0]
	for _, r := range rows {
		if !r.Claimed {
			out = append(out, r)
		}
	}
	return out
}

func renderHuman(w *os.File, rep report, orphanOnly bool) {
	fmt.Fprintf(w, "docclaim-sweep: %d flag definitions; %d claimed, %d orphan, %d non-literal help\n\n",
		rep.Total, rep.Claimed, rep.Orphans, rep.NonLit)
	header := "STATUS  FILE:LINE                          FUNC          FLAG                   HELP"
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)+40))
	for _, r := range rep.Flags {
		if orphanOnly && r.Claimed {
			continue
		}
		status := "ORPHAN"
		if r.Claimed {
			status = "OK    "
		}
		loc := fmt.Sprintf("%s:%d", r.File, r.Line)
		help := r.Help
		if len(help) > 80 {
			help = help[:77] + "..."
		}
		fmt.Fprintf(w, "%s  %-34s %-13s %-22s %s\n",
			status, loc, r.FuncName, truncate(r.FlagName, 22), help)
		if r.Claimed && len(r.MatchedBy) > 0 {
			fmt.Fprintf(w, "        matched by: %s\n", strings.Join(r.MatchedBy, ", "))
		}
	}
	if rep.Orphans > 0 && !orphanOnly {
		fmt.Fprintf(w, "\n%d orphan flag(s). Re-run with --orphan-only to focus.\n", rep.Orphans)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
