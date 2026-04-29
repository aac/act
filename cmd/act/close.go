package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runClose dispatches `act close <id>`. Positional argument is the id;
// flags follow spec §3 (`--reason TEXT`, `--json`) plus the universal
// write flags (`--no-commit`, `--push`, `--isolated`).
func runClose(args []string) int {
	fs := flag.NewFlagSet("close", flag.ContinueOnError)
	reason := fs.String("reason", "", "closed reason (stored as closed_reason)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "act close: usage: act close <id> [--reason TEXT] [--json]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitClose(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunClose(root, cli.CloseOptions{
		ID:       idArg,
		Reason:   *reason,
		AsJSON:   *asJSON,
		NoCommit: *noCommit,
		Push:     *push,
		Isolated: *isolated,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitClose(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act close: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	switch v := out.(type) {
	case cli.CloseResult:
		fmt.Print(cli.FormatCloseHuman(v))
	case cli.CloseAlreadyClosed:
		fmt.Print(cli.FormatCloseAlreadyClosedHuman(v))
	default:
		fmt.Fprintf(os.Stderr, "act close: unexpected output type %T\n", out)
		return 1
	}
	return 0
}

// emitClose renders an error envelope for the close subcommand.
func emitClose(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act close: json marshal: %v\n", err)
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
