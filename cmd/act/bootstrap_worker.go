package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runBootstrapWorker dispatches `act bootstrap-worker <target-path>
// [--force] [--json]`.
//
// Phase 1.5 prerequisite for the coordination-plane Phase 2 work
// (docs/coordination-plane-phase2-plan.md ticket 7). Copies the host
// repo's `.act/` state tree into the given worker target so a dispatched
// sub-agent can immediately run act commands against state that mirrors
// the orchestrator's view.
//
// The Phase 2 `--from-remote <url>` flag is intentionally not added here;
// the subcommand is designed so adding it later is a clean addition (a
// new branch in this function and a corresponding RunBootstrapWorker
// option), not a refactor of the cwd-resolves-source path.
//
// dispatch_hlc is recorded into the target's
// `.act/.bootstrap-meta.json` file. See the doc comment on
// internal/cli/bootstrap_worker.go for the rationale.
func runBootstrapWorker(args []string) int {
	fs := flag.NewFlagSet("bootstrap-worker", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing non-empty <target>/.act/ in the worker")
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
			"act bootstrap-worker: usage: act bootstrap-worker <target-path> [--force] [--json]")
		return 2
	}
	target := fs.Arg(0)

	out, code := cli.RunBootstrapWorker(cli.BootstrapWorkerOptions{
		Target: target,
		Force:  *force,
		AsJSON: *asJSON,
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
	fmt.Printf("  dispatch_hlc recorded in %s/.act/%s\n", res.Target, cli.BootstrapMetaFileName)
	return 0
}
