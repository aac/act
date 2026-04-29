package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runDepAdd dispatches `act dep add <child> <parent>`. Positional args
// are the child and parent ids; flags follow the spec §3 form
// (`--type T`, `--json`) plus the universal write flags. Argv has
// already been advanced past `act dep add`, so `args` starts at the
// first user-supplied token (typically the child id).
func runDepAdd(args []string) int {
	fs := flag.NewFlagSet("dep add", flag.ContinueOnError)
	typ := fs.String("type", "blocks", "edge type (blocks|relates|supersedes)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "act dep add: usage: act dep add <child> <parent> [--type T] [--json]")
		return 2
	}
	child := fs.Arg(0)
	parent := fs.Arg(1)

	root, err := findRepoRoot()
	if err != nil {
		emitDepAdd(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunDepAdd(root, cli.DepAddOptions{
		Child:    child,
		Parent:   parent,
		EdgeType: *typ,
		AsJSON:   *asJSON,
		NoCommit: *noCommit,
		Push:     *push,
		Isolated: *isolated,
	})
	if code != 0 {
		// Cycle output is structured differently from bad-flag/not-found:
		// pass it through verbatim so the JSON payload preserves the
		// `{"error":{"kind":"cycle","path":[...]}}` shape.
		if cyc, ok := out.(cli.DepAddCycleOutput); ok {
			if *asJSON {
				data, jerr := json.Marshal(cyc)
				if jerr != nil {
					fmt.Fprintf(os.Stderr, "act dep add: json marshal: %v\n", jerr)
					return 1
				}
				fmt.Println(string(data))
				return code
			}
			fmt.Fprintf(os.Stderr, "act dep add: cycle detected: %v\n", cyc.Error.Path)
			return code
		}
		m, _ := toMap(out)
		emitDepAdd(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act dep add: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.DepAddResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act dep add: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatDepAddHuman(res))
	return 0
}

// emitDepAdd renders an error envelope for the dep add subcommand.
func emitDepAdd(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act dep add: json marshal: %v\n", err)
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
