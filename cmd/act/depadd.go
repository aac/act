package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// runDepAdd dispatches `act dep add`. Three input forms are supported,
// all writing the same underlying (child, parent, edge_type) tuple:
//
//	act dep add <a> <b> [--type T]      # positional: a is child of b
//	act dep add <a> --blocked-by <b>    # a is blocked by b (a child, b parent)
//	act dep add <a> --blocks <b>        # a blocks b (b child, a parent)
//
// The directional flag aliases imply --type blocks; mixing them with
// --type=<non-blocks> is exit 2. Argv has already been advanced past
// `act dep add`, so `args` starts at the first user-supplied token.
func runDepAdd(args []string) int {
	fs := flag.NewFlagSet("dep add", flag.ContinueOnError)
	// Direction primer (act-982a): the positional form
	// `act dep add A B --type blocks` means "A is blocked by B; A is
	// hidden from `act ready` until B closes". This is the canonical
	// child-then-parent ordering — A is the child whose deps[] grows
	// by an entry pointing at B (the blocker). The output strings on
	// `act dep add` and `act show` read in this direction (e.g.
	// "Added: A is blocked by B" and `dep: blocked-by B`).
	// Inspection hint: inspect with act show <id> (human) or act show <id> --json (blocked_by / blocks arrays).
	typ := fs.String("type", "blocks", "edge type (blocks|relates|supersedes); for blocks, `act dep add A B --type blocks` means A is blocked by B; A is hidden from ready until B closes; inspect with act show <id> (human) or act show <id> --json (blocked_by / blocks arrays)")
	blockedBy := fs.String("blocked-by", "", "directional alias: <a> --blocked-by <b> means a is blocked by b")
	blocks := fs.String("blocks", "", "directional alias: <a> --blocks <b> means a blocks b")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip the auto-commit")
	push := fs.Bool("push", false, "push after the commit")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	offline := fs.Bool("offline", false, "commit locally, skip push; record in .act/.pending-pushes for retry on next non-offline write")
	branch := fs.String("branch", "", "branch in the nested .act/ repo to commit on and push to (default: current branch / tracking config). Worktree subagents pass --branch <worktree-branch> so op commits don't fan onto origin/main.")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}

	// Resolve the three input forms into (child, parent, edgeType).
	// Exactly one of the directional flags or the second positional may
	// be present; bare-flag and non-blocks --type combinations are bad
	// flags.
	child, parent, edgeType, usageCode, usageMsg := resolveDepAddArgs(fs, *typ, *blockedBy, *blocks)
	if usageCode != 0 {
		emitBadFlag(*asJSON, usageMsg)
		return usageCode
	}

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
		EdgeType: edgeType,
		AsJSON:   *asJSON,
		NoCommit: *noCommit,
		Push:     *push,
		Isolated: *isolated,
		Offline:  *offline,
		Branch:   *branch,
	})
	if code != 0 {
		// Cycle output is normalised through the canonical envelope so it
		// matches the spec shape `{"error":"cycle","message":"...","details":
		// {"path":[...]}}`. The legacy nested-error shape is no longer
		// emitted at the cmd boundary.
		if cyc, ok := out.(cli.DepAddCycleOutput); ok {
			path := append([]string(nil), cyc.Error.Path...)
			env := cli.New(
				cli.ErrCycle,
				fmt.Sprintf("act dep add: cycle detected: %s", strings.Join(path, " -> ")),
				map[string]any{"path": path},
			)
			cli.Emit(env, *asJSON, os.Stdout, os.Stderr)
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

// emitDepAdd renders an error envelope for the dep add subcommand. Delegates
// to the shared emitEnvelope helper so the JSON shape matches the rest of
// the CLI surface.
func emitDepAdd(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}

// resolveDepAddArgs reconciles the three accepted input forms into a
// canonical (child, parent, edgeType) triple. It returns a non-zero
// usage exit code (always 2, the bad-flag code) and a human-readable
// message when the inputs are mutually inconsistent.
//
// Forms accepted:
//
//	positional:   <a> <b>                  → child=a, parent=b, edge=--type|blocks
//	blocked-by:   <a> --blocked-by <b>     → child=a, parent=b, edge=blocks
//	blocks:       <a> --blocks <b>         → child=b, parent=a, edge=blocks
//
// Direction flags mutually exclude each other and the second positional
// (you cannot say `act dep add a b --blocked-by c`). They imply the
// blocks edge type; a non-default --type alongside them is an error.
func resolveDepAddArgs(fs *flag.FlagSet, typ, blockedBy, blocks string) (child, parent, edgeType string, code int, msg string) {
	hasBlockedBy := blockedBy != ""
	hasBlocks := blocks != ""

	if hasBlockedBy && hasBlocks {
		return "", "", "", 2, "act dep add: --blocked-by and --blocks are mutually exclusive"
	}

	// At least one positional (the subject id) is always required.
	if fs.NArg() < 1 {
		return "", "", "", 2, depAddUsage()
	}
	subject := fs.Arg(0)

	if hasBlockedBy || hasBlocks {
		// Direction flags supply the second id; a second positional
		// would be ambiguous.
		if fs.NArg() > 1 {
			return "", "", "", 2, "act dep add: cannot mix a second positional id with --blocked-by/--blocks"
		}
		// Direction flags imply edge type "blocks". If the caller also
		// passed --type with a non-blocks value, that's a contradiction;
		// --type=blocks is fine (redundant but consistent).
		if typ != "" && typ != "blocks" {
			return "", "", "", 2, fmt.Sprintf("act dep add: --blocked-by/--blocks imply --type blocks; got --type %q", typ)
		}
		if hasBlockedBy {
			// `<a> --blocked-by <b>` → a is blocked by b → a is the
			// child whose deps[] grows by an entry pointing at b.
			return subject, blockedBy, "blocks", 0, ""
		}
		// `<a> --blocks <b>` → a blocks b → b is the child whose deps[]
		// grows by an entry pointing at a.
		return blocks, subject, "blocks", 0, ""
	}

	// Positional form: need both ids.
	if fs.NArg() < 2 {
		return "", "", "", 2, depAddUsage()
	}
	return subject, fs.Arg(1), typ, 0, ""
}

// depAddUsage is the canonical usage line, kept in one place so the
// positional-arity error and the help-on-bad-flag path stay in sync.
func depAddUsage() string {
	return "act dep add: usage: act dep add <a> <b> [--type T]  |  act dep add <a> --blocked-by <b>  |  act dep add <a> --blocks <b>"
}
