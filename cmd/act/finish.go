package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runFinish dispatches `act finish <id>`: the composed close + push flow,
// the CLI equivalent of the act_finish MCP tool (internal/mcp/composed.go's
// callFinish). It is a thin wrapper over `act close`: the close path already
// performs the synchronous publish (AutoPushAfterCommit) when an origin is
// configured, and silently skips the push on a no-origin repo — so "close +
// push" needs no extra step here, exactly as callFinish relies on.
//
// The result shape mirrors callFinish: {"closed": true, "id", "short_id",
// "reason"} on a fresh close, {"closed": true, "id", "already_closed": true}
// for the idempotent already-closed case. Failures use the standard error
// envelope (RunClose's typed error outputs, normalized like the close verb).
//
// The function lives in its own file so concurrent edits to main.go's
// dispatch table do not collide with `act finish` wiring (mirrors close.go).
func runFinish(args []string) int {
	fs := flag.NewFlagSet("finish", flag.ContinueOnError)
	reason := fs.String("reason", "", "closed reason (stored as closed_reason; max 500 bytes — see 'act help workflow' for cap rationale)")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	noCommit := fs.Bool("no-commit", false, "write op file but skip staging, the auto-commit, and the push")
	push := fs.Bool("push", false, "push after the commit (errors if the close stays staged for the agent's next commit)")
	isolated := fs.Bool("isolated", false, "offline mode: commit but no network ops")
	rearranged, err := rearrangeArgs(args, fs)
	if err != nil {
		return 2
	}
	if err := fs.Parse(rearranged); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		emitFinish(*asJSON, map[string]any{
			"error":   cli.ErrBadFlag,
			"message": "act finish: usage: act finish <id> [--reason TEXT] [--json]",
		})
		return 2
	}
	// Upfront --reason length validation, mirroring `act close` (fail fast
	// before any op file is written, naming the byte cap).
	if n := len(*reason); n > closeReasonMaxBytes {
		emitFinish(*asJSON, map[string]any{
			"error": cli.ErrBadFlag,
			"message": fmt.Sprintf(
				"act finish: --reason exceeds %d-byte cap (got %d bytes); please shorten",
				closeReasonMaxBytes, n,
			),
		})
		return 2
	}
	idArg := fs.Arg(0)

	root, err := findRepoRoot()
	if err != nil {
		emitFinish(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	out, code := cli.RunClose(root, cli.CloseOptions{
		ID:       idArg,
		Reason:   *reason,
		AsJSON:   *asJSON,
		NoCommit: *noCommit,
		Push:     *push,
		Isolated: *isolated,
	})
	if code != 0 {
		m, _ := toMap(out)
		emitFinish(*asJSON, m)
		return code
	}

	// Success: reshape into the callFinish-style {closed, id, short_id, ...}
	// envelope so both interfaces report a "finished" issue identically.
	switch v := out.(type) {
	case cli.CloseResult:
		if *asJSON {
			emitFinishJSON(map[string]any{
				"closed":   true,
				"id":       v.ID,
				"short_id": v.ShortID,
				"reason":   v.Reason,
			})
			return 0
		}
		fmt.Print(cli.FormatCloseHuman(v))
	case cli.CloseAlreadyClosed:
		if *asJSON {
			emitFinishJSON(map[string]any{
				"closed":         true,
				"id":             v.ID,
				"already_closed": true,
			})
			return 0
		}
		fmt.Print(cli.FormatCloseAlreadyClosedHuman(v))
	default:
		fmt.Fprintf(os.Stderr, "act finish: unexpected output type %T\n", out)
		return 1
	}
	return 0
}

// emitFinishJSON marshals a success map to stdout. Kept separate from
// emitFinish (the error path) so the success shape is explicit.
func emitFinishJSON(payload map[string]any) {
	data, jerr := json.Marshal(payload)
	if jerr != nil {
		fmt.Fprintf(os.Stderr, "act finish: json marshal: %v\n", jerr)
		return
	}
	fmt.Println(string(data))
}

// emitFinish renders an error envelope for the finish subcommand. Delegates
// to the shared emitEnvelope helper so the JSON shape matches the rest of
// the CLI surface.
func emitFinish(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
