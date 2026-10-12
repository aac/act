package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// fnwFinished is the part of `act finish --json` these tests read.
type fnwFinished struct {
	Closed             bool     `json:"closed"`
	ID                 string   `json:"id"`
	Error              string   `json:"error"`
	Warnings           []string `json:"warnings"`
	PushDeferred       bool     `json:"push_deferred"`
	PushDeferredReason string   `json:"push_deferred_reason"`
}

// fnwFinish runs `act finish --json <args>`, wants exit 0, and returns the
// parsed result, the raw stdout and stderr.
func fnwFinish(t *testing.T, dir string, args ...string) (fnwFinished, string, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append([]string{"finish", "--json"}, args...)...)
	var res fnwFinished
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act finish --json %v: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res, out, stderr
}

// TestDocClaim_FnwFinish_PushNoRemoteWarns: `act finish --push` in a tracker
// with no origin remote closes the issue and says the push it was asked for
// did not happen, as `act close --push` does (act-7b65b0: finish rebuilt
// close's result and dropped `warnings`).
func TestDocClaim_FnwFinish_PushNoRemoteWarns(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "fnw finish push, no remote")

	res, out, stderr := fnwFinish(t, dir, id, "--push")
	if !res.Closed || res.ID != id {
		t.Errorf("act finish --push --json = %s; want closed:true and id %s", out, id)
	}
	if got := strings.Join(res.Warnings, ","); got != "push_no_remote" {
		t.Errorf("act finish --push --json with no origin remote: warnings %q, want push_no_remote\nstdout=%s\nstderr=%s", got, out, stderr)
	}
	if st := showField(t, dir, id, "status"); st != "closed" {
		t.Errorf("after act finish --push, show status = %q, want closed", st)
	}

	// No push asked for: nothing to warn about, and no `warnings` key.
	id2 := createIssue(t, dir, "fnw finish, no push")
	if _, out2, _ := fnwFinish(t, dir, id2); strings.Contains(out2, "warnings") {
		t.Errorf("act finish --json without --push carries warnings: %s", out2)
	}

	// Without --json the warning is a line on stderr, worded as close's.
	id3 := createIssue(t, dir, "fnw finish push, human")
	hout, hstderr, code := runActIn(t, dir, "finish", id3, "--push")
	if code != 0 {
		t.Fatalf("act finish --push: exit %d, want 0\nstdout=%s\nstderr=%s", code, hout, hstderr)
	}
	if want := "act finish: warning: --push: no origin remote, nothing was pushed"; !strings.Contains(hstderr, want) {
		t.Errorf("act finish --push with no origin remote: stderr %q lacks %q", hstderr, want)
	}
	if !strings.HasPrefix(hout, "Closed act-") {
		t.Errorf("act finish --push: stdout %q, want the usual line starting %q", hout, "Closed act-")
	}
}

// TestDocClaim_FnwFinish_IndexUpdateFailedWarns: a finish whose close op is
// committed and whose index refresh then fails reports the close with the
// index_update_failed warning (act-7b65b0).
func TestDocClaim_FnwFinish_IndexUpdateFailedWarns(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "fnw finish, frozen index")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "finish", "--json", id)
	if !strings.Contains(out, `"closed":true`) || !strings.Contains(out, `"id":"`+id+`"`) {
		t.Errorf("finish result = %s; want closed:true and id %s", out, id)
	}
	thaw()
	if got := ixwShow(t, dir, id)["status"]; got != "closed" {
		t.Errorf("status after thaw = %v, want closed", got)
	}

	// Without --json: the stderr line close prints, in finish's name.
	dir2 := sp6Site(t)
	id2 := createIssue(t, dir2, "fnw finish, frozen index, human")
	ixwFreezeIndex(t, dir2)
	hout, hstderr, code := runActIn(t, dir2, "finish", id2)
	if code != 0 {
		t.Fatalf("act finish with a read-only index: exit %d, want 0\nstdout=%s\nstderr=%s", code, hout, hstderr)
	}
	if !strings.Contains(hstderr, "act finish: warning: ") || !strings.Contains(hstderr, "index.db could not be updated") {
		t.Errorf("act finish with a read-only index: stderr %q lacks the `act finish: warning:` index line", hstderr)
	}
}

// TestDocClaim_FnwFinish_PushDeferredReachesTheCaller: with an origin that
// cannot be reached the close lands, exits 0, and finish reports the push
// as deferred, as close does (act-7b65b0).
func TestDocClaim_FnwFinish_PushDeferredReachesTheCaller(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "fnw finish, unreachable origin")
	upuAddMissingOrigin(t, dir)

	res, out, stderr := fnwFinish(t, dir, id)
	if !res.Closed || !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("act finish --json with an unreachable origin = %s; want closed:true, push_deferred:true and a push_deferred_reason\nstderr=%s", out, stderr)
	}
	if q := pnrPendingPushes(t, dir); q == "" {
		t.Errorf("premise: finish with an unreachable origin queued nothing in .act/.pending-pushes")
	}

	// A finish that had nothing to defer carries neither key.
	okDir := sp6Site(t)
	okID := createIssue(t, okDir, "fnw finish, no origin")
	if _, okOut, _ := fnwFinish(t, okDir, okID); strings.Contains(okOut, "push_deferred") {
		t.Errorf("act finish --json with no origin carries push_deferred: %s", okOut)
	}
}

// TestDocClaim_FnwNext_ClaimWarningsReachTheCaller: `act next` whose claim
// is committed and whose index refresh then fails says it claimed, with the
// claim's index_update_failed warning (act-7b65b0: next rebuilt the result
// without it).
func TestDocClaim_FnwNext_ClaimWarningsReachTheCaller(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "fnw next, frozen index")
	thaw := ixwFreezeIndex(t, dir)

	out := ixwRunFrozen(t, dir, "next", "--json")
	if !strings.Contains(out, `"claimed":true`) || !strings.Contains(out, id) {
		t.Errorf("next result = %s; want claimed:true for %s", out, id)
	}
	thaw()
	if got := ixwShow(t, dir, id)["status"]; got != "in_progress" {
		t.Errorf("status after thaw = %v, want in_progress", got)
	}
}
