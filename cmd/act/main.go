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
	"time"

	"github.com/aac/act/internal/cli"
	_ "github.com/aac/act/internal/fold" // registers op_version=1 in the op-package dispatch registry
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/mcp"
	"github.com/aac/act/internal/op"
)

func main() {
	if len(os.Args) < 2 {
		// Bare `act` invocation. Honour --json only if it was somehow passed
		// (it cannot be at this point, by definition, but guard anyway for
		// future flag parsing changes).
		//
		// Per the UX-polish pass (act-f2c7 finding #1), a single-line
		// "usage: act <subcommand> [flags]" leaves a fresh agent no
		// concrete next step — they don't yet know what subcommands
		// exist. Print the same multi-line block `act --help` shows so
		// the subcommand list and the pointer to `act help` are visible
		// immediately.
		emitBadFlag(false, bareUsageMsg())
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	// CI-friendly no-state guard (Phase 1 delta item 8, act-37f7).
	//
	// When `act` is invoked from a directory whose host repo has no
	// `.act/` nested state (fresh clone, CI checkout, doc-only fork), we
	// branch on subcommand class:
	//
	//   - Read-only commands soft-exit 0 with a one-line message — the
	//     absence of state is expected and the agent loop should be able
	//     to no-op against it without failing CI.
	//   - Write commands hard-exit 3 with an actionable message pointing
	//     at `act init`. Silently no-op'ing on a write would be a class
	//     of silent-loss bug we don't want.
	//   - `init` is special: it creates the state, so it must proceed.
	//   - `version`, `help`, and the help short-flags are stateless and
	//     pass through.
	//   - `mcp` is its own special case (handled in runMCP).
	//
	// The guard fires only when we can resolve a host repo. If there's
	// no host repo at all, the per-command findRepoRoot call returns its
	// existing "not in a git working tree" error envelope and we don't
	// override that.
	if shouldCheckNoState(sub, args) {
		if root, err := findRepoRoot(); err == nil {
			actDir := filepath.Join(root, ".act")
			if _, serr := os.Stat(actDir); os.IsNotExist(serr) {
				if isReadOnlyNoStateCommand(sub) {
					fmt.Fprintln(os.Stderr, "act: no act state in this repo — this is normal in CI / fresh clones")
					os.Exit(0)
				}
				if isWriteNoStateCommand(sub) || isWriteDepInvocation(sub, args) {
					emitEnvelope(hasJSONFlag(args), map[string]any{
						"error":   "act_not_initialized",
						"message": `act: no act state — run "act init" to bootstrap`,
					})
					os.Exit(3)
				}
			}
		}
	}

	// Handle the nested `act dep <verb>` family before the flat-subcommand
	// switch. Currently only `dep add` is implemented; future verbs (rm,
	// list) plug in here.
	if sub == "dep" {
		if len(os.Args) < 3 {
			// Bare `act dep` — surface the verb list, not "not implemented".
			emitBadFlag(false, depUsageMsg())
			os.Exit(2)
		}
		verb := os.Args[2]
		rest := os.Args[3:]
		// `act dep --help` / `act dep -h` / any flag-shaped first token
		// route to the dep-level usage rather than the unknown-verb path,
		// which would misleadingly say "act dep --help: not implemented yet".
		if verb == "-h" || verb == "--help" || verb == "help" {
			fmt.Fprintln(os.Stderr, depUsageMsg())
			os.Exit(0)
		}
		if strings.HasPrefix(verb, "-") {
			emitBadFlag(hasJSONFlag(rest), depUsageMsg())
			os.Exit(2)
		}
		switch verb {
		case "add":
			os.Exit(runDepAdd(rest))
		default:
			// Unknown verb. --json may live in `rest`; honour it for the
			// envelope, mirroring the rest of the CLI surface.
			emitBadFlag(hasJSONFlag(rest), unknownDepVerbMsg(verb))
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
	case "reopen":
		os.Exit(runReopen(args))
	case "delete":
		os.Exit(runDelete(args))
	case "update":
		os.Exit(runUpdate(args))
	case "doctor":
		os.Exit(runDoctor(args))
	case "migrate":
		os.Exit(runMigrate(args))
	case "migrate-to-nested":
		os.Exit(runMigrateToNested(args))
	case "import":
		os.Exit(runImport(args))
	case "mcp":
		os.Exit(runMCP(args))
	case "mine":
		os.Exit(runMine(args))
	case "install-skill":
		os.Exit(runInstallSkill(args))
	case "state":
		os.Exit(runState(args))
	case "bootstrap-worker":
		os.Exit(runBootstrapWorker(args))
	case "harvest":
		os.Exit(runHarvest(args))
	case "remote":
		os.Exit(runRemote(args))
	case "-h", "--help":
		usage()
		os.Exit(0)
	case "help":
		os.Exit(runHelp(args))
	default:
		emitBadFlag(hasJSONFlag(args), unknownSubcommandMsg(sub))
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, bareUsageMsg())
}

// bareUsageMsg is the canonical usage block for a bare `act` invocation
// or `act --help` / `act -h`. It lists every implemented subcommand with
// comma separators (so the multi-word `dep add` does not look like three
// separate items — act-f2c7 finding #2) and points to `act help` for
// the full tutorial.
func bareUsageMsg() string {
	return "usage: act <subcommand> [flags]\n" +
		"subcommands: init, version, log, list, search, ready, show, create, close, reopen, delete, update, dep add, doctor, migrate-to-nested, import, mcp, mine, install-skill, state import, state export, remote\n" +
		"(run 'act help' for the full subcommand tutorial)"
}

// unknownSubcommandMsg returns the canonical "you typed a subcommand
// I don't recognise" message. Distinct from "not implemented yet"
// (which historically conflated unknown subcommands with stubs that
// genuinely existed in plan but not in code) — for v0.1 the entire
// subcommand surface is implemented, so any miss is a typo.
func unknownSubcommandMsg(sub string) string {
	return fmt.Sprintf("act: unknown subcommand %q; run 'act help' for the list", sub)
}

// depUsageMsg is the usage line shown when the dep family is invoked
// without a verb or with a flag-shaped first token. Lists every
// implemented verb so an agent knows what's available without
// consulting docs.
func depUsageMsg() string {
	return "act dep: usage: act dep <verb> [args]\nverbs: add"
}

// unknownDepVerbMsg is the unknown-verb message under `act dep`,
// kept in sync with unknownSubcommandMsg's tone.
func unknownDepVerbMsg(verb string) string {
	return fmt.Sprintf("act dep: unknown verb %q; run 'act dep --help' for the list", verb)
}

// runInit dispatches `act init`. It resolves the repo root from cwd, gathers
// machine-id + git email for node_id derivation, then delegates to RunInit.
//
// Phase 1 (docs/coordination-plane-design.md) removed the pre-existing
// `--no-commit` flag: the nested .act/ repo's initial commit is what `act
// init` does, and there's no flag-gated rollout. Existing repos use the
// migration tracked under act-603d.
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
		// Phase 1 two-repo bootstrap: surface what landed and what didn't.
		// `nested_committed` is the load-bearing piece (without it doctor
		// can't reconcile). The rest are best-effort host-side niceties.
		if nested, _ := m["nested_committed"].(bool); nested {
			fmt.Println(`Bootstrapped nested .act/ git repo with initial commit.`)
		}
		if hc, _ := m["host_committed"].(bool); hc {
			fmt.Println(`Committed host-side changes (.gitignore + CONTRIBUTING stanza).`)
		} else if gi, _ := m["gitignore_updated"].(bool); gi {
			fmt.Println(`Added .act/ to host .gitignore (commit pending; run git commit when ready).`)
		}
		if hi, _ := m["hook_installed"].(bool); hi {
			fmt.Println(`Installed host pre-commit hook to reject accidental .act/* stages.`)
		}
		if ce, _ := m["contributing_emitted"].(bool); ce {
			fmt.Println(`Appended Act-Id trailer stanza to CONTRIBUTING.md (public-looking remote detected).`)
		}
		if pf, ok := m["partial_failures"].([]any); ok && len(pf) > 0 {
			fmt.Fprintln(os.Stderr, "warning: some host-side steps did not complete:")
			for _, f := range pf {
				fmt.Fprintf(os.Stderr, "  - %v\n", f)
			}
			fmt.Fprintln(os.Stderr, "  nested .act/ is in place; re-run act init or remediate the listed steps manually.")
		}
		// Next-step hint (act-f2c7 finding #5). The previous one-liner
		// ("Run \"act create\" to file your first issue.") was a half-
		// step — it told the agent what to do next, but didn't name
		// the full canonical-loop tutorial. AGENTS.md filled the gap
		// when act ran inside its own repo; an agent doing `act init`
		// in a fresh project saw nothing about the loop. The "Next:"
		// hint surfaces both anchors in one line.
		fmt.Println(`Next: run 'act create "<title>"' to file your first issue, or 'act help workflow' for the canonical loop.`)
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

// readOnlyNoStateCommands enumerates the subcommands that soft-exit 0
// when the host repo has no `.act/` state. These are read-only and
// expected to no-op in CI / fresh clones (Phase 1 delta item 8).
//
// `version` and `help` are intentionally NOT in this set — they don't
// depend on state and should print their normal output regardless.
var readOnlyNoStateCommands = map[string]bool{
	"ready":  true,
	"list":   true,
	"show":   true,
	"doctor": true,
	"search": true,
	"log":    true,
	"mine":   true,
}

// writeNoStateCommands enumerates the subcommands that hard-exit 3 with
// an actionable error when the host repo has no `.act/` state. These
// would silently lose the user's intent if they no-op'd, so we surface
// the precondition gap explicitly (Phase 1 delta item 8). `init` is
// excluded because it is the bootstrap command.
var writeNoStateCommands = map[string]bool{
	"create": true,
	"update": true,
	"close":  true,
	"reopen": true,
	"delete": true,
	"import": true,
}

// shouldCheckNoState reports whether the guard runs for this subcommand
// at all. Subcommands not in either map (e.g. `init`, `mcp`, `version`,
// `help`, unknown subcommands) bypass the guard and go through the
// normal dispatch path. `dep` is a write surface but its dispatch lives
// in its own branch in main(); we treat write-shaped `dep` invocations
// (e.g. `dep add`) as writes here so the guard fires before that branch.
// Argparse-only `dep` paths — bare `dep`, `dep --help`, `dep <unknown-verb>`,
// `dep <flag-shaped-token>` — bypass the guard entirely because they
// error or print usage before touching any state (see act-993b93).
func shouldCheckNoState(sub string, args []string) bool {
	if readOnlyNoStateCommands[sub] || writeNoStateCommands[sub] {
		return true
	}
	return isWriteDepInvocation(sub, args)
}

// isWriteDepInvocation reports whether an `act dep ...` invocation is a
// state-mutating verb (currently only `dep add`) as opposed to an
// argparse-only path that errors or prints usage before touching state.
// The guard fires only for the former.
func isWriteDepInvocation(sub string, args []string) bool {
	if sub != "dep" {
		return false
	}
	if len(args) == 0 {
		return false // bare `act dep` → usage
	}
	verb := args[0]
	if verb == "-h" || verb == "--help" || verb == "help" {
		return false // help routes to usage
	}
	if strings.HasPrefix(verb, "-") {
		return false // flag-shaped first token routes to bad-flag/usage
	}
	switch verb {
	case "add":
		return true
	default:
		return false // unknown verb routes to unknown-verb error
	}
}

// isReadOnlyNoStateCommand reports whether sub should soft-exit 0 on
// absent state. See readOnlyNoStateCommands.
func isReadOnlyNoStateCommand(sub string) bool {
	return readOnlyNoStateCommands[sub]
}

// isWriteNoStateCommand reports whether sub should hard-exit 3 on
// absent state. See writeNoStateCommands.
func isWriteNoStateCommand(sub string) bool {
	return writeNoStateCommands[sub]
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

// findRepoRoot returns the host repo root by delegating to
// gitops.FindHostRepoRoot. Under Phase 1 the nearest .git walking upward from
// cwd may belong to the nested .act/.git; FindHostRepoRoot skips those and
// continues the walk so commands run from inside .act/ still resolve to the
// enclosing host repo (act-0852da).
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getcwd: %w", err)
	}
	return gitops.FindHostRepoRoot(cwd)
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

// runLog dispatches `act log [<id>] [--since D] [--by-issue ID] [--type T,T]`.
// It resolves the repo root from cwd, then delegates to RunLogOpts. Output
// rendering branches on --json. The positional <id> is now optional: omit
// it (and --by-issue) to walk every issue's op stream, e.g. paired with
// --since for a time-window retrospective across the whole backlog.
func runLog(args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	since := fs.String("since", "", "only include ops newer than this duration (e.g. 24h, 7d, 30m); prefix ok")
	byIssue := fs.String("by-issue", "", "only include ops for this issue id (full or unique prefix)")
	typeFlag := fs.String("type", "", "only include ops of these types (comma-separated, e.g. create,close)")
	summary := fs.Bool("summary", false, "render one line per op (timestamp, op_type, 8-char hash, summary) instead of full envelopes")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}

	idArg := ""
	if fs.NArg() >= 1 {
		idArg = fs.Arg(0)
	}
	if idArg == "" && *byIssue == "" && *since == "" && *typeFlag == "" && !*summary {
		emitBadFlag(*asJSON, "act log: usage: act log [<id>] [--since D] [--by-issue ID] [--type T[,T...]] [--summary] [--json]")
		return 2
	}

	opts := cli.LogOptions{ByIssue: *byIssue, Summary: *summary}
	if *since != "" {
		d, perr := parseSinceDuration(*since)
		if perr != nil {
			emitBadFlag(*asJSON, fmt.Sprintf("act log: --since %q: %v", *since, perr))
			return 2
		}
		opts.Since = d
	}
	if *typeFlag != "" {
		opts.Types = splitCSVArg(*typeFlag)
	}

	root, err := findRepoRoot()
	if err != nil {
		emitLogError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunLogOpts(root, idArg, *asJSON, opts)
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
	if *summary {
		fmt.Print(cli.FormatLogHumanSummary(res))
	} else {
		fmt.Print(cli.FormatLogHuman(res))
	}
	return 0
}

// emitLogError renders the error envelope to stderr (human form) or stdout
// (JSON form). All command-specific emit*Error helpers now defer to the
// shared emitEnvelope so every CLI surface produces the same shape.
func emitLogError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// parseSinceDuration parses a --since value into a time.Duration. It
// accepts everything time.ParseDuration accepts (ns, us/µs, ms, s, m,
// h) plus the bare "d" suffix for days (e.g. "7d") which is common in
// agent prompts and retrospective windows. A "d" form is translated to
// the equivalent hours; mixed units like "1d12h" are NOT supported (the
// expected use is single-token coarse windows — go's stdlib doesn't
// know "d" and we don't expand to a full parser for it).
func parseSinceDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "d") {
		nStr := strings.TrimSuffix(s, "d")
		// Disallow further unit chars; "1d2h" is rejected.
		if nStr == "" {
			return 0, fmt.Errorf("missing number before 'd'")
		}
		hours := nStr + "h"
		// time.ParseDuration accepts integers and floats in "h".
		d, err := time.ParseDuration(hours)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %v", err)
		}
		return d * 24, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %v", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return d, nil
}

// splitCSVArg splits a comma-separated flag value, trimming whitespace
// and dropping empty fields. Mirrors internal/cli.splitCSV but lives in
// main so the CLI shell doesn't reach into a private helper.
func splitCSVArg(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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
	commitMarker := fs.Bool("commit-marker", false, "emit just the Act-Id: act-XXXX commit-message trailer for this issue and exit")
	full := fs.Bool("full", false, "render description and closed_reason without truncation in human format (--json is always full)")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act show: usage: act show <id> [--json] [--include-ops] [--commit-marker] [--full]")
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
		Full:       *full,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitShowError(*asJSON, m)
		return code
	}

	// --commit-marker emits just the `Act-Id: act-XXXXXX` trailer string
	// the caller embeds in their work-commit message body (separated from
	// the subject by a blank line). Tombstoned issues have no commit marker
	// (the issue is gone), so we surface a clear error. Pre-act-c4c5 this
	// emitted `(act-XXXX)` subject-line form; the trailer is the only
	// emission shape now (docs/coordination-plane-design.md v2.1).
	if *commitMarker {
		switch v := out.(type) {
		case cli.ShowResult:
			short, _ := v.Fields["short_id"].(string)
			if short == "" {
				if id, ok := v.Fields["id"].(string); ok {
					short = id
				}
			}
			fmt.Println(cli.WorkCommitTrailerKey + ": " + short)
			return 0
		case cli.ShowTombstoned:
			emitShowError(*asJSON, map[string]any{
				"error":   "tombstoned",
				"message": fmt.Sprintf("act show: %s is tombstoned; no commit marker", v.ID),
			})
			return 3
		}
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
