package cli

// End-to-end tests for the --description-file flag on `act create` and
// `act update` (issue act-6bbd). The flag must:
//   - read a file's UTF-8 contents as the description payload.
//   - accept "-" as a sentinel for stdin.
//   - be mutually exclusive with --description (exit 2).
//   - reject content over the schema's 16384-char description cap
//     with exit 2 and a clear error message.
//
// Driven through the prebuilt act binary so the flag wiring,
// mutual-exclusion check, and post-load handoff to RunCreate/RunUpdate
// all cover real argv parsing.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeDescFileSite initializes a fresh site with `act init` so subsequent
// create/update commands have a real .act/ to write into. Returns the
// site root path.
func makeDescFileSite(t *testing.T) string {
	t.Helper()
	if actBinaryPath == "" {
		t.Fatalf("descfile: act binary not built (TestMain did not run?)")
	}
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "df@example.com", "df")
	mustRunAct(t, site, 0, "init", "--json")
	return site
}

// runActStdin is a stdin-aware variant of runAct: it pipes `stdin` to the
// child's standard input. We need it for the "-" sentinel path on
// --description-file. Returns stdout, stderr, exit code.
func runActStdin(t *testing.T, site string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(actBinaryPath, args...)
	cmd.Dir = site
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("runActStdin: %v", err)
		}
	}
	return so.String(), se.String(), code
}

// TestCreate_DescriptionFile_HappyPath: a regular file under the cap is
// read into the description and lands on the issue.
func TestCreate_DescriptionFile_HappyPath(t *testing.T) {
	site := makeDescFileSite(t)
	descPath := filepath.Join(site, "desc.md")
	body := "## Bug\n\nSteps to reproduce..."
	if err := os.WriteFile(descPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write desc: %v", err)
	}

	out, _ := mustRunAct(t, site, 0, "create", "df-task", "--description-file", descPath, "--json")
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, out)
	}

	// Round-trip via `act show --json` to confirm the description landed.
	showOut, _ := mustRunAct(t, site, 0, "show", res.ID, "--json")
	var showRes struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(showOut), &showRes); err != nil {
		t.Fatalf("show JSON parse: %v\n%s", err, showOut)
	}
	if showRes.Description != body {
		t.Fatalf("description mismatch:\n got: %q\nwant: %q", showRes.Description, body)
	}
}

// TestCreate_DescriptionFile_StdinDash uses "-" to read the description
// from stdin.
func TestCreate_DescriptionFile_StdinDash(t *testing.T) {
	site := makeDescFileSite(t)
	body := "stdin-fed description payload"

	so, se, code := runActStdin(t, site, body, "create", "df-stdin", "--description-file", "-", "--json")
	if code != 0 {
		t.Fatalf("create code = %d\nstdout:%s\nstderr:%s", code, so, se)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(so), &res); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, so)
	}

	showOut, _ := mustRunAct(t, site, 0, "show", res.ID, "--json")
	var showRes struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(showOut), &showRes); err != nil {
		t.Fatalf("show JSON parse: %v\n%s", err, showOut)
	}
	if showRes.Description != body {
		t.Fatalf("description mismatch:\n got: %q\nwant: %q", showRes.Description, body)
	}
}

// TestCreate_DescriptionFile_MutualExclusion: passing both --description
// and --description-file is a flag-level error (exit 2). The check must
// fire before any file I/O so a missing path here is irrelevant.
func TestCreate_DescriptionFile_MutualExclusion(t *testing.T) {
	site := makeDescFileSite(t)

	_, se, code := runAct(t, site,
		"create", "df-mux",
		"--description", "inline",
		"--description-file", "/nonexistent/path",
		"--json")
	if code != 2 {
		t.Fatalf("exit = %d want 2; stderr:\n%s", code, se)
	}
}

// TestCreate_DescriptionFile_OverCap: a file larger than 16384 chars
// exits 2 with a "bad_flag" envelope and a message that references the
// limit. JSON form is asserted because that's what agents will parse.
func TestCreate_DescriptionFile_OverCap(t *testing.T) {
	site := makeDescFileSite(t)
	descPath := filepath.Join(site, "huge.txt")
	body := strings.Repeat("a", 16385) // one over the cap
	if err := os.WriteFile(descPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	so, _, code := runAct(t, site, "create", "huge", "--description-file", descPath, "--json")
	if code != 2 {
		t.Fatalf("exit = %d want 2; stdout:%s", code, so)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(so), &env); err != nil {
		t.Fatalf("parse error envelope: %v\n%s", err, so)
	}
	if env["error"] != "bad_flag" {
		t.Fatalf("error = %v want bad_flag", env["error"])
	}
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, "16384") {
		t.Fatalf("message does not reference 16384: %q", msg)
	}
}

// TestUpdate_DescriptionFile_HappyPath: --description-file replaces the
// description on an existing issue. We bootstrap with `act create`,
// then update via the file, then read back via `act show`.
func TestUpdate_DescriptionFile_HappyPath(t *testing.T) {
	site := makeDescFileSite(t)
	createOut, _ := mustRunAct(t, site, 0, "create", "df-update", "--description", "v1", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, createOut)
	}

	descPath := filepath.Join(site, "v2.txt")
	body := "v2 — updated from a file"
	if err := os.WriteFile(descPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustRunAct(t, site, 0, "update", created.ID, "--description-file", descPath, "--json")

	showOut, _ := mustRunAct(t, site, 0, "show", created.ID, "--json")
	var show struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(showOut), &show); err != nil {
		t.Fatalf("show JSON parse: %v\n%s", err, showOut)
	}
	if show.Description != body {
		t.Fatalf("description mismatch:\n got: %q\nwant: %q", show.Description, body)
	}
}

// TestUpdate_DescriptionFile_MutualExclusion mirrors the create test.
func TestUpdate_DescriptionFile_MutualExclusion(t *testing.T) {
	site := makeDescFileSite(t)
	createOut, _ := mustRunAct(t, site, 0, "create", "df-update-mux", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, createOut)
	}

	_, _, code := runAct(t, site,
		"update", created.ID,
		"--description", "inline",
		"--description-file", "/nonexistent",
		"--json")
	if code != 2 {
		t.Fatalf("exit = %d want 2", code)
	}
}

// TestUpdate_DescriptionFile_OverCap mirrors the create over-cap test.
func TestUpdate_DescriptionFile_OverCap(t *testing.T) {
	site := makeDescFileSite(t)
	createOut, _ := mustRunAct(t, site, 0, "create", "df-update-huge", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, createOut)
	}

	descPath := filepath.Join(site, "huge2.txt")
	if err := os.WriteFile(descPath, []byte(strings.Repeat("z", 16385)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	so, _, code := runAct(t, site, "update", created.ID, "--description-file", descPath, "--json")
	if code != 2 {
		t.Fatalf("exit = %d want 2", code)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(so), &env); err != nil {
		t.Fatalf("parse error envelope: %v\n%s", err, so)
	}
	if env["error"] != "bad_flag" {
		t.Fatalf("error = %v want bad_flag", env["error"])
	}
}

// TestCreate_DescriptionFile_FileMissing: a non-existent file path is
// exit 3 with a file_not_found envelope.
func TestCreate_DescriptionFile_FileMissing(t *testing.T) {
	site := makeDescFileSite(t)
	missing := filepath.Join(site, "no-such-file.txt")
	so, _, code := runAct(t, site, "create", "ghost", "--description-file", missing, "--json")
	if code != 3 {
		t.Fatalf("exit = %d want 3; stdout:%s", code, so)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(so), &env); err != nil {
		t.Fatalf("parse: %v\n%s", err, so)
	}
	if env["error"] != "file_not_found" {
		t.Fatalf("error = %v want file_not_found", env["error"])
	}
}
