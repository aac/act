package main

// act-a025ab: the no-state guard distinguishes "no tracker anywhere" (fresh
// clone, CI — normal) from "the configured tracker remote exists but this
// checkout has none of it" (broken checkout — say so, name the recovery).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const brokencoFreshCloneMsg = "act: no act state in this repo — this is normal in CI / fresh clones"

// brokencoGit runs git with args and fails the test on error.
func brokencoGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// brokencoIsolateEnv points XDG_CONFIG_HOME at an empty temp dir and clears
// ACT_TRACKER_REMOTE, so the operator's real settings never leak in.
func brokencoIsolateEnv(t *testing.T) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("ACT_TRACKER_REMOTE", "")
	return xdg
}

// brokencoHostRepo makes a host git repo named "proj" under a temp dir.
func brokencoHostRepo(t *testing.T) string {
	t.Helper()
	host := filepath.Join(t.TempDir(), "proj")
	brokencoGit(t, "init", "-q", host)
	return host
}

// brokencoTrackerBare builds a real tracker with one issue and publishes it
// as a bare repo at <baresDir>/proj.git. Returns the bare path and issue id.
func brokencoTrackerBare(t *testing.T, baresDir string) (string, string) {
	t.Helper()
	src := brokencoHostRepo(t)
	if _, stderr, code := runActIn(t, src, "init"); code != 0 {
		t.Fatalf("act init: exit %d: %s", code, stderr)
	}
	stdout, stderr, code := runActIn(t, src, "create", "--json", "a real queued issue")
	if code != 0 {
		t.Fatalf("act create: exit %d: %s", code, stderr)
	}
	var created map[string]any
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("parse create output %q: %v", stdout, err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create output has no id: %q", stdout)
	}
	bare := filepath.Join(baresDir, "proj.git")
	brokencoGit(t, "clone", "-q", "--bare", filepath.Join(src, ".act"), bare)
	return bare, id
}

func TestDocClaim_TrackerRemoteFoundNoActDir(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, id := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	_, stderr, code := runActIn(t, host, "show", id)
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr=%q", code, stderr)
	}
	if strings.Contains(stderr, "normal") {
		t.Errorf("broken checkout must not be called normal: %q", stderr)
	}
	actDir := filepath.Join(host, ".act")
	wantRecover := "git clone " + bare + " " + actDir + " (then act init if " + filepath.Join(actDir, "config.json") + " is missing)"
	for _, want := range []string{bare, "from $ACT_TRACKER_REMOTE", "Recover with: " + wantRecover} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q: %q", want, stderr)
		}
	}

	// Write commands take the same branch rather than suggesting a bare
	// `act init`, which would start a divergent tracker.
	_, stderr, code = runActIn(t, host, "create", "x")
	if code != 3 || !strings.Contains(stderr, "tracker exists at "+bare) {
		t.Errorf("create: exit=%d stderr=%q", code, stderr)
	}

	// The named recovery actually recovers: after running it, the queue
	// is visible.
	brokencoGit(t, "clone", "-q", bare, actDir)
	if _, err := os.Stat(filepath.Join(actDir, "config.json")); err != nil {
		if _, stderr, code := runActIn(t, host, "init"); code != 0 {
			t.Fatalf("recovery act init: exit %d: %s", code, stderr)
		}
	}
	stdout, stderr, code := runActIn(t, host, "show", id)
	if code != 0 || !strings.Contains(stdout, "a real queued issue") {
		t.Errorf("after recovery: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDocClaim_TrackerRemoteFoundNoConfigJSON(t *testing.T) {
	xdg := brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, _ := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	brokencoGit(t, "clone", "-q", bare, filepath.Join(host, ".act"))
	// The broken state from the incident: the ops are here, the
	// machine-local config.json is not.
	if err := os.Remove(filepath.Join(host, ".act", "config.json")); err != nil {
		t.Fatal(err)
	}

	// Configure through the XDG file this time, with a ~/-free absolute
	// template.
	cfg := filepath.Join(xdg, "act", "tracker-remote")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("\n"+filepath.Join(bares, "{repo}.git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := runActIn(t, host, "ready", "--json")
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stdout=%q", code, stdout)
	}
	var env struct {
		Error   string         `json:"error"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("parse envelope %q: %v", stdout, err)
	}
	if env.Error != "tracker_not_checked_out" {
		t.Errorf("error = %q, want tracker_not_checked_out", env.Error)
	}
	if env.Details["tracker_remote"] != bare || env.Details["source"] != cfg || env.Details["recover"] != "act init" {
		t.Errorf("details = %v", env.Details)
	}
	if !strings.Contains(env.Message, "has no config.json") || strings.Contains(env.Message, "normal") {
		t.Errorf("message = %q", env.Message)
	}
}

func TestDocClaim_TrackerRemoteAbsentKeepsFreshCloneWording(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"unconfigured", ""},
		{"configured-no-bare", filepath.Join(t.TempDir(), "{repo}.git")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brokencoIsolateEnv(t)
			t.Setenv("ACT_TRACKER_REMOTE", tc.template)
			host := brokencoHostRepo(t)
			_, stderr, code := runActIn(t, host, "show", "act-aaaaaaaa")
			if code != 0 {
				t.Errorf("exit = %d, want 0; stderr=%q", code, stderr)
			}
			if strings.TrimSpace(stderr) != brokencoFreshCloneMsg {
				t.Errorf("stderr = %q, want exactly %q", stderr, brokencoFreshCloneMsg)
			}
		})
	}
}
