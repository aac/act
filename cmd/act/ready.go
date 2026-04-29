package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
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
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
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

	out, code := cli.RunReady(root, cli.ReadyOptions{
		Under:  *under,
		Limit:  *limit,
		AsJSON: *asJSON,
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
// or stdout (JSON form).
func emitReadyError(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act ready: json marshal: %v\n", err)
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
