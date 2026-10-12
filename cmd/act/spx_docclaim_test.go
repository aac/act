package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// spxKeys runs one act command, wants exit 0, and returns the top-level keys
// of the JSON object it printed, sorted and comma-joined, with the object.
func spxKeys(t *testing.T, dir string, args ...string) (string, map[string]json.RawMessage) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", args...)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("act %s: not a JSON object (%v)\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, out, stderr)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ","), m
}

const spxCloseFiveKeys = "commit_marker,committed,id,ops_written,reason"

// TestDocClaim_Spec_SpxCloseAndUpdateSetPushDeferred asserts the auto-publish
// paragraph's sentence, and the matching one in `act help errors`: with an
// `origin` that cannot be reached, both `act close --json` and
// `act update --json` exit 0 and set push_deferred and push_deferred_reason.
func TestDocClaim_Spec_SpxCloseAndUpdateSetPushDeferred(t *testing.T) {
	dir := sp6Site(t)
	toClose := createIssue(t, dir, "spx deferred close")
	toUpdate := createIssue(t, dir, "spx deferred update")
	upuAddMissingOrigin(t, dir)

	for _, args := range [][]string{
		{"close", "--json", "--no-code", "--no-doctor", toClose},
		{"update", "--json", "--priority", "1", toUpdate},
	} {
		_, m := spxKeys(t, dir, args...)
		if string(m["push_deferred"]) != "true" || sp8Str(m["push_deferred_reason"]) == "" {
			t.Errorf("act %s with an unreachable origin: push_deferred %s, push_deferred_reason %s; want true and a reason",
				strings.Join(args, " "), m["push_deferred"], m["push_deferred_reason"])
		}
		if string(m["committed"]) != "true" {
			t.Errorf("act %s with an unreachable origin: committed %s, want true", strings.Join(args, " "), m["committed"])
		}
	}
	if st := sp8Str(sp8Show(t, dir, toClose)["status"]); st != "closed" {
		t.Errorf("after the deferred close, show reports status %q, want closed", st)
	}

	out, stderr, code := runActIn(t, t.TempDir(), "help", "errors")
	if code != 0 {
		t.Fatalf("act help errors: exit %d\nstderr=%s", code, stderr)
	}
	flat := strings.Join(strings.Fields(out), " ")
	if want := "'act close --json' and 'act update --json' set push_deferred: true"; !strings.Contains(flat, want) {
		t.Errorf("act help errors does not say %q", want)
	}
}

// TestDocClaim_Spec_SpxCloseJSONKeysThatAppearWhenTheyApply asserts the
// sentence under the close section's JSON example: a close always prints the
// five keys, and warnings, push_deferred and push_deferred_reason appear
// only when there is something to report.
func TestDocClaim_Spec_SpxCloseJSONKeysThatAppearWhenTheyApply(t *testing.T) {
	dir := sp6Site(t)
	plain := createIssue(t, dir, "spx plain close")
	noRemote := createIssue(t, dir, "spx no-remote close")
	deferred := createIssue(t, dir, "spx deferred close")
	frozen := createIssue(t, dir, "spx frozen close")

	if got, _ := spxKeys(t, dir, "close", "--json", "--no-code", "--no-doctor", plain); got != spxCloseFiveKeys {
		t.Errorf("plain close keys = %s, want %s", got, spxCloseFiveKeys)
	}

	got, m := spxKeys(t, dir, "close", "--json", "--no-code", "--no-doctor", "--push", noRemote)
	if got != spxCloseFiveKeys+",warnings" || string(m["warnings"]) != `["push_no_remote"]` {
		t.Errorf("close --push with no origin: keys %s, warnings %s; want the five keys plus warnings [push_no_remote]", got, m["warnings"])
	}

	// The index failure comes before the origin is added, so its result
	// carries the warning and no deferral.
	thaw := ixwFreezeIndex(t, dir)
	got, m = spxKeys(t, dir, "close", "--json", "--no-code", "--no-doctor", frozen)
	thaw()
	if got != spxCloseFiveKeys+",warnings" || string(m["warnings"]) != `["index_update_failed"]` {
		t.Errorf("close with a read-only index: keys %s, warnings %s; want the five keys plus warnings [index_update_failed]", got, m["warnings"])
	}

	upuAddMissingOrigin(t, dir)
	got, m = spxKeys(t, dir, "close", "--json", "--no-code", "--no-doctor", deferred)
	if got != "commit_marker,committed,id,ops_written,push_deferred,push_deferred_reason,reason" || string(m["push_deferred"]) != "true" {
		t.Errorf("close with an unreachable origin: keys %s, push_deferred %s; want the five keys plus push_deferred:true and push_deferred_reason", got, m["push_deferred"])
	}
}

// TestDocClaim_Spec_SpxCloseIndexFailureExitCodes asserts the index half of
// the close section's exit-code sentence. An index that cannot be updated
// after the close is committed is exit 0 with a warning. An index that
// cannot be brought up to date, or opened, before the op is written is exit
// 1 with no op written or committed.
func TestDocClaim_Spec_SpxCloseIndexFailureExitCodes(t *testing.T) {
	dir := sp6Site(t)
	first := createIssue(t, dir, "spx first close")
	second := createIssue(t, dir, "spx second close")
	third := createIssue(t, dir, "spx third close")

	// After the commit: exit 0, warning. ixwRunFrozen asserts both, and
	// that the op commit exists.
	thaw := ixwFreezeIndex(t, dir)
	ixwRunFrozen(t, dir, "close", "--json", "--no-code", "--no-doctor", first)

	// The index is now one op behind and still read-only, so the rebuild
	// the next close runs before writing fails.
	before := ixwOpCommits(t, dir)
	pass5Run(t, dir, 1, "index_rebuild_failed", "close", "--json", "--no-code", "--no-doctor", second)
	if after := ixwOpCommits(t, dir); after != before {
		t.Errorf("close stopped by index_rebuild_failed still committed an op (%d commits before, %d after)", before, after)
	}
	thaw()
	if st := sp8Str(sp8Show(t, dir, second)["status"]); st != "open" {
		t.Errorf("after the close that exited index_rebuild_failed, show reports status %q, want open", st)
	}
	if st := sp8Str(sp8Show(t, dir, first)["status"]); st != "closed" {
		t.Errorf("after the close that warned index_update_failed, show reports status %q, want closed", st)
	}

	// An index that cannot be opened at all: exit 1, nothing committed.
	idxBreakIndex(t, dir)
	before = ixwOpCommits(t, dir)
	pass5Run(t, dir, 1, "index_open_failed", "close", "--json", "--no-code", "--no-doctor", third)
	if after := ixwOpCommits(t, dir); after != before {
		t.Errorf("close stopped by index_open_failed still committed an op (%d commits before, %d after)", before, after)
	}
}
