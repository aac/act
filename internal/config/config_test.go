package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutPaths(t *testing.T) {
	repo := "/tmp/myrepo"
	l := Layout(repo)

	cases := map[string]string{
		"Root":           filepath.Join(repo, ".act"),
		"Ops":            filepath.Join(repo, ".act", "ops"),
		"Snapshots":      filepath.Join(repo, ".act", "snapshots"),
		"FoldCheckpoint": filepath.Join(repo, ".act", "fold-checkpoint.json"),
		"IndexDB":        filepath.Join(repo, ".act", "index.db"),
		"Hooks":          filepath.Join(repo, ".act", "hooks"),
		"Imports":        filepath.Join(repo, ".act", "imports"),
		"ConfigJSON":     filepath.Join(repo, ".act", "config.json"),
		"CompactLock":    filepath.Join(repo, ".act", ".lock"),
	}
	got := map[string]string{
		"Root":           l.Root,
		"Ops":            l.Ops,
		"Snapshots":      l.Snapshots,
		"FoldCheckpoint": l.FoldCheckpoint,
		"IndexDB":        l.IndexDB,
		"Hooks":          l.Hooks,
		"Imports":        l.Imports,
		"ConfigJSON":     l.ConfigJSON,
		"CompactLock":    l.CompactLock,
	}
	for name, want := range cases {
		if got[name] != want {
			t.Errorf("Layout.%s = %q, want %q", name, got[name], want)
		}
	}
}

func TestComputeNodeID(t *testing.T) {
	a1 := ComputeNodeID("machine-abc", "alice@example.com")
	a2 := ComputeNodeID("machine-abc", "alice@example.com")
	if a1 != a2 {
		t.Errorf("ComputeNodeID not deterministic: %s vs %s", a1, a2)
	}
	if len(a1) != 8 {
		t.Errorf("ComputeNodeID length = %d, want 8", len(a1))
	}
	for _, c := range a1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("ComputeNodeID = %q contains non-hex char %q", a1, c)
		}
	}

	b := ComputeNodeID("machine-xyz", "alice@example.com")
	if b == a1 {
		t.Errorf("ComputeNodeID should differ on different machine-id")
	}
	c := ComputeNodeID("machine-abc", "bob@example.com")
	if c == a1 {
		t.Errorf("ComputeNodeID should differ on different email")
	}

	// Spec's ordering: machine-id || email. Verify concat order matters.
	d := ComputeNodeID("alice@example.com", "machine-abc")
	if d == a1 {
		t.Errorf("ComputeNodeID should depend on argument order")
	}
}

func TestInitDirsCreatesAll(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	for _, dir := range []string{paths.Root, paths.Ops, paths.Snapshots, paths.Hooks, paths.Imports} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Errorf("missing dir %s: %v", dir, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}

func TestInitDirsIdempotent(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("first InitDirs: %v", err)
	}
	// Drop a sentinel into ops; second call must not remove it.
	sentinel := filepath.Join(paths.Ops, "sentinel")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatalf("sentinel: %v", err)
	}
	if err := InitDirs(paths); err != nil {
		t.Fatalf("second InitDirs: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel removed by idempotent InitDirs: %v", err)
	}
}

func TestWriteReadConfigRoundTrip(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	want := Config{
		NodeID:    "7f3a91c2",
		CreatedAt: "2026-04-29T14:23:01.000Z",
		Version:   "0.1.0",
		LastHLC:   HLCState{Wall: 1714400000000, Logical: 7},
	}
	if err := WriteConfig(paths, want); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	got, err := ReadConfig(paths)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got  %#v\n want %#v", got, want)
	}
}

func TestWriteConfigDeterministicAndSortedKeys(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	c := Config{
		NodeID:    "abcdef01",
		CreatedAt: "2026-04-29T00:00:00.000Z",
		Version:   "0.1.0",
		LastHLC:   HLCState{Wall: 42, Logical: 0},
	}
	if err := WriteConfig(paths, c); err != nil {
		t.Fatalf("WriteConfig 1: %v", err)
	}
	first, err := os.ReadFile(paths.ConfigJSON)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if err := WriteConfig(paths, c); err != nil {
		t.Fatalf("WriteConfig 2: %v", err)
	}
	second, err := os.ReadFile(paths.ConfigJSON)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("non-deterministic output:\n first  %s\n second %s", first, second)
	}

	// Top-level keys must appear in lexicographic order:
	// created_at < last_hlc < node_id < version.
	s := string(first)
	idx := func(key string) int { return strings.Index(s, `"`+key+`"`) }
	keys := []string{"created_at", "last_hlc", "node_id", "version"}
	prev := -1
	for _, k := range keys {
		i := idx(k)
		if i < 0 {
			t.Fatalf("key %q missing in output: %s", k, s)
		}
		if i <= prev {
			t.Errorf("key %q at offset %d not after previous %d (output: %s)", k, i, prev, s)
		}
		prev = i
	}

	// Nested object keys (logical, wall) sorted as well.
	li := strings.Index(s, `"logical"`)
	wi := strings.Index(s, `"wall"`)
	if li < 0 || wi < 0 || li > wi {
		t.Errorf("nested HLC keys not sorted: %s", s)
	}
}

func TestWriteConfigNoTrailingNewline(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	c := Config{
		NodeID:    "00000000",
		CreatedAt: "2026-04-29T00:00:00.000Z",
		Version:   "0.1.0",
	}
	if err := WriteConfig(paths, c); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	data, err := os.ReadFile(paths.ConfigJSON)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("empty config file")
	}
	if data[len(data)-1] == '\n' {
		t.Errorf("config file ends with trailing newline; canonicaljson must not emit one")
	}
	if data[len(data)-1] != '}' {
		t.Errorf("config file should end with '}', got %q", data[len(data)-1])
	}
}

func TestWriteConfigOverwritesAtomically(t *testing.T) {
	repo := t.TempDir()
	paths := Layout(repo)
	if err := InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	c1 := Config{NodeID: "11111111", CreatedAt: "t1", Version: "0.1.0"}
	c2 := Config{NodeID: "22222222", CreatedAt: "t2", Version: "0.1.0"}
	if err := WriteConfig(paths, c1); err != nil {
		t.Fatalf("write c1: %v", err)
	}
	if err := WriteConfig(paths, c2); err != nil {
		t.Fatalf("write c2: %v", err)
	}
	got, err := ReadConfig(paths)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if got.NodeID != "22222222" {
		t.Errorf("expected overwrite to NodeID=22222222, got %s", got.NodeID)
	}
	// No leftover .tmp files in .act/.
	entries, err := os.ReadDir(paths.Root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
