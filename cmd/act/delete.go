package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runDelete dispatches `act delete <id>`. Positional argument is the id;
// flags follow the spec's universal write-flag set (`--reason TEXT`,
// `--cascade`, `--json`, `--no-commit`, `--push`, `--isolated`,
// `--verify`). The op_type emitted is `tombstone` per the spec's
// op-type table.
func runDelete(args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	reason := fs.String("reason", "", "free-text rationale (recorded in commit subject)")
	cascade := fs.Bool("cascade", false, "also tombstone all live descendants")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	offline := fs.Bool("offline", false, "commit locally, skip push; record in .act/.pending-pushes for retry on next non-offline write")
	branch := fs.String("branch", "", "branch in the nested .act/ repo to commit on and push to (default: current branch / tracking config). Worktree subagents pass --branch <worktree-branch> so op commits don't fan onto origin/main.")
	verify := fs.Bool("verify", false, "run pre-commit hooks during the commit")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act delete: usage: act delete <id> [--reason TEXT] [--cascade] [--json]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitDelete(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunDelete(root, cli.DeleteOptions{
		ID:       idArg,
		Reason:   *reason,
		Cascade:  *cascade,
		AsJSON:   *asJSON,
		NoCommit: *noCommit,
		Push:     *push,
		Isolated: *isolated,
		Offline:  *offline,
		Branch:   *branch,
		Verify:   *verify,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitDelete(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act delete: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.DeleteResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act delete: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatDeleteHuman(res))
	return 0
}

// emitDelete renders an error envelope for the delete subcommand.
// Delegates to the shared emitEnvelope helper so the JSON shape stays
// uniform across commands.
func emitDelete(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
