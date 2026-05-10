package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDescriptionFile_ReadsFile covers the happy path: a regular
// file under the cap. Round-trip the bytes verbatim and check that exit
// code 0 plus a nil error envelope come back.
func TestLoadDescriptionFile_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	body := "hello world\nmulti-line description\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, code, errEnv := loadDescriptionFile(path)
	if code != 0 {
		t.Fatalf("code = %d want 0; errEnv=%v", code, errEnv)
	}
	if errEnv != nil {
		t.Fatalf("errEnv = %v want nil", errEnv)
	}
	if got != body {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
}

// TestLoadDescriptionFile_FileMissing covers the "file does not exist"
// branch. Per the spec convention, this is exit 3 (resource not found),
// not exit 2 (bad flag).
func TestLoadDescriptionFile_FileMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-such-file.txt")

	_, code, errEnv := loadDescriptionFile(path)
	if code != 3 {
		t.Fatalf("code = %d want 3", code)
	}
	if errEnv == nil || errEnv["error"] != "file_not_found" {
		t.Fatalf("errEnv = %v want error=file_not_found", errEnv)
	}
}

// TestLoadDescriptionFile_AtCap reads a file exactly at the 16384-byte
// limit. This is the boundary case that proves we don't off-by-one.
func TestLoadDescriptionFile_AtCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	body := strings.Repeat("a", maxDescriptionBytes)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, code, errEnv := loadDescriptionFile(path)
	if code != 0 {
		t.Fatalf("code = %d want 0; errEnv=%v", code, errEnv)
	}
	if len(got) != maxDescriptionBytes {
		t.Fatalf("len = %d want %d", len(got), maxDescriptionBytes)
	}
}

// TestLoadDescriptionFile_OverCap reads a file one byte over the cap;
// must return exit 2 with a "bad_flag" envelope and a clear message
// referencing the limit.
func TestLoadDescriptionFile_OverCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	body := strings.Repeat("a", maxDescriptionBytes+1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, code, errEnv := loadDescriptionFile(path)
	if code != 2 {
		t.Fatalf("code = %d want 2", code)
	}
	if errEnv == nil || errEnv["error"] != "bad_flag" {
		t.Fatalf("errEnv = %v want error=bad_flag", errEnv)
	}
	msg, _ := errEnv["message"].(string)
	if !strings.Contains(msg, "16384") {
		t.Fatalf("message %q does not reference 16384-char limit", msg)
	}
}

// TestLoadDescriptionFile_StdinSentinel verifies that "-" reads stdin
// rather than opening a literal file named "-". We replace os.Stdin
// with a pipe so the test is hermetic.
func TestLoadDescriptionFile_StdinSentinel(t *testing.T) {
	body := "stdin-payload\n"

	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write([]byte(body))
		_ = w.Close()
	}()

	got, code, errEnv := loadDescriptionFile("-")
	if code != 0 {
		t.Fatalf("code = %d want 0; errEnv=%v", code, errEnv)
	}
	if got != body {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
}

// TestLoadDescriptionFile_StdinOverCap verifies that the cap is also
// applied when reading from stdin. Push >16384 bytes through the pipe;
// expect exit 2 with bad_flag.
func TestLoadDescriptionFile_StdinOverCap(t *testing.T) {
	oldStdin := os.Stdin
	t.Cleanup(func() { os.Stdin = oldStdin })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = r
	go func() {
		_, _ = w.Write([]byte(strings.Repeat("b", maxDescriptionBytes+10)))
		_ = w.Close()
	}()

	_, code, errEnv := loadDescriptionFile("-")
	if code != 2 {
		t.Fatalf("code = %d want 2", code)
	}
	if errEnv == nil || errEnv["error"] != "bad_flag" {
		t.Fatalf("errEnv = %v want error=bad_flag", errEnv)
	}
}

// TestLoadDescriptionFile_EmptyFile is intentionally permitted: an
// empty payload is a valid description per the schema's 0..16384 range.
func TestLoadDescriptionFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, code, errEnv := loadDescriptionFile(path)
	if code != 0 {
		t.Fatalf("code = %d want 0; errEnv=%v", code, errEnv)
	}
	if got != "" {
		t.Fatalf("body = %q want empty", got)
	}
}

// TestLoadDescriptionFile_UTF8 verifies that multi-byte UTF-8 is
// preserved verbatim. The byte-based cap means a file with N runes ≤ N
// bytes always passes; this case uses a small payload of multi-byte
// characters as a smoke check.
func TestLoadDescriptionFile_UTF8(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "desc.txt")
	body := "héllo — wörld 🚀"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, code, _ := loadDescriptionFile(path)
	if code != 0 {
		t.Fatalf("code = %d want 0", code)
	}
	if got != body {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
}
