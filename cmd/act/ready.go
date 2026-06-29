package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/config"
)

// runReady dispatches `act ready`. It resolves the repo root from cwd,
// then delegates to cli.RunReady. Output rendering branches on --json.
//
// The function lives in its own file so concurrent edits to main.go's
// dispatch table do not collide with `act ready` wiring.
func runReady(args []string) int {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	under := fs.String("under", "", "restrict to descendants of the given issue id (prefix ok)")
	limit := fs.Int("limit", 50, "maximum number of issues to return")
	mine := fs.Bool("mine", false, "filter to issues already assigned to the calling node")
	as := fs.String("as", "", "override identity for --mine; defaults to .act/config.json node_id")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	// Phase 2 ticket 5: --fresh forces a fetch+rebase of .act/.git
	// before reading state. --no-cache is a flag-for-flag alias with
	// dispatch-identical behavior; the dual surface exists so agents
	// reaching for the "no cache" idiom find a working flag without
	// having to learn act's preferred spelling. The cache check is also
	// bypassed by ACT_DISPATCH_MODE=1 in the environment.
	fresh := fs.Bool("fresh", false, "bypass the read-path TTL cache and fetch+rebase before reading")
	noCache := fs.Bool("no-cache", false, "alias for --fresh: bypass the read-path TTL cache and fetch+rebase")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *as != "" && !*mine {
		emitBadFlag(*asJSON, "act ready: --as requires --mine")
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitReadyError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	// Resolve identity for --mine. Default reads .act/config.json node_id;
	// --as <id> overrides without touching config (useful for ad-hoc
	// "what's <agent-x> working on?" queries).
	assigneeFilter := ""
	if *mine {
		assigneeFilter = *as
		if assigneeFilter == "" {
			paths := config.Layout(root)
			cfg, cerr := config.ReadConfig(paths)
			if cerr != nil {
				emitReadyError(*asJSON, map[string]any{
					"error":   "no_repo",
					"message": fmt.Sprintf("act ready: --mine cannot read .act/config.json: %v; run 'act init' first or pass --as <id>", cerr),
				})
				return 3
			}
			assigneeFilter = cfg.NodeID
		}
	}

	out, code := cli.RunReady(root, cli.ReadyOptions{
		Under:          *under,
		Limit:          *limit,
		AssigneeFilter: assigneeFilter,
		AsJSON:         *asJSON,
		Fresh:          *fresh || *noCache,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitReadyError(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act ready: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.ReadyResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act ready: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatReadyHuman(res))
	return 0
}

// emitReadyError renders the ready error envelope to stderr (human form)
// or stdout (JSON form). Delegates to the shared emitEnvelope helper.
func emitReadyError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
