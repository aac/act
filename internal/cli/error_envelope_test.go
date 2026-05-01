package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestErrorEnvelope_Shape walks every command and triggers each error class
// listed in the spec table, asserting the unified envelope shape:
//
//	{"error": "<string>", "message": "<non-empty>", "details": {<object>}}
//
// Per spec-v2.md §error-envelope, every non-zero exit with --json MUST
// emit this object on stdout. The test exercises the cli.Run* functions
// directly (the same code paths cmd/act uses) and feeds their structured
// outputs through cli.Normalize + cli.Emit, then re-parses the JSON to
// verify the on-the-wire shape rather than the in-memory typed struct.
func TestErrorEnvelope_Shape(t *testing.T) {
	cases := []struct {
		name string
		// run produces a typed payload + exit code, mirroring what main.go
		// would receive from a Run* call. The test then normalises it
		// through the same path the CLI uses for JSON output.
		run func(t *testing.T) (any, int)
		// wantCode is the expected exit code (always non-zero for errors).
		wantCode int
		// wantError is the error slug we expect after normalisation.
		wantError string
	}{
		{
			name: "show: not_in_git (no .act/)",
			run: func(t *testing.T) (any, int) {
				root := makeRepo(t) // .git/ exists, no .act/
				return RunShow(root, ShowOptions{ID: "act-12345678"})
			},
			wantCode:  3,
			wantError: "not_in_git",
		},
		{
			name: "show: issue_not_found",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				return RunShow(root, ShowOptions{ID: "act-deadbeef"})
			},
			wantCode:  3,
			wantError: "issue_not_found",
		},
		{
			name: "log: issue_not_found",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				return RunLog(root, "act-deadbeef", true)
			},
			wantCode:  3,
			wantError: "issue_not_found",
		},
		{
			name: "list: no_repo (missing .act/)",
			run: func(t *testing.T) (any, int) {
				return RunList(t.TempDir(), ListOptions{Limit: 50})
			},
			wantCode:  3,
			wantError: "no_repo",
		},
		{
			name: "ready: no_repo",
			run: func(t *testing.T) (any, int) {
				return RunReady(t.TempDir(), ReadyOptions{Limit: 50})
			},
			wantCode:  3,
			wantError: "no_repo",
		},
		{
			name: "search: not_in_git",
			run: func(t *testing.T) (any, int) {
				return RunSearch(t.TempDir(), "foo", SearchOptions{Limit: 50})
			},
			wantCode:  3,
			wantError: "not_in_git",
		},
		{
			name: "create: act_not_initialized",
			run: func(t *testing.T) (any, int) {
				p := 1
				return RunCreate(makeRepo(t), CreateOptions{Title: "x", Type: "task", Priority: &p})
			},
			wantCode:  3,
			wantError: "act_not_initialized",
		},
		{
			name: "close: issue_not_found",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				return RunClose(root, CloseOptions{ID: "act-deadbeef"})
			},
			wantCode:  3,
			wantError: "issue_not_found",
		},
		{
			name: "update: bad_flag (no field flags)",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				crOut, _ := RunCreate(root, CreateOptions{Title: "x", Type: "task"})
				return RunUpdate(root, UpdateOptions{ID: crOut.(CreateResult).ID})
			},
			wantCode:  2,
			wantError: "bad_flag",
		},
		{
			name: "update: issue_not_found",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				s := "open"
				return RunUpdate(root, UpdateOptions{ID: "act-deadbeef", Status: &s})
			},
			wantCode:  3,
			wantError: "issue_not_found",
		},
		{
			name: "depadd: bad_flag (--type bogus)",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				return RunDepAdd(root, DepAddOptions{
					Child:    "act-12345678",
					Parent:   "act-87654321",
					EdgeType: "bogus",
				})
			},
			wantCode:  2,
			wantError: "bad_flag",
		},
		{
			name: "depadd: issue_not_found",
			run: func(t *testing.T) (any, int) {
				root := makeCreateRepo(t)
				return RunDepAdd(root, DepAddOptions{
					Child:    "act-deadbeef",
					Parent:   "act-cafef00d",
					EdgeType: "blocks",
				})
			},
			wantCode:  3,
			wantError: "issue_not_found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, code := tc.run(t)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (payload=%+v)", code, tc.wantCode, payload)
			}
			env := Normalize(payload)
			if env.Error != tc.wantError {
				t.Errorf("error = %q, want %q (payload=%+v)", env.Error, tc.wantError, payload)
			}
			if env.Message == "" {
				t.Errorf("message is empty (payload=%+v)", payload)
			}
			// Re-marshal through Emit and re-parse to verify the on-the-wire
			// JSON shape matches the spec exactly.
			var stdout bytes.Buffer
			Emit(env, true, &stdout, nil)
			var raw map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
				t.Fatalf("envelope JSON did not parse: %v\n%s", err, stdout.String())
			}
			// 1. error must be a non-empty string.
			gotErr, ok := raw["error"].(string)
			if !ok || gotErr == "" {
				t.Errorf("envelope.error must be a non-empty string; got %T %v", raw["error"], raw["error"])
			}
			// 2. message must be a non-empty string.
			gotMsg, ok := raw["message"].(string)
			if !ok || gotMsg == "" {
				t.Errorf("envelope.message must be a non-empty string; got %T %v", raw["message"], raw["message"])
			}
			// 3. details must be present and an object (possibly empty).
			gotDetails, ok := raw["details"]
			if !ok {
				t.Errorf("envelope.details must always be present (got missing key)")
			} else if _, ok := gotDetails.(map[string]any); !ok {
				t.Errorf("envelope.details must be an object; got %T %v", gotDetails, gotDetails)
			}
			// 4. no other top-level keys leak. Spec mandates exactly three.
			for k := range raw {
				if k != "error" && k != "message" && k != "details" {
					t.Errorf("unexpected top-level key %q in envelope", k)
				}
			}
		})
	}
}

// TestErrorEnvelope_DepAddCycle covers the non-trivial dep-add cycle path.
// Historically this command emitted `{"error":{"kind":"cycle","path":...}}`,
// where `error` was an object rather than a string. The cmd/act layer now
// flattens it into the canonical envelope; this test mirrors that flow
// with cli.New so the on-the-wire JSON shape is asserted directly.
func TestErrorEnvelope_DepAddCycle(t *testing.T) {
	root := makeCreateRepo(t)
	aOut, _ := RunCreate(root, CreateOptions{Title: "A", Type: "task"})
	bOut, _ := RunCreate(root, CreateOptions{Title: "B", Type: "task"})
	cOut, _ := RunCreate(root, CreateOptions{Title: "C", Type: "task"})
	a := aOut.(CreateResult).ID
	b := bOut.(CreateResult).ID
	c := cOut.(CreateResult).ID
	if _, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("A blocks B: code=%d", code)
	}
	if _, code := RunDepAdd(root, DepAddOptions{Child: b, Parent: c, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("B blocks C: code=%d", code)
	}
	out, code := RunDepAdd(root, DepAddOptions{Child: c, Parent: a, EdgeType: "blocks"})
	if code != 1 {
		t.Fatalf("C blocks A: code=%d, want 1", code)
	}
	cyc, ok := out.(DepAddCycleOutput)
	if !ok {
		t.Fatalf("type=%T, want DepAddCycleOutput", out)
	}
	env := New("cycle", "act dep add: cycle detected: "+strings.Join(cyc.Error.Path, " -> "), map[string]any{
		"path": cyc.Error.Path,
	})
	var stdout bytes.Buffer
	Emit(env, true, &stdout, nil)
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("cycle envelope JSON did not parse: %v\n%s", err, stdout.String())
	}
	if raw["error"] != "cycle" {
		t.Errorf("error = %v, want \"cycle\"", raw["error"])
	}
	d, ok := raw["details"].(map[string]any)
	if !ok {
		t.Fatalf("details must be an object; got %T", raw["details"])
	}
	pathAny, ok := d["path"].([]any)
	if !ok {
		t.Fatalf("details.path must be a JSON array; got %T", d["path"])
	}
	if len(pathAny) < 2 {
		t.Errorf("details.path too short: %v", pathAny)
	}
}

// TestErrorEnvelope_NormalizeLegacyShape verifies cli.Normalize copes with
// the historical `{"error":{"kind":"cycle","path":[...]}}` form so any
// caller still emitting that shape gets uniformly re-shaped.
func TestErrorEnvelope_NormalizeLegacyShape(t *testing.T) {
	legacy := map[string]any{
		"error": map[string]any{
			"kind": "cycle",
			"path": []string{"act-aaaa", "act-bbbb", "act-aaaa"},
		},
	}
	env := Normalize(legacy)
	if env.Error != "cycle" {
		t.Errorf("error = %q, want cycle", env.Error)
	}
	if env.Details["path"] == nil {
		t.Errorf("details.path missing; got %+v", env.Details)
	}
}

// TestErrorEnvelope_EmitHumanForm verifies the non-JSON branch writes the
// human-readable message to stderr and leaves stdout untouched.
func TestErrorEnvelope_EmitHumanForm(t *testing.T) {
	env := New("issue_not_found", "act show: no issue matches 'act-deadbeef'", map[string]any{"query": "act-deadbeef"})
	var stdout, stderr bytes.Buffer
	Emit(env, false, &stdout, &stderr)
	if stdout.Len() != 0 {
		t.Errorf("stdout must be empty under human form; got %q", stdout.String())
	}
	got := strings.TrimRight(stderr.String(), "\n")
	if got != env.Message {
		t.Errorf("stderr = %q, want %q", got, env.Message)
	}
}

// TestErrorEnvelope_DetailsAlwaysPresent confirms a nil Details map still
// renders as `{}` on the wire, satisfying the "always present, may be
// empty object" rule.
func TestErrorEnvelope_DetailsAlwaysPresent(t *testing.T) {
	env := New("bad_flag", "act show: usage: act show <id>", nil)
	var stdout bytes.Buffer
	Emit(env, true, &stdout, nil)
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("envelope JSON did not parse: %v\n%s", err, stdout.String())
	}
	d, ok := raw["details"]
	if !ok {
		t.Fatalf("details key missing; got %v", raw)
	}
	m, ok := d.(map[string]any)
	if !ok {
		t.Fatalf("details must be an object; got %T", d)
	}
	if len(m) != 0 {
		t.Errorf("details must be empty when nil was passed; got %v", m)
	}
}

// TestErrorEnvelope_StderrTailCapture verifies SplitWrappedError +
// CaptureStderrTail produce a clean message and a capped tail.
func TestErrorEnvelope_StderrTailCapture(t *testing.T) {
	msg, tail := SplitWrappedError("claim: pull --rebase: git pull --rebase: exit status 1 (output: fatal: not a git repository)")
	if tail == "" {
		t.Errorf("stderr tail empty; want extracted output")
	}
	if strings.Contains(msg, "fatal: not a git repository") {
		t.Errorf("message must not embed raw stderr: %q", msg)
	}
	long := strings.Repeat("x", MaxStderrTail+100)
	got := CaptureStderrTail(long)
	if len(got) != MaxStderrTail {
		t.Errorf("CaptureStderrTail length = %d, want %d", len(got), MaxStderrTail)
	}
}
