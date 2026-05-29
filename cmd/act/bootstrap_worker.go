package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runBootstrapWorker dispatches `act bootstrap-worker`.
//
// Two modes:
//
//	act bootstrap-worker <target-path> [--force] [--json]
//	    Phase 1.5 cwd-source path: copy the host repo's `.act/` tree
//	    (resolved from cwd) into <target>/.act/.
//
//	act bootstrap-worker --from-remote <url> <target-path>
//	    [--force] [--json] [--timeout-seconds <N>]
//	    Phase 2 ticket 7 (act-0480c9): clone <url> --depth 1 into a
//	    staging dir, atomic-rename to <target>/.act/, stamp
//	    act.role=worker, validate via `act ready`. Honors
//	    act.bootstrapTimeoutSeconds (default 30s); the --timeout-seconds
//	    flag is a test/override knob.
//
//	act bootstrap-worker --from-cwd <orchestrator-path> [<target-path>]
//	    [--force] [--json]
//	    Worker-cwd mode (act-40fce0): the WORKER runs this from inside its
//	    freshly-created worktree, names the orchestrator's repo (or `.act/`)
//	    path as the source, and the target defaults to cwd. Copies the op
//	    log + config + snapshots + imports + nested .act/.git but NOT a live
//	    index.db; rebuilds the index locally. This is the documented
//	    replacement for `cp -r <orchestrator>/.act .`, which copied a
//	    possibly-locked live index.db and caused silent op-write loss.
//
// --from-remote, --from-cwd, and the default cwd-source path are mutually
// exclusive. dispatch_hlc is recorded into the target's
// `.act/.bootstrap-meta.json` under the cwd-source and --from-cwd paths;
// the from-remote path emits dispatch_hlc on stdout (the cloned tree
// already carries the source history).
func runBootstrapWorker(args []string) int {
	fs := flag.NewFlagSet("bootstrap-worker", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing non-empty <target>/.act/ in the worker")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	fromRemote := fs.String("from-remote", "", "clone .act/ from this git URL instead of copying from cwd (Phase 2 ticket 7)")
	fromCWD := fs.String("from-cwd", "", "worker-cwd mode: copy from this orchestrator path into cwd (target defaults to cwd), rebuilding index.db locally instead of copying a live one (act-40fce0)")
	timeoutSeconds := fs.Int("timeout-seconds", 0, "override act.bootstrapTimeoutSeconds for --from-remote (0 = use config / default 30s)")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	// In --from-cwd mode the target is OPTIONAL (defaults to cwd); every
	// other mode requires the positional <target-path>.
	if *fromCWD == "" && fs.NArg() < 1 {
		emitBadFlag(*asJSON,
			"act bootstrap-worker: usage: act bootstrap-worker [--from-remote <url> | --from-cwd <orchestrator-path>] <target-path> [--force] [--json] [--timeout-seconds N]")
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
	})

	if code != 0 {
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act bootstrap-worker: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.BootstrapWorkerResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act bootstrap-worker: unexpected output type %T\n", out)
		return 1
	}
	fmt.Printf("Bootstrapped worker .act/ at %s\n", res.Target)
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
