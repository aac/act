package main

import (
	"strings"
	"testing"
)

// TestDocClaim_Spec_ClrClaimFailedKeepsGitOutputOutOfMessage asserts the
// `claim_failed` row of the error-envelope table (act-2bd35b item 2): git's
// output travels in `details.stderr_tail`, and `message` is the error's text
// without it. The claim's git steps wrap a failure as `… (stderr: <output>)`,
// which the envelope did not split, so the whole of git's output sat in
// `message` and `details` was empty.
//
// The failure used is a `git commit` that cannot run (the nested repo's
// commits are broken), reached through the claim's own commit step.
func TestDocClaim_Spec_ClrClaimFailedKeepsGitOutputOutOfMessage(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "clr claim_failed envelope probe")
	second := createBlocksIssue(t, dir, "clr claim_failed human probe")
	breakCommits(t, dir)

	res, stdout, _, code := clrRunClaim(t, dir, id)
	if code != 1 || res.Error != "claim_failed" {
		t.Fatalf("claim with commits broken: exit %d error=%q; want exit 1 claim_failed\nstdout=%s", code, res.Error, stdout)
	}
	tail, _ := res.Details["stderr_tail"].(string)
	if strings.TrimSpace(tail) == "" {
		t.Errorf("claim_failed: details.stderr_tail is empty; git's output belongs there\nstdout=%s", stdout)
	}
	if strings.Contains(res.Message, "(stderr:") || strings.Contains(res.Message, "(output:") {
		t.Errorf("claim_failed: message still wraps git's output: %q", res.Message)
	}
	if tail != "" && strings.Contains(res.Message, strings.TrimSpace(tail)) {
		t.Errorf("claim_failed: message repeats git's output %q: %q", tail, res.Message)
	}
	if !strings.HasPrefix(res.Message, "claim: commit:") {
		t.Errorf("claim_failed: message = %q; want it to start with the failing step, claim: commit:", res.Message)
	}
	if _, ok := res.Details["quarantined_op"].(string); !ok {
		t.Errorf("claim_failed: details.quarantined_op is gone: %s", stdout)
	}

	// Without --json the message is on stderr, followed by git's output:
	// a person reading the terminal has no details object to look in.
	_, stderr, code := runActIn(t, dir, "update", second, "--claim")
	t.Logf("act update --claim (human), commits broken: exit %d\nstderr=%s", code, stderr)
	if code != 1 || !strings.Contains(stderr, "claim: commit:") {
		t.Fatalf("claim (human) with commits broken: exit %d, want 1 and the message on stderr\nstderr=%s", code, stderr)
	}
	if tail != "" && !strings.Contains(stderr, strings.TrimSpace(tail)) {
		t.Errorf("claim (human): stderr lacks git's output %q:\n%s", tail, stderr)
	}
}
