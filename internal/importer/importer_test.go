package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/op"
)

// fakeGitOps records the calls made by the importer without invoking git.
type fakeGitOps struct {
	staged    []string
	commits   []string
	pushed    int
	failStage bool
}

func (f *fakeGitOps) StageOpFile(p string) error {
	if f.failStage {
		return fmt.Errorf("fake stage error")
	}
	f.staged = append(f.staged, p)
	return nil
}

func (f *fakeGitOps) Commit(msg string) error { f.commits = append(f.commits, msg); return nil }
func (f *fakeGitOps) Push() error             { f.pushed++; return nil }

// setup creates a tempdir with a minimal `.act/` layout plus a config.json
// containing a fixed node_id. Returns the repo root.
func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	paths := config.Layout(root)
	if err := config.InitDirs(paths); err != nil {
		t.Fatalf("init dirs: %v", err)
	}
	cfg := config.Config{
		NodeID:    "deadbeef",
		CreatedAt: "2026-04-29T00:00:00Z",
		Version:   "0.1.0",
	}
	if err := config.WriteConfig(paths, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return root
}

// goodNonce returns a 32-char hex nonce derived from a label so tests are
// deterministic across runs.
func goodNonce(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:16])
}

// authorJSONL writes a JSONL file with two creates and one update_field op.
// The two creates use bootstrap ids "boot-1" and "boot-2"; the update_field
// targets "boot-1".
func authorJSONL(t *testing.T, dir string) string {
	t.Helper()
	type env struct {
		OpVersion     int             `json:"op_version"`
		SchemaVersion int             `json:"schema_version"`
		OpType        string          `json:"op_type"`
		IssueID       string          `json:"issue_id"`
		Payload       json.RawMessage `json:"payload"`
		HLC           any             `json:"hlc"`
		NodeID        string          `json:"node_id"`
	}
	srcHLC := map[string]any{
		"wall":    "2024-01-01T00:00:00.000Z",
		"logical": 0,
		"node_id": "ffffffff",
	}
	create := func(id, title, nonce string) env {
		p := map[string]any{
			"title": title,
			"type":  "task",
			"nonce": nonce,
		}
		raw, _ := canonicaljson.Marshal(p)
		return env{
			OpVersion:     1,
			SchemaVersion: 1,
			OpType:        "create",
			IssueID:       id,
			Payload:       raw,
			HLC:           srcHLC,
			NodeID:        "ffffffff",
		}
	}
	upd := env{
		OpVersion:     1,
		SchemaVersion: 1,
		OpType:        "update_field",
		IssueID:       "boot-1",
		Payload:       json.RawMessage(`{"field":"title","value":"renamed"}`),
		HLC:           srcHLC,
		NodeID:        "ffffffff",
	}
	all := []env{
		create("boot-1", "first", goodNonce("first")),
		create("boot-2", "second", goodNonce("second")),
		upd,
	}
	var b strings.Builder
	for _, e := range all {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func countOpFiles(t *testing.T, opsDir string) int {
	t.Helper()
	count := 0
	_ = filepath.Walk(opsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".json") {
			count++
		}
		return nil
	})
	return count
}

func TestRun_Basic(t *testing.T) {
	root := setup(t)
	jsonl := authorJSONL(t, root)
	g := &fakeGitOps{}

	res, err := Run(root, Options{JSONLPath: jsonl}, g)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Idempotent {
		t.Fatalf("expected non-idempotent first run")
	}
	if res.OpsImported != 3 {
		t.Fatalf("ops imported = %d, want 3", res.OpsImported)
	}
	if res.IssuesCreated != 2 {
		t.Fatalf("issues created = %d, want 2", res.IssuesCreated)
	}
	if res.MappingFile == "" {
		t.Fatalf("mapping file empty")
	}

	// 3 op files on disk.
	paths := config.Layout(root)
	if got := countOpFiles(t, paths.Ops); got != 3 {
		t.Fatalf("op files on disk = %d, want 3", got)
	}

	// One commit with the expected subject.
	if len(g.commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(g.commits))
	}
	if !strings.HasPrefix(g.commits[0], "act-import: issues.jsonl sha=") {
		t.Fatalf("commit subject = %q", g.commits[0])
	}

	// Mapping file shape: source contains @<sha>, mapping has 2 entries,
	// canonical JSON (no trailing newline, sorted keys).
	data, err := os.ReadFile(res.MappingFile)
	if err != nil {
		t.Fatalf("read mapping: %v", err)
	}
	if strings.HasSuffix(string(data), "\n") {
		t.Fatalf("mapping file ends with newline (not canonical)")
	}
	var mf mappingFile
	if err := json.Unmarshal(data, &mf); err != nil {
		t.Fatalf("unmarshal mapping: %v", err)
	}
	if !strings.HasPrefix(mf.Source, "issues.jsonl@") {
		t.Fatalf("source = %q", mf.Source)
	}
	if len(mf.Mapping) != 2 {
		t.Fatalf("mapping len = %d, want 2", len(mf.Mapping))
	}
	if _, ok := mf.Mapping["boot-1"]; !ok {
		t.Fatalf("missing boot-1 entry: %v", mf.Mapping)
	}
	if _, ok := mf.Mapping["boot-2"]; !ok {
		t.Fatalf("missing boot-2 entry: %v", mf.Mapping)
	}

	// Confirm the canonical-JSON bytes are byte-identical to a fresh
	// re-marshal of the parsed value (sorted keys invariant).
	canon, err := canonicaljson.Marshal(mf)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(canon) != string(data) {
		t.Fatalf("mapping file is not canonical JSON\non disk:    %s\ncanonical:  %s", data, canon)
	}
}

func TestRun_Idempotent(t *testing.T) {
	root := setup(t)
	jsonl := authorJSONL(t, root)
	g := &fakeGitOps{}

	res1, err := Run(root, Options{JSONLPath: jsonl}, g)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res1.Idempotent {
		t.Fatalf("first run was idempotent")
	}

	res2, err := Run(root, Options{JSONLPath: jsonl}, g)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !res2.Idempotent {
		t.Fatalf("second run was not idempotent")
	}
	if res2.OpsImported != 0 {
		t.Fatalf("second run ops = %d, want 0", res2.OpsImported)
	}
	if res2.MappingFile != res1.MappingFile {
		t.Fatalf("mapping file mismatch: %s vs %s", res2.MappingFile, res1.MappingFile)
	}
	// Second run produced no additional commits.
	if len(g.commits) != 1 {
		t.Fatalf("commits = %d after idempotent rerun, want 1", len(g.commits))
	}
}

func TestRun_MalformedJSONL_NoSideEffects(t *testing.T) {
	root := setup(t)
	jsonl := filepath.Join(root, "bad.jsonl")
	// First line is fine; second line is missing op_type entirely.
	good := fmt.Sprintf(
		`{"op_version":1,"schema_version":1,"op_type":"create","issue_id":"b1","payload":{"title":"x","type":"task","nonce":%q},"hlc":{"wall":"2024-01-01T00:00:00.000Z","logical":0,"node_id":"ffffffff"},"node_id":"ffffffff"}`,
		goodNonce("x"),
	)
	bad := `{"op_version":1,"schema_version":1,"issue_id":"b2","payload":{"title":"y","type":"task"},"hlc":{},"node_id":"ffffffff"}`
	if err := os.WriteFile(jsonl, []byte(good+"\n"+bad+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	paths := config.Layout(root)
	preCount := countOpFiles(t, paths.Ops)

	g := &fakeGitOps{}
	_, err := Run(root, Options{JSONLPath: jsonl}, g)
	if err == nil {
		t.Fatalf("expected error on malformed jsonl")
	}
	if !strings.Contains(err.Error(), "import_invalid_jsonl") {
		t.Fatalf("error not tagged import_invalid_jsonl: %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error missing line number: %v", err)
	}

	// No new ops on disk, no commit.
	postCount := countOpFiles(t, paths.Ops)
	if postCount != preCount {
		t.Fatalf("ops dir grew on failed validation: pre=%d post=%d", preCount, postCount)
	}
	if len(g.commits) != 0 {
		t.Fatalf("commits on failed validation: %v", g.commits)
	}
	// imports/ should also be empty (no mapping file written).
	entries, _ := os.ReadDir(paths.Imports)
	for _, e := range entries {
		if !e.IsDir() {
			t.Fatalf("imports/ contains %s after failed run", e.Name())
		}
	}
}

func TestLoadAllMappings(t *testing.T) {
	root := setup(t)
	paths := config.Layout(root)

	mf1 := mappingFile{
		Source:  "a.jsonl@aaa",
		Mapping: map[string]string{"b1": "act-1111", "b2": "act-2222"},
	}
	mf2 := mappingFile{
		Source:  "b.jsonl@bbb",
		Mapping: map[string]string{"b3": "act-3333"},
	}
	for name, mf := range map[string]mappingFile{
		"2026-01-01T00:00:00.000Z.json": mf1,
		"2026-02-01T00:00:00.000Z.json": mf2,
	} {
		data, err := canonicaljson.Marshal(mf)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(filepath.Join(paths.Imports, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	all, err := LoadAllMappings(paths.Imports)
	if err != nil {
		t.Fatalf("LoadAllMappings: %v", err)
	}
	want := map[string]string{
		"b1": "act-1111",
		"b2": "act-2222",
		"b3": "act-3333",
	}
	if len(all) != len(want) {
		t.Fatalf("got %d entries, want %d (%v)", len(all), len(want), all)
	}
	for k, v := range want {
		if all[k] != v {
			t.Fatalf("mapping[%q] = %q, want %q", k, all[k], v)
		}
	}
}

func TestLoadAllMappings_MissingDir(t *testing.T) {
	all, err := LoadAllMappings(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadAllMappings on missing dir: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected empty map, got %v", all)
	}
}

func TestRun_NoCommit(t *testing.T) {
	root := setup(t)
	jsonl := authorJSONL(t, root)

	res, err := Run(root, Options{JSONLPath: jsonl, NoCommit: true}, nil)
	if err != nil {
		t.Fatalf("Run no-commit: %v", err)
	}
	if res.OpsImported != 3 {
		t.Fatalf("ops imported = %d, want 3", res.OpsImported)
	}
	// Mapping file still written.
	if _, err := os.Stat(res.MappingFile); err != nil {
		t.Fatalf("mapping file missing: %v", err)
	}
}

// TestImporterMappingFilename_NoColon is the regression test for act-561c63.
// The importer used to emit `.act/imports/<iso>.json` mapping filenames with
// colons in the time component, which break `git checkout` on NTFS hosts
// before any Go code runs (same class of bug as the op-filename fix in
// act-2f3d). New mapping filenames must use the dash form (op.IsoLayout).
//
// User-visible boundary: the file basename on disk, not the in-memory format
// string — this is what `git checkout` actually sees on Windows.
func TestImporterMappingFilename_NoColon(t *testing.T) {
	root := setup(t)
	jsonl := authorJSONL(t, root)

	res, err := Run(root, Options{JSONLPath: jsonl}, &fakeGitOps{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.MappingFile == "" {
		t.Fatalf("mapping file empty")
	}
	base := filepath.Base(res.MappingFile)
	if strings.ContainsRune(base, ':') {
		t.Fatalf("mapping filename %q contains ':' (NTFS-unsafe); want dash-form ISO layout per op.IsoLayout", base)
	}
	if !strings.HasSuffix(base, ".json") {
		t.Fatalf("mapping filename %q missing .json suffix", base)
	}
	// Confirm the dash-form layout: `YYYY-MM-DDTHH-MM-SS.sssZ.json`.
	stem := strings.TrimSuffix(base, ".json")
	if _, perr := time.ParseInLocation(op.IsoLayout, stem, time.UTC); perr != nil {
		t.Fatalf("mapping filename stem %q does not parse as op.IsoLayout: %v", stem, perr)
	}
}
