package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/aac/act/internal/cli"
)

// runNext dispatches `act next`: the composed ready → claim → show flow,
// the CLI equivalent of the act_next MCP tool (internal/mcp/composed.go's
// callNext). It picks the top claimable issue from the ready frontier,
// claims it atomically, and shows it.
//
// Observable semantics mirror callNext:
//   - Empty ready set → {"claimed": false, "candidates": []}, exit 0.
//   - A claim succeeds → {"claimed": true, "issue": {...},
//     "commit_marker": "Act-Id: act-XXXX"}, exit 0.
//   - Concurrent claimers resolve last-write-wins: a lost claim (exit 5,
//     claim_lost) excludes that id and the loop tries the next candidate
//     in priority order. If every candidate is lost, the result is
//     {"claimed": false, "candidates": [...]} (exit 0) so the caller can
//     re-run or pick manually — the same budget-exhausted shape callNext
//     returns. Unlike the MCP tool, the CLI does not sleep/backoff between
//     attempts: a one-shot CLI invocation immediately tries the next
//     candidate rather than waiting for a distributed writer to settle.
//
// The function lives in its own file so concurrent edits to main.go's
// dispatch table do not collide with `act next` wiring (mirrors ready.go).
func runNext(args []string) int {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	under := fs.String("under", "", "restrict the ready frontier to descendants of the given issue id (prefix ok)")
	limit := fs.Int("limit", 50, "maximum number of ready candidates to consider")
	isolated := fs.Bool("isolated", false, "offline mode for the claim: commit but no network ops")
	asJSON := fs.Bool("json", false, "emit JSON output instead of human-friendly text")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := findRepoRoot()
	if err != nil {
		emitNext(*asJSON, map[string]any{
			"error":   "not_in_git",
			"message": err.Error(),
		})
		return 3
	}

	// Step 1: gather the ready frontier (filtered by --under), ordered by
	// priority — the same set `act ready` returns.
	readyOut, code := cli.RunReady(root, cli.ReadyOptions{
		Under:  *under,
		Limit:  *limit,
		AsJSON: true,
	})
	if code != 0 {
		m, _ := toMap(readyOut)
		emitNext(*asJSON, m)
		return code
	}
	res, ok := readyOut.(cli.ReadyResult)
	if !ok {
		emitNext(*asJSON, map[string]any{
			"error":   "internal",
			"message": fmt.Sprintf("act next: ready returned unexpected type %T", readyOut),
		})
		return 1
	}
	if len(res.Ready) == 0 {
		return emitNextNoClaim(*asJSON, []cli.ReadyIssue{})
	}

	// Tracks ids that lost their claim race this run; excluded from the
	// next pick (mirrors callNext's `lost` set).
	lost := make(map[string]bool)

	for {
		// Pick the first not-yet-lost candidate in priority order.
		var pick *cli.ReadyIssue
		for i := range res.Ready {
			if !lost[res.Ready[i].ID] {
				pick = &res.Ready[i]
				break
			}
		}
		if pick == nil {
			// Every candidate lost its race; surface the frontier so the
			// caller can re-run or pick manually.
			return emitNextNoClaim(*asJSON, res.Ready)
		}

		// Step 2: attempt the atomic claim.
		claimOut, claimCode := cli.RunUpdate(root, cli.UpdateOptions{
			ID:       pick.ID,
			Claim:    true,
			Isolated: *isolated,
			AsJSON:   true,
		})
		if claimCode == 5 {
			// Claim lost (last-write-wins): exclude this id and retry the
			// next candidate. exit 5 is the canonical claim_lost code.
			lost[pick.ID] = true
			continue
		}
		if claimCode != 0 {
			// Any other failure (e.g. the id vanished, bad state) is a real
			// error — surface its envelope verbatim.
			m, _ := toMap(claimOut)
			emitNext(*asJSON, m)
			return claimCode
		}

		// Step 3: show the freshly-claimed issue.
		showOut, showCode := cli.RunShow(root, cli.ShowOptions{
			ID:     pick.ID,
			AsJSON: true,
		})
		if showCode != 0 {
			m, _ := toMap(showOut)
			emitNext(*asJSON, m)
			return showCode
		}

		// commit_marker carries the `Act-Id: act-XXXX` trailer the caller
		// embeds in the BODY of any work-commit for this issue (mirrors
		// callNext). Derive the short id from show's short_id, falling back
		// to the full id.
		short := pick.ID
		var issueJSON any = showOut
		if sr, ok := showOut.(cli.ShowResult); ok {
			m := sr.ShowJSON()
			issueJSON = m
			if s, ok := m["short_id"].(string); ok && s != "" {
				short = s
			}
		}
		commitMarker := cli.WorkCommitTrailerKey + ": " + short

		if *asJSON {
			data, jerr := json.Marshal(map[string]any{
				"claimed":       true,
				"issue":         issueJSON,
				"commit_marker": commitMarker,
			})
			if jerr != nil {
				fmt.Fprintf(os.Stderr, "act next: json marshal: %v\n", jerr)
				return 1
			}
			fmt.Println(string(data))
			return 0
		}

		fmt.Printf("Claimed %s — now showing:\n\n", short)
		fmt.Print(cli.FormatShowHuman(showOut))
		fmt.Printf("\ncommit marker: %s\n", commitMarker)
		return 0
	}
}

// emitNextNoClaim renders the "nothing claimed" result: {"claimed": false,
// "candidates": [...]} under --json, or a human line. candidates is the
// (possibly empty) ready frontier the caller can fall back to. Always
// exit 0 — an empty or fully-contended frontier is not an error.
func emitNextNoClaim(asJSON bool, candidates []cli.ReadyIssue) int {
	if candidates == nil {
		candidates = []cli.ReadyIssue{}
	}
	if asJSON {
		data, jerr := json.Marshal(map[string]any{
			"claimed":    false,
			"candidates": candidates,
		})
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "act next: json marshal: %v\n", jerr)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	if len(candidates) == 0 {
		fmt.Println("No claimable work in the ready set.")
	} else {
		fmt.Printf("Could not claim any of the %d ready candidate(s) — they were claimed concurrently. Re-run 'act next' or pick from 'act ready'.\n", len(candidates))
	}
	return 0
}

// emitNext renders an error envelope for the next subcommand. Delegates to
// the shared emitEnvelope helper so the JSON shape matches the rest of the
// CLI surface.
func emitNext(asJSON bool, payload map[string]any) {
	emitEnvelope(asJSON, payload)
}
