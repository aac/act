package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runReopen dispatches `act reopen <id>`. Positional argument is the id;
// flags follow the gap-issue spec (--reason TEXT, --json) plus the
// universal write flags (--no-commit, --push, --isolated, --verify).
func runReopen(args []string) int {
	fs := flag.NewFlagSet("reopen", flag.ContinueOnError)
	reason := fs.String("reason", "", "reopen reason (recorded in the op payload)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	offline := fs.Bool("offline", false, "commit locally, skip push; record in .act/.pending-pushes for retry on next non-offline write")
	branch := fs.String("branch", "", "branch in the nested .act/ repo to commit on and push to (default: current branch / tracking config). Worktree subagents pass --branch <worktree-branch> so op commits don't fan onto origin/main.")
	verify := fs.Bool("verify", false, "run git commit hooks rather than --no-verify")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON, "act reopen: usage: act reopen <id> [--reason TEXT] [--json]")
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitReopen(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunReopen(root, cli.ReopenOptions{
		ID:       idArg,
		Reason:   *reason,
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
		emitReopen(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act reopen: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	switch v := out.(type) {
	case cli.ReopenResult:
		fmt.Print(cli.FormatReopenHuman(v))
	case cli.ReopenAlreadyOpen:
		fmt.Print(cli.FormatReopenAlreadyOpenHuman(v))
	default:
		fmt.Fprintf(os.Stderr, "act reopen: unexpected output type %T\n", out)
		return 1
	}
	return 0
}

// emitReopen renders an error envelope for the reopen subcommand.
func emitReopen(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
