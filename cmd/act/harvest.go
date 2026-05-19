package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runHarvest dispatches `act harvest <worker-path> [--dry-run] [--json]`.
//
// Phase 1.5 prerequisite for the coordination-plane Phase 2 work
// (docs/coordination-plane-phase2-plan.md). Mirror of bootstrap-worker:
// pulls new ops from a worker's `.act/ops/` back into the host's `.act/`,
// commits them on the nested `.act/.git`, and re-folds the index. Does
// NOT push — that's the orchestrator's responsibility.
//
// The Phase 2 `--from-remote <url>` flag is intentionally not added here;
// adding it later is a clean addition (a new branch in RunHarvest and a
// corresponding option), not a refactor of the cwd-resolves-host path.
func runHarvest(args []string) int {
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be harvested without writing or committing")
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
			"act harvest: usage: act harvest <worker-path> [--dry-run] [--json]")
		return 2
	}
	worker := fs.Arg(0)

	out, code := cli.RunHarvest(cli.HarvestOptions{
		WorkerPath: worker,
		DryRun:     *dryRun,
		AsJSON:     *asJSON,
	})

	if code != 0 {
		emitEnvelope(*asJSON, out)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act harvest: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	res, ok := out.(cli.HarvestResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act harvest: unexpected output type %T\n", out)
		return 1
	}
	if res.DryRun {
		fmt.Printf("Would harvest %d ops from %s (dry-run; no writes)\n", len(res.HarvestedOps), worker)
	} else if len(res.HarvestedOps) == 0 {
		fmt.Printf("Harvested 0 ops from %s (already in sync)\n", worker)
	} else {
		fmt.Printf("Harvested %d ops from %s\n", len(res.HarvestedOps), worker)
		fmt.Printf("  commit:           %s\n", res.CommitMessage)
		fmt.Printf("  issues indexed:   %d\n", res.FoldDiffSummary.IssuesIndexed)
	}
	if len(res.SkippedOps) > 0 {
		fmt.Printf("  skipped (already present): %d\n", len(res.SkippedOps))
	}
	if res.FoldError != "" {
		fmt.Fprintf(os.Stderr, "warning: fold failed after harvest commit: %s\n", res.FoldError)
		fmt.Fprintln(os.Stderr, "  the op log is the source of truth; run `act doctor` or `act list` to rebuild the index.")
	}
	return 0
}
