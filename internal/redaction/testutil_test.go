package redaction

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// Shared fixtures for the redaction tests. Every helper is prefixed red.

const (
	redSecret = "PLANTED-4417-9902-7731"
	redNode   = "7f3a91c2"
	redNodeB  = "1b2c3d4e"
	redNonce  = "0123456789abcdef0123456789abcdef"
	redIssue  = "act-ae2e"
	redWall   = int64(1764358860229) // 2025-11-28T19:41:00.229Z
)

// redEnv builds an envelope for issue/opType at (wall, logical, node) with
// the given payload JSON.
func redEnv(t *testing.T, issue, opType string, wall int64, logical uint32, node, payload string) op.Envelope {
	t.Helper()
	if !json.Valid([]byte(payload)) {
		t.Fatalf("redEnv: payload is not JSON: %s", payload)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issue,
		Payload:       json.RawMessage(payload),
		HLC:           hlc.HLC{Wall: wall, Logical: logical, NodeID: node},
		NodeID:        node,
	}
}

// redMarshal is env.Marshal or a test failure.
func redMarshal(t *testing.T, env op.Envelope) []byte {
	t.Helper()
	b, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// redWriteOp writes env under stateRoot/ops at its canonical path and
// returns the path relative to stateRoot.
func redWriteOp(t *testing.T, stateRoot string, env op.Envelope) string {
	t.Helper()
	shard := op.ShardDir(filepath.Join(stateRoot, "ops"), env.IssueID, env.HLC.Wall)
	return redWriteAt(t, stateRoot, filepath.Join(shard, op.Filename(env)), redMarshal(t, env))
}

// redWriteAt writes body at abs (creating its directory) and returns the
// path relative to stateRoot, slash-separated.
func redWriteAt(t *testing.T, stateRoot, abs string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(stateRoot, abs)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// redFiles lists every regular file under stateRoot/sub, relative to
// stateRoot, sorted.
func redFiles(t *testing.T, stateRoot, sub string) []string {
	t.Helper()
	var out []string
	root := filepath.Join(stateRoot, sub)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(stateRoot, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// redRead parses the op file at rel.
func redRead(t *testing.T, stateRoot, rel string) op.Envelope {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	env, err := op.UnmarshalTolerant(b)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return env
}

// redFoldLive folds stateRoot/ops the way a fold with the slot rule of
// docs/redaction-design.md §4 does: superseded slot members are dropped,
// then the real fold runs over what is left. The slot rule lives in
// internal/fold (U1, built in parallel); until this package can call it,
// the test drops superseded files itself, using splitLive, and folds the
// rest with fold.Fold and the real dispatch.
func redFoldLive(t *testing.T, stateRoot string) map[string]*fold.IssueState {
	t.Helper()
	slots := map[slotKey][]member{}
	for _, rel := range redFiles(t, stateRoot, "ops") {
		if !strings.HasSuffix(rel, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(stateRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		m, err := parseMember(rel, b)
		if err != nil {
			t.Fatal(err)
		}
		slots[keyOf(m.env)] = append(slots[keyOf(m.env)], m)
	}
	tmp := t.TempDir()
	for _, ms := range slots {
		live, _, _ := splitLive(ms)
		for _, m := range live {
			redWriteAt(t, tmp, filepath.Join(tmp, filepath.FromSlash(m.rel)), m.body)
		}
	}
	res, err := fold.Fold(filepath.Join(tmp, "ops"), fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return res.Issues
}

// redDir is path.Dir for a slash path.
func redDir(rel string) string { return path.Dir(rel) }
