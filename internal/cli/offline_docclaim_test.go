package cli

// Phase 2 ticket 3b (act-4a604d) — doc-claim regression tests for
// --offline, slow-write warning literal, slow-write log schema, and
// pending-push log schema. Each test asserts the doc claim at the
// user-visible boundary (stderr text, on-disk JSON record, or
// `act create --help` output) — the spirit of the act-ff5c
// TestDocClaim_* convention (see CLAUDE.md "Documentation
// discipline").

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
)

// TestDocClaim_SlowWrite_WarningText asserts the literal stderr pattern
// promised by docs/spec-v2.md is what the binary actually emits when
// the slow-write path fires. The boundary is process stderr (the same
// surface an agent would see).
func TestDocClaim_SlowWrite_WarningText(t *testing.T) {
	root := makeCreateRepo(t)
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "1200")

	stderr := captureStderr(t, func() {
		_, code := RunCreate(root, CreateOptions{Title: "slow-doc", Type: "task"})
		if code != 0 {
			t.Fatalf("RunCreate code=%d", code)
		}
	})

	// Pinned literal prefix and suffix per docs/spec-v2.md "Slow-write
	// observation" section.
	if !strings.Contains(stderr, "act: slow write detected (") {
		t.Errorf("stderr missing pinned prefix:\n%s", stderr)
	}
	if !strings.Contains(stderr, "; see .act/.slow-writes") {
		t.Errorf("stderr missing pinned suffix:\n%s", stderr)
	}
}

// TestDocClaim_SlowWrite_LogSchema asserts the on-disk JSON-line record
// contains the four pinned field names: timestamp, op_id,
// duration_ms, op_type. The doc-claim layer is the field-name set,
// not the values; values are covered by TestSlowWrite_SchemaIsCorrect.
func TestDocClaim_SlowWrite_LogSchema(t *testing.T) {
	root := makeCreateRepo(t)
	t.Setenv("ACT_TEST_SLOW_COMMIT_MS", "1200")

	_ = captureStderr(t, func() {
		_, code := RunCreate(root, CreateOptions{Title: "slow-schema-doc", Type: "task"})
		if code != 0 {
			t.Fatalf("RunCreate code=%d", code)
		}
	})

	paths := config.Layout(root)
	body, err := os.ReadFile(filepath.Join(paths.Root, ".slow-writes"))
	if err != nil {
		t.Fatalf("read .slow-writes: %v", err)
	}
	line := strings.SplitN(strings.TrimRight(string(body), "\n"), "\n", 2)[0]

	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("unmarshal: %v (line=%q)", err, line)
	}
	for _, want := range []string{"timestamp", "op_id", "duration_ms", "op_type"} {
		if _, ok := rec[want]; !ok {
			t.Errorf("slow-write record missing field %q: %s", want, line)
		}
	}
}

// TestDocClaim_PendingPush_Schema asserts the on-disk JSON-line record
// for .act/.pending-pushes contains the three pinned field names:
// timestamp, sha, op_type. The trigger is an --offline create.
func TestDocClaim_PendingPush_Schema(t *testing.T) {
	root, _ := makeRepoWithRemoteOrigin(t)
	_, code := RunCreate(root, CreateOptions{Title: "pp-schema", Type: "task", Offline: true})
	if code != 0 {
		t.Fatalf("RunCreate (offline) code=%d", code)
	}

	paths := config.Layout(root)
	body, err := os.ReadFile(filepath.Join(paths.Root, ".pending-pushes"))
	if err != nil {
		t.Fatalf("read .pending-pushes: %v", err)
	}
	line := strings.SplitN(strings.TrimRight(string(body), "\n"), "\n", 2)[0]

	var rec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("unmarshal: %v (line=%q)", err, line)
	}
	for _, want := range []string{"timestamp", "sha", "op_type"} {
		if _, ok := rec[want]; !ok {
			t.Errorf("pending-push record missing field %q: %s", want, line)
		}
	}
}

// TestDocClaim_Offline_FlagHelp asserts that `act create --help`
// advertises the --offline flag with the canonical help text. Drives
// the real binary (built by TestMain) inside an initialized act site
// so the dispatcher reaches the create subcommand's flag parser.
func TestDocClaim_Offline_FlagHelp(t *testing.T) {
	if actBinaryPath == "" {
		t.Skip("act binary not built by TestMain")
	}
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")

	cmd := exec.Command(actBinaryPath, "create", "--help")
	cmd.Dir = site
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	_ = cmd.Run() // --help often returns non-zero with flag.ContinueOnError; ignore code

	combined := so.String() + se.String()
	// Go's flag package emits each flag with a single leading dash
	// ("-offline") regardless of how it's invoked ("--offline" or
	// "-offline"); both forms are accepted at the call site. We grep
	// for the single-dash listing because that's what --help prints.
	if !strings.Contains(combined, "-offline") {
		t.Errorf("`act create --help` missing -offline flag listing:\n%s", combined)
	}
	if !strings.Contains(combined, "commit locally, skip push") {
		t.Errorf("`act create --help` missing canonical --offline help text:\n%s", combined)
	}
}
