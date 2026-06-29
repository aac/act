package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeSearchEnv builds a create-op envelope for the given issue id with the
// supplied title + description. The nonce is fixed-but-issue-specific so two
// distinct issues never collide on the canonical hash.
func makeSearchEnv(issueID, title, description string, wallMs int64) op.Envelope {
	payload, _ := json.Marshal(op.CreatePayload{
		Title:       title,
		Description: description,
		Type:        "task",
		Nonce:       "0123456789abcdef0123456789abcdef",
	})
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       issueID,
		Payload:       payload,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: 0,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// makeCloseEnv builds a close-op envelope at the given HLC for issueID.
func makeCloseEnv(issueID string, wallMs int64, logical uint32) op.Envelope {
	payload, _ := json.Marshal(op.ClosePayload{Reason: "done"})
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       issueID,
		Payload:       payload,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// seedSearchRepo writes one create per (id,title,desc) triple plus optional
// trailing close ops for the ids listed in closedIDs. The repoRoot is created
// fresh and returned.
func seedSearchRepo(t *testing.T, ids []struct{ id, title, desc string }, closedIDs []string) string {
	t.Helper()
	root := makeRepoWithAct(t)
	for i, e := range ids {
		env := makeSearchEnv(e.id, e.title, e.desc, 1700000000000+int64(i))
		writeOpFile(t, root, env, "2026-04", "create.json")
	}
	for i, id := range closedIDs {
		env := makeCloseEnv(id, 1700000000000+int64(len(ids)+i)+10, 1)
		writeOpFile(t, root, env, "2026-04", "close.json")
	}
	return root
}

func TestRunSearch_TitleHit(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "alpha bravo charlie", "irrelevant body text"},
		{"act-bbbb", "delta echo foxtrot", "another body"},
	}, nil)

	out, code := RunSearch(root, "alpha", SearchOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(SearchResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.Count != 1 {
		t.Fatalf("count = %d, want 1; matches=%+v", res.Count, res.Matches)
	}
	if res.Matches[0].ID != "act-aaaa" {
		t.Fatalf("matched id = %q, want act-aaaa", res.Matches[0].ID)
	}
	if res.Matches[0].Snippet == "" {
		t.Fatalf("snippet is empty")
	}
}

func TestRunSearch_InTitleExcludesDescriptionMatches(t *testing.T) {
	// "needle" appears in the description of one issue and the title of
	// another. --in title should return only the title-match.
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "totally unrelated heading", "this body mentions needle in passing"},
		{"act-bbbb", "needle in the title", "boring body"},
	}, nil)

	out, code := RunSearch(root, "needle", SearchOptions{In: "title"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res := out.(SearchResult)
	if res.Count != 1 || res.Matches[0].ID != "act-bbbb" {
		t.Fatalf("--in title returned %+v, want only act-bbbb", res.Matches)
	}

	// Sanity: the default scope (all) sees both.
	out2, code := RunSearch(root, "needle", SearchOptions{In: "all"})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if out2.(SearchResult).Count != 2 {
		t.Fatalf("--in all expected 2, got %d", out2.(SearchResult).Count)
	}
}

func TestRunSearch_StatusFilter(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "openhit one", ""},
		{"act-bbbb", "openhit two", ""},
	}, []string{"act-bbbb"})

	out, code := RunSearch(root, "openhit", SearchOptions{Status: "closed"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res := out.(SearchResult)
	if res.Count != 1 || res.Matches[0].ID != "act-bbbb" {
		t.Fatalf("--status closed returned %+v, want only act-bbbb", res.Matches)
	}

	// And the open scope returns the other.
	out2, code := RunSearch(root, "openhit", SearchOptions{Status: "open"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res2 := out2.(SearchResult)
	if res2.Count != 1 || res2.Matches[0].ID != "act-aaaa" {
		t.Fatalf("--status open returned %+v, want only act-aaaa", res2.Matches)
	}
}

func TestRunSearch_EmptyResult(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "hello world", "nothing in particular"},
	}, nil)

	out, code := RunSearch(root, "nonexistentterm", SearchOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res, ok := out.(SearchResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.Count != 0 {
		t.Fatalf("count = %d, want 0", res.Count)
	}
	if res.Matches == nil {
		t.Fatalf("matches is nil; want non-nil empty slice")
	}
	if len(res.Matches) != 0 {
		t.Fatalf("matches has %d entries, want 0", len(res.Matches))
	}
	// And the JSON shape matches the spec.
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	got := string(data)
	if got != `{"matches":[],"count":0}` {
		t.Fatalf("empty-result JSON = %q, want %q", got, `{"matches":[],"count":0}`)
	}
}

func TestRunSearch_EmptyQueryExit2(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "hello", ""},
	}, nil)

	// Empty / whitespace-only queries error with usage exit code.
	_, code := RunSearch(root, "", SearchOptions{})
	if code != 2 {
		t.Fatalf("empty query: exit = %d, want 2", code)
	}
	_, code = RunSearch(root, "   \t  ", SearchOptions{})
	if code != 2 {
		t.Fatalf("whitespace query: exit = %d, want 2", code)
	}
}

// TestRunSearch_PreviouslyBadFTSChars exercises the act-ad52d9 fix: input
// characters that used to confuse FTS5 (hyphens, periods, colons, unbalanced
// quotes/parens) are now safely quoted as literal phrase content, so they
// never produce parse errors.
func TestRunSearch_PreviouslyBadFTSChars(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "post-receive hook design", "details about the post-receive hook"},
		{"act-bbbb", "index.db dirty after crash", "the index.db file goes stale"},
		{"act-cccc", "phase-2 rollout plan", "phase-2 is the coordination plane"},
		{"act-dddd", "regular benign content", "nothing special here"},
	}, nil)

	cases := []struct {
		name        string
		query       string
		wantMin     int
		wantMatchID string // optional: assert at least one match has this id
	}{
		{name: "hyphen_phrase", query: "post-receive hook", wantMin: 1, wantMatchID: "act-aaaa"},
		{name: "period_token", query: "index.db", wantMin: 1, wantMatchID: "act-bbbb"},
		{name: "period_two_tokens", query: "index.db dirty", wantMin: 1, wantMatchID: "act-bbbb"},
		{name: "hyphen_token", query: "phase-2", wantMin: 1, wantMatchID: "act-cccc"},
		{name: "unmatched_paren_literal", query: "hello (world", wantMin: 0},
		{name: "unmatched_quote_literal", query: `"unbalanced`, wantMin: 0},
		{name: "multi_word_simple", query: "regular benign", wantMin: 1, wantMatchID: "act-dddd"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := RunSearch(root, tc.query, SearchOptions{})
			if code != 0 {
				t.Fatalf("query %q: exit = %d, want 0; out=%+v", tc.query, code, out)
			}
			res, ok := out.(SearchResult)
			if !ok {
				t.Fatalf("output type = %T", out)
			}
			if res.Count < tc.wantMin {
				t.Fatalf("query %q: count = %d, want >= %d; matches=%+v", tc.query, res.Count, tc.wantMin, res.Matches)
			}
			if tc.wantMatchID != "" {
				found := false
				for _, m := range res.Matches {
					if m.ID == tc.wantMatchID {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("query %q: missing expected match %s; matches=%+v", tc.query, tc.wantMatchID, res.Matches)
				}
			}
		})
	}
}

// TestQuoteFTSTokens exercises the token-quoting helper in isolation. The
// expected outputs are pasteable into an FTS5 MATCH expression.
func TestQuoteFTSTokens(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "foo", want: `"foo"`},
		{in: "foo bar", want: `"foo" "bar"`},
		{in: "post-receive hook", want: `"post-receive" "hook"`},
		{in: "index.db", want: `"index.db"`},
		{in: "phase-2", want: `"phase-2"`},
		{in: "col:value", want: `"col:value"`},
		{in: `say "hi"`, want: `"say" """hi"""`},
		{in: "  spaced   out  ", want: `"spaced" "out"`},
	}
	for _, tc := range cases {
		got := quoteFTSTokens(tc.in)
		if got != tc.want {
			t.Errorf("quoteFTSTokens(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunSearch_MissingActExit3(t *testing.T) {
	// Repo dir without .act/.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	out, code := RunSearch(root, "anything", SearchOptions{})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if _, ok := out.(SearchErrorOutput); !ok {
		t.Fatalf("output type = %T, want SearchErrorOutput", out)
	}
}

func TestRunSearch_BadInFlagExit2(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "hello", ""},
	}, nil)
	_, code := RunSearch(root, "hello", SearchOptions{In: "bogus"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunSearch_LimitDefault(t *testing.T) {
	// Build many issues, all matching, ensure default limit caps at 50.
	var ids []struct{ id, title, desc string }
	for i := 0; i < 60; i++ {
		ids = append(ids, struct{ id, title, desc string }{
			id:    "act-" + hexN(i),
			title: "commonterm issue",
			desc:  "",
		})
	}
	root := seedSearchRepo(t, ids, nil)

	out, code := RunSearch(root, "commonterm", SearchOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res := out.(SearchResult)
	if res.Count != 50 {
		t.Fatalf("count = %d, want 50 (default limit)", res.Count)
	}

	out2, code := RunSearch(root, "commonterm", SearchOptions{Limit: 5})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if out2.(SearchResult).Count != 5 {
		t.Fatalf("limit=5 produced %d", out2.(SearchResult).Count)
	}
}

// hexN renders i as a 4-char zero-padded lowercase hex string. It is
// intentionally simple — tests only need 0..255 distinct ids.
func hexN(i int) string {
	const hex = "0123456789abcdef"
	return string([]byte{
		hex[(i>>12)&0xf], hex[(i>>8)&0xf], hex[(i>>4)&0xf], hex[i&0xf],
	})
}

// TestRunSearch_IndexFTSPopulated verifies that Rebuild populates the FTS
// virtual table — this is the integration-level guarantee we depend on.
func TestRunSearch_IndexFTSPopulated(t *testing.T) {
	root := seedSearchRepo(t, []struct{ id, title, desc string }{
		{"act-aaaa", "uniqueftstokenhere", "body"},
	}, nil)

	out, code := RunSearch(root, "uniqueftstokenhere", SearchOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res := out.(SearchResult)
	if res.Count != 1 {
		t.Fatalf("expected FTS to surface unique token, got %d matches", res.Count)
	}
}
