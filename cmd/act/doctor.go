package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runDoctor dispatches `act doctor`. The repo root is resolved from cwd;
// flag parsing follows the universal pattern used by other commands.
//
// Exit codes mirror cli.RunDoctor: 0 success, 1 error finding,
// 2 bad flags, 3 missing repo / .act/.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	check := fs.String("check", "", "run a single named check (default: all)")
	fix := fs.Bool("fix", false, "auto-remediate index-divergence/index-schema findings")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	compact := fs.Bool("compact", false, "trigger manual compaction of eligible issues")
	strict := fs.Bool("strict", false, "promote warn findings to error (exit 1 on any finding)")
	noFetch := fs.Bool("no-fetch", false, "suppress the inline `git fetch --dry-run` probe used by case (g) reachability and case (h) upstream-drift detection; case (h) is suppressed entirely")
	fixIndex := fs.Bool("fix-index", false, "rebuild .act/index.db from .act/ops/ when the index is malformed; backs up the broken copy to .act/index.db.malformed-<ts>")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitDoctorError(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunDoctor(root, cli.DoctorOptions{
		Check:    *check,
		Fix:      *fix,
		AsJSON:   *asJSON,
		Compact:  *compact,
		Strict:   *strict,
		NoFetch:  *noFetch,
		FixIndex: *fixIndex,
	})
	// Exit 4 is the Phase 2 case-(g) origin-unreachable code — same
	// envelope shape as a Phase 1 error finding (DoctorResult on the
	// success-shape return), so the rendering path below handles it.
	if code != 0 && code != 1 && code != 4 {
		// 2/3 are error envelopes.
		m, _ := toMap(out)
		emitDoctorError(*asJSON, m)
		return code
	}

	if *asJSON {
		data, jerr := json.Marshal(out)
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act doctor: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return code
	}

	res, ok := out.(cli.DoctorResult)
	if !ok {
		fmt.Fprintf(os.Stderr, "act doctor: unexpected output type %T\n", out)
		return 1
	}
	fmt.Print(cli.FormatDoctorHuman(res))
	// Phase 2 ticket 9: cases (f), (g), (h) — emit the bare finding
	// message to stderr so an agent (or human) tailing stderr sees the
	// load-bearing literals (`local: N unpushed commits...`, `remote:
	// origin unreachable...`, `upstream: origin-upstream is N commits
	// behind...`) without parsing the bracketed human-text output. The
	// stderr emission is in addition to the stdout finding line; the
	// two surfaces serve different consumers.
	for _, f := range res.Findings {
		switch f.Check {
		case cli.CheckUnpushedCommits, cli.CheckRemoteReachable, cli.CheckUpstreamDrift:
			fmt.Fprintln(os.Stderr, f.Message)
		case "index-malformed":
			// Per act-f2f93a: emit the bare message to stderr so an
			// agent (or human) tailing stderr sees the load-bearing
			// remediation literal `'act doctor --fix-index'` without
			// parsing the bracketed human-text output.
			fmt.Fprintln(os.Stderr, f.Message)
		}
	}
	return code
}

// emitDoctorError renders the doctor error envelope to stderr (human form)
// or stdout (JSON form). Delegates to the shared emitEnvelope helper.
func emitDoctorError(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
