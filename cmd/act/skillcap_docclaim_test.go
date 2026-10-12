package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Doc-claim cover for the id-capture passage in skills/act/SKILL.md
// (act-377d38): read a new ticket's id from `act create --json`, check the
// exit status, and let `jq -e` refuse a missing id.
//
// The passage exists because a session grepped the id out of the sentence
// `act create` prints, the create had failed, and the empty capture was
// passed on. Both tests run the real binary; the second runs the skill's own
// snippet, so an edit that makes the snippet stop stopping goes red here.

var skillcapIDRe = regexp.MustCompile(`^act-[0-9a-f]{6,}$`)

// skillcapEnvelope parses one JSON object from stdout.
func skillcapEnvelope(t *testing.T, what, stdout string) map[string]json.RawMessage {
	t.Helper()
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("%s: stdout is not one JSON object: %v\nstdout=%q", what, err, stdout)
	}
	return env
}

// skillcapSnippet returns the fenced block in SKILL.md that holds the
// `act create --json` capture.
func skillcapSnippet(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "skills", "act", "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	var found []string
	for i, block := range strings.Split(string(raw), "\n```\n") {
		if i%2 == 1 && strings.Contains(block, "act create --json") {
			found = append(found, block)
		}
	}
	if len(found) != 1 {
		t.Fatalf("SKILL.md has %d fenced blocks containing `act create --json`, want 1", len(found))
	}
	return found[0]
}

// skillcapRunSnippet runs script under bash in dir with the test binary on
// PATH as `act`, and returns stdout and the exit code.
func skillcapRunSnippet(t *testing.T, dir, script string) (string, int) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.Symlink(actBinary(t), filepath.Join(binDir, "act")); err != nil {
		t.Fatalf("symlink act: %v", err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var outB strings.Builder
	cmd.Stdout = &outB
	err := cmd.Run()
	if err == nil {
		return outB.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return outB.String(), ee.ExitCode()
	}
	t.Fatalf("bash: failed to launch: %v", err)
	return "", -1
}

// TestDocClaim_Skill_CreateJSONCarriesIDFailedCreateDoesNot pins the two
// facts the passage rests on: a successful `act create --json` exits 0 with
// the new id in the `id` field, and a failed one exits non-zero and writes a
// JSON envelope with no `id`.
func TestDocClaim_Skill_CreateJSONCarriesIDFailedCreateDoesNot(t *testing.T) {
	dir := pass5Site(t)

	out, stderr, code := runActIn(t, dir, "create", "--json", "capture me", "--type", "task")
	if code != 0 {
		t.Fatalf("create --json: exit %d, want 0\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	var id string
	if err := json.Unmarshal(skillcapEnvelope(t, "create --json", out)["id"], &id); err != nil {
		t.Fatalf("create --json: `id` is not a string: %v\nstdout=%s", err, out)
	}
	if !skillcapIDRe.MatchString(id) {
		t.Fatalf("create --json: id %q does not look like a ticket id\nstdout=%s", id, out)
	}
	if showOut, _, showCode := runActIn(t, dir, "show", id, "--json"); showCode != 0 || !strings.Contains(showOut, "capture me") {
		t.Errorf("act show %s: exit %d, want 0 and the created title\nstdout=%s", id, showCode, showOut)
	}

	failures := []struct {
		name string
		dir  string
		args []string
	}{
		{"bad flag value", dir, []string{"create", "--json", "bad", "--type", "nonsense"}},
		{"empty title", dir, []string{"create", "--json", ""}},
		{"outside a git repo", t.TempDir(), []string{"create", "--json", "nowhere"}},
	}
	for _, f := range failures {
		out, stderr, code := runActIn(t, f.dir, f.args...)
		if code == 0 {
			t.Errorf("%s: exit 0, want non-zero\nstdout=%s\nstderr=%s", f.name, out, stderr)
		}
		if _, has := skillcapEnvelope(t, f.name, out)["id"]; has {
			t.Errorf("%s: failed create wrote an `id` field\nstdout=%s", f.name, out)
		}
	}
}

// TestDocClaim_Skill_CreateIDCaptureSnippetStopsOnFailedCreate runs the
// snippet SKILL.md prints. On a good create it leaves a real id in $id; on a
// failed one it exits before the command that would have used the id. It also
// pins the reason the snippet uses `jq -e`: a plain `jq -r .id` on a failed
// create prints `null`, which a non-empty check would accept.
func TestDocClaim_Skill_CreateIDCaptureSnippetStopsOnFailedCreate(t *testing.T) {
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; skipping the snippet run", tool)
		}
	}
	dir := pass5Site(t)
	snippet := skillcapSnippet(t)
	const next = "\necho \"next-command-ran id=$id\"\n"

	out, code := skillcapRunSnippet(t, dir, snippet+next)
	m := regexp.MustCompile(`(?m)^next-command-ran id=(\S+)$`).FindStringSubmatch(out)
	if code != 0 || m == nil || !skillcapIDRe.MatchString(m[1]) {
		t.Fatalf("good create: exit %d, stdout=%q; want exit 0 and a ticket id in $id", code, out)
	}
	if _, _, showCode := runActIn(t, dir, "show", m[1]); showCode != 0 {
		t.Errorf("captured id %q is not a ticket: act show exit %d", m[1], showCode)
	}

	bad := strings.Replace(snippet, "--type task", "--type nonsense", 1)
	if bad == snippet {
		t.Fatalf("snippet has no `--type task` to break:\n%s", snippet)
	}
	out, code = skillcapRunSnippet(t, dir, bad+next)
	if code == 0 || strings.Contains(out, "next-command-ran") {
		t.Errorf("failed create: exit %d, stdout=%q; want non-zero and the next command never run", code, out)
	}

	// The second line alone must also stop: feed it a failed create's envelope.
	lines := strings.Split(strings.TrimSpace(snippet), "\n")
	envelope, _, _ := runActIn(t, dir, "create", "--json", "bad", "--type", "nonsense")
	second := "out='" + strings.TrimSpace(envelope) + "'\n" + lines[len(lines)-1]
	out, code = skillcapRunSnippet(t, dir, second+next)
	if code == 0 || strings.Contains(out, "next-command-ran") {
		t.Errorf("jq line on an error envelope: exit %d, stdout=%q; want non-zero and the next command never run", code, out)
	}

	out, _ = skillcapRunSnippet(t, dir, `act create --json "bad" --type nonsense | jq -r .id`)
	if strings.TrimSpace(out) != "null" {
		t.Errorf("plain `jq -r .id` on a failed create printed %q, SKILL.md says `null`", strings.TrimSpace(out))
	}
}
