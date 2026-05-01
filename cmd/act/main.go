// Command act is the CLI entry point. It dispatches subcommands to the
// internal/cli package.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/cli"
	_ "github.com/aac/act/internal/fold" // registers op_version=1 in the op-package dispatch registry
	"github.com/aac/act/internal/mcp"
	"github.com/aac/act/internal/op"
)

func main() {
	if len(os.Args) < 2 {
		// Bare `act` invocation. Honour --json only if it was somehow passed
		// (it cannot be at this point, by definition, but guard anyway for
		// future flag parsing changes).
		emitBadFlag(false, "usage: act <subcommand> [flags]")
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	// Handle the nested `act dep <verb>` family before the flat-subcommand
	// switch. Currently only `dep add` is implemented; future verbs (rm,
	// list) plug in here.
	if sub == "dep" {
		if len(os.Args) < 3 {
			// Pre-flag-parse usage error: there's no chance to read --json,
			// so default to the human surface (stderr). Agents that pipe
			// `act dep` with --json appended after `add` already have to
			// dispatch through the verb branch below to see structured
			// output, so this fallback is correct.
			emitBadFlag(false, "act dep: usage: act dep <add> [args]")
			os.Exit(2)
		}
		verb := os.Args[2]
		rest := os.Args[3:]
		switch verb {
		case "add":
			os.Exit(runDepAdd(rest))
		default:
			// Same caveat as above: --json may live in `rest` but verb is
			// unknown, so we cannot promise to honour it. Probe rest for a
			// bare `--json` token to upgrade unknown-verb errors to the
			// structured envelope, mirroring the rest of the CLI.
			emitBadFlag(hasJSONFlag(rest), fmt.Sprintf("act dep %s: not implemented yet", verb))
			os.Exit(2)
		}
	}

	switch sub {
	case "init":
		os.Exit(runInit(args))
	case "version":
		os.Exit(runVersion(args))
	case "log":
		os.Exit(runLog(args))
	case "list":
		os.Exit(runList(args))
	case "search":
		os.Exit(runSearch(args))
	case "show":
		os.Exit(runShow(args))
	case "ready":
		os.Exit(runReady(args))
	case "create":
		os.Exit(runCreate(args))
	case "close":
		os.Exit(runClose(args))
	case "update":
		os.Exit(runUpdate(args))
	case "doctor":
		os.Exit(runDoctor(args))
	case "migrate":
		os.Exit(runMigrate(args))
	case "import":
		os.Exit(runImport(args))
	case "mcp":
		os.Exit(runMCP(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		emitBadFlag(hasJSONFlag(args), fmt.Sprintf("act %s: not implemented yet", sub))
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: act <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands: init, version, log, list, search, ready, show, create, close, update, dep add, doctor, import, mcp")
}

// runInit dispatches `act init`. It resolves the repo root from cwd, gathers
// machine-id + git email for node_id derivation, then delegates to RunInit.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "reinitialize even if .act/ already exists")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		// Surface as the same shape RunInit would emit so JSON consumers
		// see a single uniform error envelope.
		emitInit(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		}, false)
		return 3
	}

	out, code := cli.RunInit(root, *force, getMachineID(), getGitEmail(), nil)
	emitInit(*asJSON, out, code == 0)
	return code
}

// emitInit writes either a JSON document or a human-friendly summary depending
// on asJSON. For success, prints the canonical "Initialized" line; for errors,
// prints the unified envelope to stdout (JSON) or stderr (human).
func emitInit(asJSON bool, payload any, success bool) {
	if success {
		if asJSON {
			data, err := json.Marshal(payload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "act init: json marshal: %v\n", err)
				return
			}
			fmt.Println(string(data))
			return
		}
		m, ok := toMap(payload)
		if !ok {
			fmt.Fprintf(os.Stderr, "%v\n", payload)
			return
		}
		fmt.Printf("Initialized .act/ at %s with node_id %s\n", m["act_dir"], m["node_id"])
		return
	}
	emitEnvelope(asJSON, payload)
}

// emitEnvelope normalises any failure payload (typed struct or map) into the
// canonical {"error","message","details"} shape and writes it via cli.Emit.
// Use this from every error branch in main and the per-command run* helpers
// so the on-disk envelope is uniform across commands.
func emitEnvelope(asJSON bool, payload any) {
	env := cli.Normalize(payload)
	cli.Emit(env, asJSON, os.Stdout, os.Stderr)
}

// emitBadFlag is the fast path for usage errors raised before any command
// logic runs (missing positional, bad flag value). It always emits the
// canonical envelope; under --json the JSON goes to stdout, otherwise the
// human-readable message goes to stderr.
func emitBadFlag(asJSON bool, message string) {
	emitEnvelope(asJSON, map[string]any{
		"error":   cli.ErrBadFlag,
		"message": message,
	})
}

// hasJSONFlag scans a raw argv tail for "--json" or "-json", which is
// equivalent to flag.Parse's recognition. Used by pre-dispatch error
// branches (e.g. unknown `act dep <verb>`) to honour --json before the
// per-command FlagSet is even constructed.
func hasJSONFlag(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" {
			return true
		}
	}
	return false
}

// toMap round-trips an arbitrary struct through JSON to recover a string-keyed
// map; isolates main.go from cli's unexported output types.
func toMap(v any) (map[string]any, bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return m, true
}

// findRepoRoot walks upward from the current working directory looking for a
// `.git` entry (file or directory). The first hit's directory is the repo root.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getcwd: %w", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git/ found in %s or any parent", cwd)
		}
		dir = parent
	}
}

// getMachineID returns a stable per-host identifier. Order:
// 1. /etc/machine-id (Linux/systemd).
// 2. os.Hostname() if reachable.
// 3. A constant fallback so node_id derivation never fails.
func getMachineID() string {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		s := strings.TrimSpace(string(data))
		if s != "" {
			return s
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "act-unknown-machine"
}

// getGitEmail shells out to `git config user.email`. Empty string is returned
// when git is missing, the config is unset, or anything else goes wrong; the
// node_id derivation tolerates an empty email.
func getGitEmail() string {
	cmd := exec.Command("git", "config", "user.email")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runLog dispatches `act log <id>`. It resolves the repo root from cwd, then
// delegates to RunLog. Output rendering branches on --json.
func runLog(args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act log: usage: act log <id> [--json]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitLogError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunLog(root, idArg, *asJSON)
	if code != 0 {
		m, _ := toMap(out)
		emitLogError(*asJSON, m)
		return code
	}

	if *asJSON {
		data, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act log: json marshal: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.LogResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act log: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatLogHuman(res))
	return 0
}

// emitLogError renders the error envelope to stderr (human form) or stdout
// (JSON form). All command-specific emit*Error helpers now defer to the
// shared emitEnvelope so every CLI surface produces the same shape.
func emitLogError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// runSearch dispatches `act search <query>`. The repo root is resolved
// from cwd; flag parsing follows the universal pattern used by the other
// read commands.
func runSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	in := fs.String("in", "all", "FTS5 column scope: title|desc|all")
	status := fs.String("status", "", "comma-separated status filter")
	limit := fs.Int("limit", 50, "maximum number of results")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act search: usage: act search <query> [--in title|desc|all] [--status X] [--limit N] [--json]")
		return 2
	}
	query := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitSearchError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunSearch(root, query, cli.SearchOptions{
		In:     *in,
		Status: *status,
		Limit:  *limit,
		AsJSON: *asJSON,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitSearchError(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act search: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.SearchResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act search: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatSearchHuman(res))
	return 0
}

// emitSearchError mirrors emitLogError for the search subcommand.
func emitSearchError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// runList dispatches `act list`. Flag set mirrors spec §act list. Output
// rendering branches on --json: JSON renders the ListResult shape; the human
// path uses cli.FormatListHuman.
func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	status := fs.String("status", "", "comma-separated status filter (open,in_progress,blocked,closed)")
	assignee := fs.String("assignee", "", "exact-match assignee filter")
	typ := fs.String("type", "", "issue type filter (task|bug|epic|chore)")
	limit := fs.Int("limit", 200, "maximum number of issues to return")
	sortFlag := fs.String("sort", "", "comma-separated sort keys; prefix with - for desc; default priority,-created_at")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *limit == 0 {
		emitBadFlag(*asJSON, "act list: --limit 0 is not allowed")
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitListError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunList(root, cli.ListOptions{
		Status:   *status,
		Assignee: *assignee,
		Type:     *typ,
		Limit:    *limit,
		Sort:     *sortFlag,
		AsJSON:   *asJSON,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitListError(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act list: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.ListResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act list: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatListHuman(res))
	return 0
}

// emitListError mirrors emitLogError for the list subcommand.
func emitListError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// runMigrate dispatches the hidden `act migrate` subcommand. It is plumbed
// for forward compatibility with op-schema migrations (see issue act-5af9)
// but is not advertised in user docs while the registry remains empty.
//
// Output is always JSON: either a MigrateOutput payload on success or a
// MigrateError envelope on failure. Exit codes follow op.RunMigrate.
func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	from := fs.Int("from", 0, "source op_version (must be > 0)")
	to := fs.Int("to", 0, "target op_version (must be > from)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		data, _ := json.Marshal(map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		fmt.Println(string(data))
		return 3
	}

	out, code := op.RunMigrate(root, *from, *to)
	data, jerr := json.Marshal(out)
	if jerr != nil {
		fmt.Fprintf(os.Stderr, "act migrate: json marshal: %v\n", jerr)
		return 1
	}
	fmt.Println(string(data))
	return code
}

func runVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	checkRepo := fs.Bool("check-repo", false, "walk .act/ops/ and report max writer_version; exit 4 on skew")
	jsonOut := fs.Bool("json", false, "emit JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	repoRoot := ""
	if *checkRepo {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "act version: %v\n", err)
			return 1
		}
		repoRoot = cwd
	}

	out, code := cli.RunVersion(*checkRepo, repoRoot)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return code
	}

	// Human-friendly rendering.
	m, ok := out.(map[string]any)
	if !ok {
		fmt.Fprintf(os.Stderr, "act version: unexpected output\n")
		return 1
	}
	if code == 0 {
		fmt.Printf("act %s (writer %s)\n", m["binary_version"], m["writer_version"])
		if v, ok := m["max_op_version"]; ok {
			fmt.Printf("repo max writer_version: %v\n", v)
		}
		return 0
	}
	// Error path.
	if msg, ok := m["message"].(string); ok {
		fmt.Fprintf(os.Stderr, "act version: %s\n", msg)
	} else if errStr, ok := m["error"].(string); ok {
		fmt.Fprintf(os.Stderr, "act version: %s\n", errStr)
	}
	return code
}

// runShow dispatches `act show`. Positional `<id>` plus `--json` and
// `--include-ops` flags. Output rendering branches on --json: JSON renders
// the rendered-state map (or tombstone short-shape); the human path uses
// cli.FormatShowHuman.
func runShow(args []string) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	includeOps := fs.Bool("include-ops", false, "inline the HLC-sorted op stream alongside the snapshot")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act show: usage: act show <id> [--json] [--include-ops]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitShowError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunShow(root, cli.ShowOptions{
		ID:         idArg,
		AsJSON:     *asJSON,
		IncludeOps: *includeOps,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitShowError(*asJSON, m)
		return code
	}

	if *asJSON {
		var payload any
		switch v := out.(type) {
		case cli.ShowResult:
			payload = v.ShowJSON()
		case cli.ShowTombstoned:
			payload = v
		default:
			payload = v
		}
		data, jerr := json.Marshal(payload)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act show: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	fmt.Print(cli.FormatShowHuman(out))
	return 0
}

// emitShowError mirrors emitLogError for the show subcommand.
func emitShowError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// runMCP dispatches `act mcp`. It launches a stdio MCP server backed by
// the cli package. Flags:
//
//	--read-only       refuse all write tools regardless of per-call args
//	--workdir DIR     chdir before serving (overrides cwd for repo
//	                  resolution); required when launched outside a repo.
//
// Exit codes follow spec: 0 clean shutdown, 2 bad flag, 3 missing .act/.
func runMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	readOnly := fs.Bool("read-only", false, "refuse all write tools")
	workdir := fs.String("workdir", "", "chdir before serving; overrides cwd for repo resolution")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *workdir != "" {
		if err := os.Chdir(*workdir); err != nil {
			fmt.Fprintf(os.Stderr, "act mcp: chdir %s: %v\n", *workdir, err)
			return 2
		}
	}

	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "act mcp: %v\n", err)
		return 3
	}
	if _, err := os.Stat(filepath.Join(root, ".act")); err != nil {
		fmt.Fprintf(os.Stderr, "act mcp: missing .act/ at %s: run `act init` first\n", root)
		return 3
	}

	srv := mcp.NewServer(root, *readOnly, os.Stdin, os.Stdout)
	if err := srv.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "act mcp: %v\n", err)
		return 1
	}
	return 0
}
