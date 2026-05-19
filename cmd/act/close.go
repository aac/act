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
	reason := fs.String("reason", "", "closed reason (stored as closed_reason; max 500 bytes — see 'act help workflow' for cap rationale)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip staging and the auto-commit")
	push := fs.Bool("push", false, "push after the commit (errors if the close stays staged for the agent's next commit)")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	offline := fs.Bool("offline", false, "commit locally, skip push; record in .act/.pending-pushes for retry on next non-offline write")
	noCode := fs.Bool("no-code", false, "mark this close as producing no code change (tracking, wrong-claim, doc-only); doctor suppresses orphan-close warnings for these closes")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act close: usage: act close <id> [--reason TEXT] [--json]")
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
		Offline:  *offline,
		NoCode:   *noCode,
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

// emitClose renders an error envelope for the close subcommand. Delegates
// to the shared emitEnvelope helper so the JSON shape is uniform.
func emitClose(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
