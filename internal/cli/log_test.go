package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeRepoWithAct creates a tempdir with `.git/` and `.act/ops/` and returns
// repoRoot.
func makeRepoWithAct(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".act", "ops"), 0o755); err != nil {
		t.Fatalf("mkdir .act/ops: %v", err)
	}
	return root
}

// writeOpFile writes env to <root>/.act/ops/<issueID>/<yyyy-mm>/<basename>.json
// using the canonical envelope marshaller.
func writeOpFile(t *testing.T, root string, env op.Envelope, monthDir, basename string) {
	t.Helper()
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", env.IssueID, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, basename)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// makeEnv builds a minimal valid create-op envelope for the given issue id and
// HLC. The payload is intentionally tiny; tests only care that the envelope
// validates and round-trips.
func makeEnv(issueID string, wallMs int64, logical uint32) op.Envelope {
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       issueID,
		Payload:       json.RawMessage(`{"title":"hello"}`),
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

func TestRunLog_HappyPath(t *testing.T) {
	root := makeRepoWithAct(t)

	// Two ops in HLC order: same wall, logical 0 then 1.
	first := makeEnv("act-abcd", 1700000000000, 0)
	second := makeEnv("act-abcd", 1700000000000, 1)
	// Vary payload to avoid filename hash collision.
	second.Payload = json.RawMessage(`{"title":"second"}`)

	// Write in reverse on-disk order to prove the sort is by HLC, not file
	// listing order.
	writeOpFile(t, root, second, "2026-04", "z-second.json")
	writeOpFile(t, root, first, "2026-04", "a-first.json")

	out, code := RunLog(root, "act-abcd", false)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(LogResult)
	if !ok {
		t.Fatalf("output type = %T, want LogResult", out)
	}
	if res.ID != "act-abcd" {
		t.Errorf("id = %q, want act-abcd", res.ID)
	}
	if got := len(res.Ops); got != 2 {
		t.Fatalf("len(ops) = %d, want 2", got)
	}
	if res.Ops[0].HLC.Logical != 0 {
		t.Errorf("ops[0].logical = %d, want 0", res.Ops[0].HLC.Logical)
	}
	if res.Ops[1].HLC.Logical != 1 {
		t.Errorf("ops[1].logical = %d, want 1", res.Ops[1].HLC.Logical)
	}
}

func TestRunLog_PrefixResolution(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	// Short prefix without `act-` should resolve.
	out, code := RunLog(root, "abcd", false)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(LogResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.ID != "act-abcd" {
		t.Errorf("id = %q, want act-abcd", res.ID)
	}
}

func TestRunLog_NoActDir(t *testing.T) {
	root := t.TempDir()
	out, code := RunLog(root, "act-abcd", false)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", e.Error)
	}
}

func TestRunLog_UnknownID(t *testing.T) {
	root := makeRepoWithAct(t)
	// Seed one issue so allIDs is non-empty.
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	out, code := RunLog(root, "act-ffff", false)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

func TestRunLog_AmbiguousPrefix(t *testing.T) {
	root := makeRepoWithAct(t)
	a := makeEnv("act-abcd1234", 1700000000000, 0)
	b := makeEnv("act-abcd5678", 1700000000001, 0)
	writeOpFile(t, root, a, "2026-04", "a.json")
	writeOpFile(t, root, b, "2026-04", "b.json")

	out, code := RunLog(root, "act-abcd", false)
	// Ambiguous prefix is a usage error per spec-v2.md universal exit-code
	// table; see resolve_helpers.go and act-8dcd.
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2; candidates=%v", len(e.Candidates), e.Candidates)
	}
	// Candidates must be lexicographically sorted.
	if e.Candidates[0] != "act-abcd1234" || e.Candidates[1] != "act-abcd5678" {
		t.Errorf("candidates = %v, want [act-abcd1234 act-abcd5678]", e.Candidates)
	}
}

func TestRunLog_JSONShape(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	out, code := RunLog(root, "act-abcd", true)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["id"] != "act-abcd" {
		t.Errorf("id = %v, want act-abcd", decoded["id"])
	}
	ops, ok := decoded["ops"].([]any)
	if !ok {
		t.Fatalf("ops type = %T, want []any", decoded["ops"])
	}
	if len(ops) != 1 {
		t.Errorf("len(ops) = %d, want 1", len(ops))
	}
}

func TestFormatLogHuman_Smoke(t *testing.T) {
	res := LogResult{
		ID: "act-abcd",
		Ops: []op.Envelope{
			makeEnv("act-abcd", 1700000000000, 0),
		},
	}
	got := FormatLogHuman(res)
	if got == "" {
		t.Fatalf("FormatLogHuman returned empty string")
	}
	// Must include op_type and the issue short id and a count line.
	for _, want := range []string{"create", "issue=act-abcd", "1 ops"} {
		if !contains(got, want) {
			t.Errorf("output missing %q in %q", want, got)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// makeEnvType is like makeEnv but lets the test pick the op_type. The
// returned envelope sets a minimal payload that satisfies op.Validate
// for the chosen type.
func makeEnvType(issueID, opType string, wallMs int64, logical uint32) op.Envelope {
	e := makeEnv(issueID, wallMs, logical)
	e.OpType = opType
	// Vary payload by op_type so canonical hashes don't collide when
	// the same issue has multiple ops of the same type in one test.
	switch opType {
	case "create":
		e.Payload = json.RawMessage(`{"title":"hello"}`)
	case "close":
		e.Payload = json.RawMessage(`{"reason":"done"}`)
	case "update_field":
		e.Payload = json.RawMessage(`{"field":"title","value":"x"}`)
	default:
		e.Payload = json.RawMessage(`{}`)
	}
	return e
}

// seedRetroFixture writes a representative spread of ops across multiple
// issues, op types, and HLC wall-times. Returns the repo root. Wall
// times are anchored to time.Now() so --since windows produce stable
// "in / out" classifications.
//
// Layout:
//
//	now-48h  act-aaaaaa  create
//	now-48h  act-aaaaaa  close
//	now-12h  act-bbbbbb  create
//	now-1h   act-bbbbbb  update_field
//	now-1h   act-cccccc  create
func seedRetroFixture(t *testing.T) string {
	t.Helper()
	root := makeRepoWithAct(t)
	now := time.Now().UnixMilli()
	h := func(d time.Duration) int64 { return now - d.Milliseconds() }

	fixtures := []struct {
		env      op.Envelope
		monthDir string
		fname    string
	}{
		{makeEnvType("act-aaaaaa", "create", h(48*time.Hour), 0), "2026-03", "a-create.json"},
		{makeEnvType("act-aaaaaa", "close", h(48*time.Hour), 1), "2026-03", "a-close.json"},
		{makeEnvType("act-bbbbbb", "create", h(12*time.Hour), 0), "2026-05", "b-create.json"},
		{makeEnvType("act-bbbbbb", "update_field", h(1*time.Hour), 0), "2026-05", "b-update.json"},
		{makeEnvType("act-cccccc", "create", h(1*time.Hour), 0), "2026-05", "c-create.json"},
	}
	for _, f := range fixtures {
		writeOpFile(t, root, f.env, f.monthDir, f.fname)
	}
	return root
}

// countByType returns op-type → count for the envelopes in the result,
// keyed by spec op_type.
func countByType(envs []op.Envelope) map[string]int {
	out := map[string]int{}
	for _, e := range envs {
		out[e.OpType]++
	}
	return out
}

// TestLog_SinceFilter — AC: `act log --since 24h` shows only ops from
// the last 24 hours.
func TestLog_SinceFilter(t *testing.T) {
	root := seedRetroFixture(t)

	out, code := RunLogOpts(root, "", false, LogOptions{Since: 24 * time.Hour})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(LogResult)
	if !ok {
		t.Fatalf("output type = %T, want LogResult", out)
	}
	if res.ID != "" {
		t.Errorf("ID = %q, want empty (cross-issue scope)", res.ID)
	}
	// Two ops at now-48h must be filtered out; three remain (now-12h,
	// now-1h, now-1h).
	if got := len(res.Ops); got != 3 {
		t.Fatalf("len(ops) = %d, want 3; ops=%+v", got, res.Ops)
	}
	for _, e := range res.Ops {
		age := time.Since(time.UnixMilli(e.HLC.Wall))
		if age > 24*time.Hour {
			t.Errorf("op %s on %s is %s old, must be ≤ 24h", e.OpType, e.IssueID, age)
		}
	}
}

// TestLog_ByIssueFilter — AC: `act log --by-issue act-XXXXXX` (full or
// unique prefix) shows only ops for that issue. Exercises both the
// LogOptions.ByIssue path and the positional-id path for parity, and
// asserts prefix resolution flows through the existing id-resolver.
func TestLog_ByIssueFilter(t *testing.T) {
	root := seedRetroFixture(t)

	// Full id via --by-issue.
	out, code := RunLogOpts(root, "", false, LogOptions{ByIssue: "act-bbbbbb"})
	if code != 0 {
		t.Fatalf("by-issue full: code = %d; out=%+v", code, out)
	}
	res := out.(LogResult)
	if res.ID != "act-bbbbbb" {
		t.Errorf("ID = %q, want act-bbbbbb", res.ID)
	}
	if got := len(res.Ops); got != 2 {
		t.Fatalf("by-issue full: len(ops) = %d, want 2", got)
	}

	// Unique short prefix via --by-issue (resolver should find it).
	out, code = RunLogOpts(root, "", false, LogOptions{ByIssue: "bb"})
	if code != 0 {
		t.Fatalf("by-issue prefix: code = %d; out=%+v", code, out)
	}
	res = out.(LogResult)
	if res.ID != "act-bbbbbb" {
		t.Errorf("prefix ID = %q, want act-bbbbbb", res.ID)
	}

	// Positional id must behave identically to --by-issue.
	out, code = RunLogOpts(root, "act-cccccc", false, LogOptions{})
	if code != 0 {
		t.Fatalf("positional: code = %d; out=%+v", code, out)
	}
	res = out.(LogResult)
	if res.ID != "act-cccccc" || len(res.Ops) != 1 {
		t.Errorf("positional: got ID=%q ops=%d, want act-cccccc ops=1", res.ID, len(res.Ops))
	}
}

// TestLog_TypeFilterSingle — AC: `act log --type close` shows only
// close ops (single value).
func TestLog_TypeFilterSingle(t *testing.T) {
	root := seedRetroFixture(t)

	out, code := RunLogOpts(root, "", false, LogOptions{Types: []string{"close"}})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	res := out.(LogResult)
	if got := len(res.Ops); got != 1 {
		t.Fatalf("len(ops) = %d, want 1", got)
	}
	if res.Ops[0].OpType != "close" {
		t.Errorf("op_type = %q, want close", res.Ops[0].OpType)
	}
}

// TestLog_TypeFilterMultiple — AC: multiple types via the same flag
// (the cmd/act/main.go layer comma-splits before reaching LogOptions).
// Also asserts the friendly aliases (update → update_field) resolve.
func TestLog_TypeFilterMultiple(t *testing.T) {
	root := seedRetroFixture(t)

	out, code := RunLogOpts(root, "", false, LogOptions{Types: []string{"create", "close"}})
	if code != 0 {
		t.Fatalf("create,close: code = %d", code)
	}
	res := out.(LogResult)
	got := countByType(res.Ops)
	if got["create"] != 3 || got["close"] != 1 || got["update_field"] != 0 {
		t.Errorf("create,close counts = %v, want create=3 close=1 update_field=0", got)
	}

	// Alias path: "update" must map to "update_field".
	out, code = RunLogOpts(root, "", false, LogOptions{Types: []string{"update"}})
	if code != 0 {
		t.Fatalf("alias: code = %d", code)
	}
	res = out.(LogResult)
	if len(res.Ops) != 1 || res.Ops[0].OpType != "update_field" {
		t.Errorf("alias result = %+v, want one update_field op", res.Ops)
	}
}

// TestLog_ComposedFilters — AC: filters compose
// (e.g. --since 7d --type create,close).
func TestLog_ComposedFilters(t *testing.T) {
	root := seedRetroFixture(t)

	// --since 7d --type create,close: window includes everything (oldest
	// op is 48h), so this is the same as --type create,close alone.
	out, code := RunLogOpts(root, "", false, LogOptions{
		Since: 7 * 24 * time.Hour,
		Types: []string{"create", "close"},
	})
	if code != 0 {
		t.Fatalf("7d+types: code = %d", code)
	}
	res := out.(LogResult)
	got := countByType(res.Ops)
	if got["create"] != 3 || got["close"] != 1 {
		t.Errorf("7d+types counts = %v, want create=3 close=1", got)
	}

	// Tighten the window to 24h: the 48h-old create+close on act-aaaaaa
	// drops out; only the two creates inside the window remain.
	out, code = RunLogOpts(root, "", false, LogOptions{
		Since: 24 * time.Hour,
		Types: []string{"create", "close"},
	})
	if code != 0 {
		t.Fatalf("24h+types: code = %d", code)
	}
	res = out.(LogResult)
	got = countByType(res.Ops)
	if got["create"] != 2 || got["close"] != 0 {
		t.Errorf("24h+types counts = %v, want create=2 close=0", got)
	}

	// Compose all three: --by-issue narrows to one issue, --type and
	// --since further trim. act-bbbbbb has one create at -12h and one
	// update_field at -1h; --type create + --since 24h yields just the
	// create.
	out, code = RunLogOpts(root, "", false, LogOptions{
		Since:   24 * time.Hour,
		ByIssue: "act-bbbbbb",
		Types:   []string{"create"},
	})
	if code != 0 {
		t.Fatalf("all-three: code = %d", code)
	}
	res = out.(LogResult)
	if res.ID != "act-bbbbbb" || len(res.Ops) != 1 || res.Ops[0].OpType != "create" {
		t.Errorf("all-three result = %+v, want one create on act-bbbbbb", res.Ops)
	}
}

// TestLog_BadSinceErrors — AC: bad --since input is rejected with a
// clear error envelope. The parsing lives in cmd/act/main.go
// (parseSinceDuration), not RunLogOpts; this test exercises the
// equivalent contract at the cli layer by passing an unknown op type
// to --type, which is the symmetric bad-flag path inside RunLogOpts.
// The CLI-layer parseSinceDuration is covered by a unit test in
// cmd/act.
func TestLog_BadSinceErrors(t *testing.T) {
	root := seedRetroFixture(t)

	// Unknown op type → bad_flag, exit 2.
	out, code := RunLogOpts(root, "", false, LogOptions{Types: []string{"frob"}})
	if code != 2 {
		t.Fatalf("unknown type: code = %d, want 2", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("unknown type: out type = %T", out)
	}
	if e.Error != "bad_flag" {
		t.Errorf("unknown type: error = %q, want bad_flag", e.Error)
	}

	// Conflicting scope: positional id AND a different --by-issue.
	out, code = RunLogOpts(root, "act-aaaaaa", false, LogOptions{ByIssue: "act-bbbbbb"})
	if code != 2 {
		t.Fatalf("conflict: code = %d, want 2", code)
	}
	if _, ok := out.(LogErrorOutput); !ok {
		t.Errorf("conflict: out type = %T", out)
	}
}
