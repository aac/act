package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// pnwNoRemoteLine is the stderr line a write command prints, without --json,
// for --push in a tracker with no origin remote. cmd is "close", "dep add",
// "reopen" or "delete".
func pnwNoRemoteLine(cmd string) string {
	return "act " + cmd + ": warning: --push: no origin remote, nothing was pushed"
}

// pnwWarnings runs `act <args> --json`, wants exit 0, and returns the
// result's `warnings` array joined with commas ("" when the key is absent)
// and the raw stdout.
func pnwWarnings(t *testing.T, dir string, args ...string) (string, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append(args, "--json")...)
	var res struct {
		Error    string   `json:"error"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act %v --json: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	if res.Error != "" {
		t.Errorf("act %v --json: error %q, want a success result\nstdout=%s", args, res.Error, out)
	}
	return strings.Join(res.Warnings, ","), out
}

// pnwWantNoRemote runs the write once with --json and once (humanArgs)
// without, both with --push in a tracker that has no origin, and wants exit
// 0, exactly the push_no_remote warning in JSON, and the stderr line in
// human mode.
func pnwWantNoRemote(t *testing.T, dir, cmd string, jsonArgs, humanArgs []string) {
	t.Helper()
	if w, out := pnwWarnings(t, dir, append(jsonArgs, "--push")...); w != "push_no_remote" {
		t.Errorf("act %v --push --json with no remote: warnings [%s], want exactly [push_no_remote]\nstdout=%s", jsonArgs, w, out)
	}
	out, stderr := pass5Run(t, dir, 0, "", append(humanArgs, "--push")...)
	if !strings.Contains(stderr, pnwNoRemoteLine(cmd)) {
		t.Errorf("act %v --push with no remote: stdout %q, stderr %q; want the line %q on stderr", humanArgs, out, stderr, pnwNoRemoteLine(cmd))
	}
}

// pnwStatus returns the status `act show --json` reports for id.
func pnwStatus(t *testing.T, dir, id string) string {
	t.Helper()
	return sp8Str(sp8Show(t, dir, id)["status"])
}

// TestDocClaim_Spec_PushNoRemoteWarnsOnEveryWrite asserts the `--push`
// bullet of the universal write flags: in a tracker with no `origin` remote,
// `close`, `dep add`, `reopen` and `delete` given `--push` write and commit
// the op, exit 0, and name the push that did not happen with the warning
// `push_no_remote` — as `create` and `update` do (act-7e8600: these four
// used to exit 0 with no sign that nothing was pushed).
func TestDocClaim_Spec_PushNoRemoteWarnsOnEveryWrite(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		dir := sp6Site(t)
		a, _ := wfxCreate(t, dir, "pnw a")
		b, _ := wfxCreate(t, dir, "pnw b")
		c, _ := wfxCreate(t, dir, "pnw c")
		d, _ := wfxCreate(t, dir, "pnw d")
		e, _ := wfxCreate(t, dir, "pnw e")

		// create and update are the model; the bullet names them too.
		if made, _ := wfxCreate(t, dir, "pnw created with push", "--push"); strings.Join(made.Warnings, ",") != "push_no_remote" {
			t.Errorf("act create --push --json with no remote: warnings %v, want exactly [push_no_remote]", made.Warnings)
		}
		if upd, _ := pnrUpdate(t, dir, e.ID, "--priority", "1", "--push"); strings.Join(upd.Warnings, ",") != "push_no_remote" {
			t.Errorf("act update --push --json with no remote: warnings %v, want exactly [push_no_remote]", upd.Warnings)
		}

		// dep add: the edge form and the --external form.
		pnwWantNoRemote(t, dir, "dep add",
			[]string{"dep", "add", a.ID, b.ID},
			[]string{"dep", "add", c.ID, d.ID})
		if out, _ := pass5Run(t, dir, 0, "", "show", "--json", a.ID); !strings.Contains(out, b.ID) {
			t.Errorf("after dep add --push with no remote, show %s does not name the blocker %s:\n%s", a.ID, b.ID, out)
		}
		pnwWantNoRemote(t, dir, "dep add",
			[]string{"dep", "add", a.ID, "--external", "linear:PNW-1"},
			[]string{"dep", "add", c.ID, "--external", "linear:PNW-2"})
		if out, _ := pass5Run(t, dir, 0, "", "show", "--json", a.ID); !strings.Contains(out, "linear:PNW-1") {
			t.Errorf("after dep add --external --push with no remote, show %s does not name linear:PNW-1:\n%s", a.ID, out)
		}

		// close.
		pnwWantNoRemote(t, dir, "close",
			[]string{"close", b.ID, "--no-code"},
			[]string{"close", d.ID, "--no-code"})
		if st := pnwStatus(t, dir, b.ID); st != "closed" {
			t.Errorf("after close --push with no remote, show reports status %q, want closed", st)
		}
		if st := pnwStatus(t, dir, d.ID); st != "closed" {
			t.Errorf("after close --push (human) with no remote, show reports status %q, want closed", st)
		}

		// reopen.
		pnwWantNoRemote(t, dir, "reopen",
			[]string{"reopen", b.ID},
			[]string{"reopen", d.ID})
		if st := pnwStatus(t, dir, b.ID); st != "open" {
			t.Errorf("after reopen --push with no remote, show reports status %q, want open", st)
		}

		// delete.
		pnwWantNoRemote(t, dir, "delete",
			[]string{"delete", b.ID},
			[]string{"delete", d.ID})
		for _, id := range []string{b.ID, d.ID} {
			if out, _ := pass5Run(t, dir, 0, "", "show", "--json", id); !strings.Contains(out, `"tombstoned":true`) {
				t.Errorf("after delete --push with no remote, show %s does not report it tombstoned:\n%s", id, out)
			}
		}

		// Without --push there is nothing to warn about.
		if w, out := pnwWarnings(t, dir, "close", e.ID, "--no-code"); w != "" {
			t.Errorf("act close --json with no remote and no --push: warnings [%s], want none\nstdout=%s", w, out)
		}
		_, stderr := pass5Run(t, dir, 0, "", "reopen", e.ID)
		if strings.Contains(stderr, "nothing was pushed") {
			t.Errorf("act reopen with no remote and no --push: stderr carries the nothing-was-pushed warning:\n%s", stderr)
		}
	})

	t.Run("origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		a, _ := wfxCreate(t, dir, "pnw a, origin")
		b, _ := wfxCreate(t, dir, "pnw b, origin")
		for _, args := range [][]string{
			{"dep", "add", a.ID, b.ID},
			{"close", b.ID, "--no-code"},
			{"reopen", b.ID},
			{"delete", b.ID},
		} {
			w, out := pnwWarnings(t, dir, append(args, "--push")...)
			if w != "" || !wfxOriginHasHead(t, dir, bare) {
				t.Errorf("act %v --push --json with origin: warnings [%s], origin has the commit %v; want none, true\nstdout=%s", args, w, wfxOriginHasHead(t, dir, bare), out)
			}
		}
	})

	// The MCP escape hatches hand the same result back, so the warning
	// reaches an MCP client too.
	t.Run("mcp", func(t *testing.T) {
		dir := sp6Site(t)
		a, _ := wfxCreate(t, dir, "pnw mcp a")
		b, _ := wfxCreate(t, dir, "pnw mcp b")
		out, stderr, code := pass5MCP(t, dir, nil, pass5MCPInit,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_dep_add","arguments":{"child":"`+a.ID+`","parent":"`+b.ID+`","push":true}}}`,
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"act_close","arguments":{"id":"`+b.ID+`","push":true}}}`)
		if code != 0 {
			t.Fatalf("act mcp: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
		}
		for _, id := range []string{`"id":2`, `"id":3`} {
			line := ""
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, id) {
					line = l
				}
			}
			if !strings.Contains(line, "push_no_remote") || strings.Contains(line, `"isError":true`) {
				t.Errorf("MCP response %s with push:true and no remote: want a success carrying push_no_remote, got:\n%s", id, line)
			}
		}
		if st := pnwStatus(t, dir, b.ID); st != "closed" {
			t.Errorf("after MCP act_close push:true with no remote, show reports status %q, want closed", st)
		}
	})
}

// TestDocClaim_Spec_CloseIsolatedDoesNotPush asserts the `--isolated` bullet
// of the universal write flags for `close`, which has its own publish path:
// with `origin` configured, `act close --isolated` commits locally, pushes
// nothing and queues nothing in `.act/.pending-pushes` (act-7e8600: it used
// to publish to origin like an ordinary close).
func TestDocClaim_Spec_CloseIsolatedDoesNotPush(t *testing.T) {
	dir := sp6Site(t)
	bare := wfxAddBareOrigin(t, dir)
	made, _ := wfxCreate(t, dir, "pnw isolated close target") // origin gets the branch
	if !wfxOriginHasHead(t, dir, bare) {
		t.Fatalf("setup: a plain create with origin did not publish")
	}

	w, out := pnwWarnings(t, dir, "close", made.ID, "--no-code", "--isolated")
	if !strings.Contains(out, `"committed":true`) || w != "" {
		t.Errorf("act close --isolated --json with origin: want committed true and no warnings, got warnings [%s]\nstdout=%s", w, out)
	}
	if strings.Contains(out, "push_deferred") {
		t.Errorf("act close --isolated --json reports a deferred push; isolated attempts none:\n%s", out)
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Errorf("act close --isolated with origin: the close commit is on origin; isolated forbids the push")
	}
	if q := pnrPendingPushes(t, dir); q != "" {
		t.Errorf("act close --isolated queued a deferred push; want none:\n%s", q)
	}
	if st := pnwStatus(t, dir, made.ID); st != "closed" {
		t.Errorf("after close --isolated, show reports status %q, want closed", st)
	}

	// The next ordinary write publishes what isolated held back.
	wfxCreate(t, dir, "pnw after isolated close")
	if !wfxOriginHasHead(t, dir, bare) {
		t.Errorf("a plain create after close --isolated did not bring origin up to HEAD")
	}
}

// TestPnw_NextHasNoPushFlag records that `act next` defines no --push flag,
// so there is no `act next --push` whose missing push could go unreported
// (act-7e8600 item 3).
func TestPnw_NextHasNoPushFlag(t *testing.T) {
	dir := sp6Site(t)
	made, _ := wfxCreate(t, dir, "pnw next target")
	pass5Run(t, dir, 2, "bad_flag", "next", "--push", "--json")
	if st := pnwStatus(t, dir, made.ID); st != "open" {
		t.Errorf("after the refused act next --push, show reports status %q, want open", st)
	}
}
