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
		Check:   *check,
		Fix:     *fix,
		AsJSON:  *asJSON,
		Compact: *compact,
	})
	if code != 0 && code != 1 {
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
	return code
}

// emitDoctorError renders the doctor error envelope to stderr (human form)
// or stdout (JSON form).
func emitDoctorError(asJSON bool, payload map[string]any) {
	if asJSON {
		data, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act doctor: json marshal: %v\n", err)
			return
		}
		fmt.Println(string(data))
		return
	}
	if msg, _ := payload["message"].(string); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	if e, _ := payload["error"].(string); e != "" {
		fmt.Fprintln(os.Stderr, e)
	}
}
