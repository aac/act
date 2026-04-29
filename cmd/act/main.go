// Command act is the CLI entry point. It dispatches subcommands to the
// internal/cli package.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/cli"
	_ "github.com/aac/act/internal/fold" // registers op_version=1 in the op-package dispatch registry
	"github.com/aac/act/internal/op"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "init":
		os.Exit(runInit(args))
	case "version":
		os.Exit(runVersion(args))
	case "log":
		os.Exit(runLog(args))
	case "search":
		os.Exit(runSearch(args))
	case "migrate":
		os.Exit(runMigrate(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "act %s: not implemented yet\n", sub)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: act <subcommand> [flags]")
	fmt.Fprintln(os.Stderr, "subcommands: init, version, log, search")
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
// prints the message text on stderr.
func emitInit(asJSON bool, payload any, success bool) {
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
	if success {
		fmt.Printf("Initialized .act/ at %s with node_id %s\n", m["act_dir"], m["node_id"])
		return
	}
	if msg, _ := m["message"].(string); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	fmt.Fprintf(os.Stderr, "%v\n", payload)
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "act log: usage: act log <id> [--json]")
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
// (JSON form).
func emitLogError(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act log: json marshal: %v\n", err)
			return
		}
		fmt.Println(string(data))
		return
	}
	if msg, _ := payload["message"].(string); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	if e, _ := payload["error"].(string); e != "" {
		fmt.Fprintln(os.Stderr, e)
	}
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "act search: usage: act search <query> [--in title|desc|all] [--status X] [--limit N] [--json]")
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
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act search: json marshal: %v\n", err)
			return
		}
		fmt.Println(string(data))
		return
	}
	if msg, _ := payload["message"].(string); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	if e, _ := payload["error"].(string); e != "" {
		fmt.Fprintln(os.Stderr, e)
	}
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
