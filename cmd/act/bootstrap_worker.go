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
// --from-remote and the cwd-source path are mutually exclusive: passing
// --from-remote bypasses the cwd resolver entirely. dispatch_hlc is
// recorded into the target's `.act/.bootstrap-meta.json` only under the
// cwd-source path; the from-remote path emits dispatch_hlc on stdout
// (the cloned tree already carries the source history).
func runBootstrapWorker(args []string) int {
	fs := flag.NewFlagSet("bootstrap-worker", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing non-empty <target>/.act/ in the worker")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	fromRemote := fs.String("from-remote", "", "clone .act/ from this git URL instead of copying from cwd (Phase 2 ticket 7)")
	timeoutSeconds := fs.Int("timeout-seconds", 0, "override act.bootstrapTimeoutSeconds for --from-remote (0 = use config / default 30s)")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitBadFlag(*asJSON,
			"act bootstrap-worker: usage: act bootstrap-worker [--from-remote <url>] <target-path> [--force] [--json] [--timeout-seconds N]")
		return 2
	}
	target := fs.Arg(0)

	out, code := cli.RunBootstrapWorker(cli.BootstrapWorkerOptions{
		Target:         target,
		Force:          *force,
		AsJSON:         *asJSON,
		FromRemoteURL:  *fromRemote,
		TimeoutSeconds: *timeoutSeconds,
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
	if *fromRemote == "" {
		fmt.Printf("  dispatch_hlc recorded in %s/.act/%s\n", res.Target, cli.BootstrapMetaFileName)
	} else {
		fmt.Printf("  dispatch_hlc: %s\n", res.DispatchHLC)
		fmt.Printf("  act.role:    worker\n")
	}
	return 0
}
