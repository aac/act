package main

// act-fec192: `act show` must not report a status the committed op log
// does not support without saying so. These tests drive the real binary
// as a subprocess, because the claim is about what a READER SEES —
// stderr text and a --json key — and that only exists at the process
// boundary. An in-process assertion on ShowResult would pass against a
// binary that never printed anything.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uncommitLastActCommit moves the nested .act/ repo's HEAD back one
// commit, leaving the op files it introduced on disk. That is exactly the
// state act-3b6e58 was in from 05:06:11 to 23:09:33 on 2026-08-31: the
// close op written, the fold reading it, and nothing durable behind it.
func uncommitLastActCommit(t *testing.T, dir string) {
	t.Helper()
	actDir := filepath.Join(dir, ".act")
	if out, err := exec.Command("git", "-C", actDir, "reset", "--soft", "HEAD~1").CombinedOutput(); err != nil {
		t.Fatalf("git reset --soft: %v: %s", err, out)
	}
}

// soleCloseOpName returns the base name of the single *-close.json op
// file under .act/ops/<id>/.
func soleCloseOpName(t *testing.T, dir, id string) string {
	t.Helper()
	issueDir := filepath.Join(dir, ".act", "ops", id)
	var found []string
	shards, err := os.ReadDir(issueDir)
	if err != nil {
		t.Fatalf("read %s: %v", issueDir, err)
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(issueDir, shard.Name()))
		if err != nil {
			t.Fatalf("read shard: %v", err)
		}
		for _, f := range files {
			if strings.HasSuffix(f.Name(), "-close.json") {
				found = append(found, f.Name())
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one close op under %s, found %d: %v", issueDir, len(found), found)
	}
	return found[0]
}

// TestDocClaim_ShowWarnsStatusNotDurable is the asserting test for the
// `act help` claim "'act show' CARRIES THE SAME WARNING" — specifically
// the "is NOT DURABLE yet" line and the --json `durability` key.
//
// This is the surface the act-fec192 incident actually needed. Three
// sessions read a close inside the uncommitted window, agreed with each
// other, and passed it on as verification; `act show` told none of them
// that the answer rested on a file no commit referenced.
func TestDocClaim_ShowWarnsStatusNotDurable(t *testing.T) {
	dir := newFailVisibilityRepo(t)
	id := createIssueFV(t, dir, "durability probe")
	if _, stderr, code := runActIn(t, dir, "close", id, "--json"); code != 0 {
		t.Fatalf("act close: exit %d; stderr=%s", code, stderr)
	}
	closeOp := soleCloseOpName(t, dir, id)

	// Precondition: a fully committed close warns about nothing. Without
	// this the test would pass against a binary that warns on every read.
	stdout, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show: exit %d; stderr=%s", code, stderr)
	}
	if strings.Contains(stderr, "NOT DURABLE") {
		t.Fatalf("clean store warned about durability; stderr=%s", stderr)
	}
	if strings.Contains(stdout, `"durability"`) {
		t.Fatalf("clean store emitted a durability key; stdout=%s", stdout)
	}

	uncommitLastActCommit(t, dir)

	stdout, stderr, code = runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show after uncommit: exit %d; stderr=%s", code, stderr)
	}

	// The status itself is unchanged — show still folds the op file, and
	// that is the behavior the warning exists to qualify, not replace.
	var res struct {
		Status     string `json:"status"`
		Durability *struct {
			Uncommitted []string `json:"uncommitted"`
			Missing     []string `json:"missing"`
		} `json:"durability"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("parse show output %q: %v", stdout, err)
	}
	if res.Status != "closed" {
		t.Errorf("status = %q, want closed", res.Status)
	}
	if res.Durability == nil {
		t.Fatalf("no durability key in --json output; stdout=%s", stdout)
	}
	if len(res.Durability.Uncommitted) != 1 || res.Durability.Uncommitted[0] != closeOp {
		t.Errorf("durability.uncommitted = %v, want [%s]", res.Durability.Uncommitted, closeOp)
	}
	if len(res.Durability.Missing) != 0 {
		t.Errorf("durability.missing = %v, want empty", res.Durability.Missing)
	}

	for _, want := range []string{
		"WARNING:",
		"NOT DURABLE",
		"(closed)",
		closeOp,
		"do not report it as verified",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}

// TestDocClaim_ShowWarnsStatusBehindOplog is the asserting test for the
// mirror-image claim, "is BEHIND the committed op log": HEAD tracks an op
// file .act/ops/ no longer has, so the status show reports is older than
// the record. That is the second half of the act-fec192 incident, after
// the gate failed and the op file was withdrawn.
func TestDocClaim_ShowWarnsStatusBehindOplog(t *testing.T) {
	dir := newFailVisibilityRepo(t)
	id := createIssueFV(t, dir, "retraction probe")
	if _, stderr, code := runActIn(t, dir, "close", id, "--json"); code != 0 {
		t.Fatalf("act close: exit %d; stderr=%s", code, stderr)
	}
	closeOp := soleCloseOpName(t, dir, id)

	// Retract the op from the working tree only; HEAD keeps it.
	issueDir := filepath.Join(dir, ".act", "ops", id)
	shards, err := os.ReadDir(issueDir)
	if err != nil {
		t.Fatalf("read %s: %v", issueDir, err)
	}
	removed := false
	for _, shard := range shards {
		p := filepath.Join(issueDir, shard.Name(), closeOp)
		if _, err := os.Stat(p); err == nil {
			if err := os.Remove(p); err != nil {
				t.Fatalf("remove %s: %v", p, err)
			}
			removed = true
		}
	}
	if !removed {
		t.Fatalf("did not find %s to remove under %s", closeOp, issueDir)
	}

	stdout, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show: exit %d; stderr=%s", code, stderr)
	}

	var res struct {
		Status     string `json:"status"`
		Durability *struct {
			Uncommitted []string `json:"uncommitted"`
			Missing     []string `json:"missing"`
		} `json:"durability"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("parse show output %q: %v", stdout, err)
	}
	if res.Status != "open" {
		t.Errorf("status = %q, want open (the close op is gone from disk)", res.Status)
	}
	if res.Durability == nil {
		t.Fatalf("no durability key in --json output; stdout=%s", stdout)
	}
	if len(res.Durability.Missing) != 1 || res.Durability.Missing[0] != closeOp {
		t.Errorf("durability.missing = %v, want [%s]", res.Durability.Missing, closeOp)
	}

	for _, want := range []string{
		"WARNING:",
		"BEHIND the committed op log",
		"(open)",
		closeOp,
		"act doctor --check status-vs-oplog",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
}
