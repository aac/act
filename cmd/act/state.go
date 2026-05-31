package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/aac/act/internal/cli"
)

// runState dispatches the `act state <verb>` family. The state verbs move
// act's own state into or out of a plain directory — directory-scoped and
// worktree-blind. act serializes its op log + config + snapshots into/out
// of the named directory with no knowledge of what the directory is for
// (a git worktree, a sandbox, a backup, etc.). Only the caller knows.
//
//	act state import <dir>   seed <dir>/.act/ with this repo's state
//	act state export <dir>   pull new ops from <dir>/.act/ back into this repo
//
// The copy/fold/atomic-rename/provenance mechanics live in internal/cli;
// these dispatchers are the worktree-blind surface over them. The legacy
// `act bootstrap-worker` / `act harvest` verbs remain as thin deprecation
// aliases that delegate here (see bootstrap_worker.go and harvest.go).
func runState(args []string) int {
	if len(args) == 0 {
		emitBadFlag(false, stateUsageMsg())
		return 2
	}
	verb := args[0]
	rest := args[1:]
	if verb == "-h" || verb == "--help" || verb == "help" {
		fmt.Fprintln(os.Stderr, stateUsageMsg())
		return 0
	}
	if strings.HasPrefix(verb, "-") {
		emitBadFlag(hasJSONFlag(rest), stateUsageMsg())
		return 2
	}
	switch verb {
	case "import":
		return runStateImport(rest)
	case "export":
		return runStateExport(rest)
	default:
		emitBadFlag(hasJSONFlag(rest), unknownStateVerbMsg(verb))
		return 2
	}
}

// stateUsageMsg is the canonical usage block for a bare `act state` or
// `act state --help`. Worktree-blind by construction.
func stateUsageMsg() string {
	return "usage: act state <verb> [flags]\n" +
		"verbs:\n" +
		"  import <dir>   seed <dir>/.act/ with this repo's state\n" +
		"  export <dir>   pull new ops from <dir>/.act/ back into this repo\n" +
		"(run 'act state import --help' or 'act state export --help' for flags)"
}

func unknownStateVerbMsg(verb string) string {
	return fmt.Sprintf("act state: unknown verb %q\n%s", verb, stateUsageMsg())
}

// runStateImport dispatches `act state import <dir>`. It copies this repo's
// `.act/` state tree into <dir>/.act/ so a process working out of that
// directory can run act commands against state that mirrors this repo's
// view. Directory-scoped: act does not care what <dir> is for.
//
// Modes (all directory-scoped — no worktree vocabulary):
//
//	act state import <dir> [--force] [--json]
//	    Copy this repo's `.act/` (resolved from cwd) into <dir>/.act/.
//
//	act state import --from-remote <url> <dir>
//	    [--force] [--json] [--timeout-seconds N]
//	    Clone <url> --depth 1 into a staging dir, atomic-rename to
//	    <dir>/.act/, stamp act.role=worker, validate via `act ready`.
//
//	act state import --from-cwd <source-path> [<dir>]
//	    [--force] [--json]
//	    Inverts source and target: run from inside the destination
//	    directory, name <source-path> as the source, and the target
//	    defaults to cwd. Copies the op log + config + snapshots + imports
//	    + nested .act/.git but NOT a live index.db; rebuilds the index
//	    locally. This is the documented replacement for a raw
//	    `cp -r <source>/.act .`, which copied a possibly-locked live
//	    index.db and caused silent op-write loss.
func runStateImport(args []string) int {
	fs := flag.NewFlagSet("state import", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing non-empty <dir>/.act/")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	fromRemote := fs.String("from-remote", "", "clone .act/ from this git URL instead of copying from cwd")
	fromCWD := fs.String("from-cwd", "", "inverted mode: copy from this source path into cwd (target defaults to cwd), rebuilding index.db locally instead of copying a live one")
	timeoutSeconds := fs.Int("timeout-seconds", 0, "override act.bootstrapTimeoutSeconds for --from-remote (0 = use config / default 30s)")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	// In --from-cwd mode the target is OPTIONAL (defaults to cwd); every
	// other mode requires the positional <dir>.
	if *fromCWD == "" && fs.NArg() < 1 {
		emitBadFlag(*asJSON,
			"act state import: usage: act state import [--from-remote <url> | --from-cwd <source-path>] <dir> [--force] [--json] [--timeout-seconds N]")
		return 2
	}
	target := ""
	if fs.NArg() >= 1 {
		target = fs.Arg(0)
	}

	out, code := cli.RunBootstrapWorker(cli.BootstrapWorkerOptions{
		Target:            target,
		Force:             *force,
		AsJSON:            *asJSON,
		FromRemoteURL:     *fromRemote,
		FromCWDSourcePath: *fromCWD,
		TimeoutSeconds:    *timeoutSeconds,
		CmdName:           "act state import",
	})

	if code != 0 {
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act state import: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.BootstrapWorkerResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act state import: unexpected output type %T\n", out)
		return 1
	}
	fmt.Printf("Imported .act/ state into %s\n", res.Target)
	fmt.Printf("  source:    %s\n", res.SourceRoot)
	fmt.Printf("  ops:       %d files\n", res.OpsCopied)
	fmt.Printf("  snapshots: %d files\n", res.SnapshotsCopied)
	switch {
	case *fromRemote != "":
		fmt.Printf("  dispatch_hlc: %s\n", res.DispatchHLC)
		fmt.Printf("  act.role:    worker\n")
	case *fromCWD != "":
		fmt.Printf("  dispatch_hlc recorded in %s/.act/%s\n", res.Target, cli.BootstrapMetaFileName)
		fmt.Printf("  index.db:    rebuilt locally (live index not copied)\n")
	default:
		fmt.Printf("  dispatch_hlc recorded in %s/.act/%s\n", res.Target, cli.BootstrapMetaFileName)
	}
	return 0
}

// runStateExport dispatches `act state export <dir> [--dry-run] [--json]`.
// It pulls new ops from <dir>/.act/ops/ back into this repo's `.act/`,
// commits them on the nested `.act/.git`, and re-folds the index. Does NOT
// push — that's the caller's responsibility. Directory-scoped: act does
// not care what <dir> is for.
func runStateExport(args []string) int {
	fs := flag.NewFlagSet("state export", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be exported without writing or committing")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON,
			"act state export: usage: act state export <dir> [--dry-run] [--json]")
		return 2
	}
	dir := fs.Arg(0)

	out, code := cli.RunHarvest(cli.HarvestOptions{
		WorkerPath: dir,
		DryRun:     *dryRun,
		AsJSON:     *asJSON,
		CmdName:    "act state export",
	})

	if code != 0 {
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act state export: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.HarvestResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act state export: unexpected output type %T\n", out)
		return 1
	}
	if res.DryRun {
		fmt.Printf("Would export %d ops from %s (dry-run; no writes)\n", len(res.HarvestedOps), dir)
	} else if len(res.HarvestedOps) == 0 {
		fmt.Printf("Exported 0 ops from %s (already in sync)\n", dir)
	} else {
		fmt.Printf("Exported %d ops from %s\n", len(res.HarvestedOps), dir)
		fmt.Printf("  commit:           %s\n", res.CommitMessage)
		fmt.Printf("  issues indexed:   %d\n", res.FoldDiffSummary.IssuesIndexed)
	}
	if len(res.SkippedOps) > 0 {
		fmt.Printf("  skipped (already present): %d\n", len(res.SkippedOps))
	}
	if res.FoldError != "" {
		fmt.Fprintf(os.Stderr, "warning: fold failed after export commit: %s\n", res.FoldError)
		fmt.Fprintln(os.Stderr, "  the op log is the source of truth; run `act doctor` or `act list` to rebuild the index.")
	}
	return 0
}
