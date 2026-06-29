package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunVersion_NoCheckRepo(t *testing.T) {
	out, code := RunVersion(false, "")
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type: got %T, want map[string]any", out)
	}
	if got, want := m["binary_version"], BinaryVersion; got != want {
		t.Errorf("binary_version: got %v, want %v", got, want)
	}
	if got, want := m["writer_version"], WriterVersion; got != want {
		t.Errorf("writer_version: got %v, want %v", got, want)
	}
	if got, want := m["go_version"], runtime.Version(); got != want {
		t.Errorf("go_version: got %v, want %v", got, want)
	}
	if got, want := m["platform"], runtime.GOOS+"/"+runtime.GOARCH; got != want {
		t.Errorf("platform: got %v, want %v", got, want)
	}
	if _, present := m["max_op_version"]; present {
		t.Errorf("max_op_version should be absent without --check-repo")
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.0", "0.1.0", 0},
		{"0.2.0", "0.1.0", 1},
		{"0.1.0", "0.2.0", -1},
		{"0.1.10", "0.1.9", 1},
		{"0.1.9", "0.1.10", -1},
		{"1.0.0", "0.9.99", 1},
		{"0.9.99", "1.0.0", -1},
		{"0.1.0", "0.1.0+abc", 0},
		{"0.1.0-rc1", "0.1.0", 0},
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		if got != c.want {
			t.Errorf("compareSemver(%q, %q): got %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestRunVersion_CheckRepoMissingAct(t *testing.T) {
	dir := t.TempDir()
	out, code := RunVersion(true, dir)
	if code != 3 {
		t.Fatalf("exit code: got %d, want 3 (out=%v)", code, out)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type: got %T", out)
	}
	if m["error"] == nil {
		t.Errorf("expected error field for missing .act/")
	}
}

func writeFakeOp(t *testing.T, dir, name, writerVersion string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"op_version":1,"schema_version":1,"writer_version":"` + writerVersion + `","op_type":"create","issue_id":"act-0000","payload":{},"hlc":{"wall":1,"logical":0},"node_id":"deadbeef"}`
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunVersion_CheckRepoSkew(t *testing.T) {
	root := t.TempDir()
	opsDir := filepath.Join(root, ".act", "ops", "ab")
	writeFakeOp(t, opsDir, "01.json", "0.1.0")
	writeFakeOp(t, opsDir, "02.json", "0.2.0")

	out, code := RunVersion(true, root)
	if code != 4 {
		t.Fatalf("exit code: got %d, want 4 (out=%v)", code, out)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type: got %T", out)
	}
	if m["error"] != "version_skew" {
		t.Errorf("error: got %v, want version_skew", m["error"])
	}
	details, ok := m["details"].(map[string]any)
	if !ok {
		t.Fatalf("details: got %T", m["details"])
	}
	if details["max_op_version"] != "0.2.0" {
		t.Errorf("max_op_version: got %v, want 0.2.0", details["max_op_version"])
	}
	if details["binary_version"] != BinaryVersion {
		t.Errorf("binary_version: got %v, want %s", details["binary_version"], BinaryVersion)
	}
}

func TestRunVersion_CheckRepoAllEqual(t *testing.T) {
	root := t.TempDir()
	opsDir := filepath.Join(root, ".act", "ops", "ab")
	writeFakeOp(t, opsDir, "01.json", "0.1.0")
	writeFakeOp(t, opsDir, "02.json", "0.1.0")

	out, code := RunVersion(true, root)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0 (out=%v)", code, out)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type: got %T", out)
	}
	if m["max_op_version"] != "0.1.0" {
		t.Errorf("max_op_version: got %v, want 0.1.0", m["max_op_version"])
	}
	if m["binary_version"] != BinaryVersion {
		t.Errorf("binary_version: got %v, want %s", m["binary_version"], BinaryVersion)
	}
}

func TestRunVersion_CheckRepoEmptyOps(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".act", "ops"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := RunVersion(true, root)
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0 (out=%v)", code, out)
	}
	m := out.(map[string]any)
	if m["max_op_version"] != "" {
		t.Errorf("max_op_version: got %v, want empty", m["max_op_version"])
	}
}
