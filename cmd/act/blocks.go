package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runBlocks / runBlockedBy dispatch `act blocks <id>` and `act blocked-by
// <id>`. Both emit a bare, newline-separated list of issue ids to stdout —
// the pipe-composable shell affordance two reviewers flagged when act-00e5cc
// was scoped (act-2e1070). There is deliberately no --json flag: `act show
// <id> --json` is the structured surface; these are raw for `| xargs`,
// `$(...)`, and `while read`.
//
// Lives in its own file so concurrent edits to main.go's dispatch table do
// not collide with this wiring.
func runBlocks(args []string) int    { return runBlocksLike(args, "blocks") }
func runBlockedBy(args []string) int { return runBlocksLike(args, "blocked-by") }

func runBlocksLike(args []string, cmd string) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: act %s <id>\n", cmd)
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		emitBadFlag(false, fmt.Sprintf("act %s: usage: act %s <id>", cmd, cmd))
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitEnvelope(false, map[string]any{"error": "not_in_git", "message": err.Error()})
		return 3
	}

	var out []string
	var e *cli.BlocksErrorOutput
	var code int
	if cmd == "blocks" {
		out, e, code = cli.RunBlocks(root, rest[0])
	} else {
		out, e, code = cli.RunBlockedBy(root, rest[0])
	}
	if code != 0 {
		payload := map[string]any{"error": e.Error, "message": e.Message}
		if len(e.Candidates) > 0 {
			payload["candidates"] = e.Candidates
		}
		emitEnvelope(false, payload)
		return code
	}
	for _, id := range out {
		fmt.Println(id)
	}
	return 0
}
