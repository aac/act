package op

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// aprilEnv is a goodEnvelope with the HLC wall pinned to 2026-04-15 12:34:56.789 UTC.
func aprilEnv(t *testing.T) Envelope {
	t.Helper()
	e := goodEnvelope()
	wall := time.Date(2026, 4, 15, 12, 34, 56, 789_000_000, time.UTC).UnixMilli()
	e.HLC.Wall = wall
	return e
}

func TestShardDir_Apr2026(t *testing.T) {
	root := "/tmp/ops-root"
	wall := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	got := ShardDir(root, "act-abcd", wall)
	want := filepath.Join(root, "act-abcd", "2026-04") + string(filepath.Separator)
	if got != want {
		t.Fatalf("ShardDir = %q, want %q", got, want)
	}
}

func TestShardDir_MonthBoundary(t *testing.T) {
	root := "/tmp/ops-root"
	// 2026-12-31 23:59:59.999 UTC and 2027-01-01 00:00:00.000 UTC.
	dec := time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC).UnixMilli()
	jan := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := ShardDir(root, "act-abcd", dec); !strings.Contains(got, "2026-12") {
		t.Fatalf("dec shard = %q, want 2026-12", got)
	}
	if got := ShardDir(root, "act-abcd", jan); !strings.Contains(got, "2027-01") {
		t.Fatalf("jan shard = %q, want 2027-01", got)
	}
}

func TestFilename_Pattern(t *testing.T) {
	e := aprilEnv(t)
	got := Filename(e)
	full, err := e.FullHash()
	if err != nil {
		t.Fatal(err)
	}
	// Time component uses '-' (not ':') per act-2f3d (NTFS-safe).
	want := "2026-04-15T12-34-56.789Z-" + full[:8] + "-create.json"
	if got != want {
		t.Fatalf("Filename = %q, want %q", got, want)
	}
}

func TestFilename_AllOpTypes(t *testing.T) {
	for opType := range ValidOpTypes {
		e := aprilEnv(t)
		e.OpType = opType
		got := Filename(e)
		if !strings.HasSuffix(got, "-"+opType+".json") {
			t.Errorf("op_type %q: Filename = %q, missing op_type suffix", opType, got)
		}
	}
}

func TestParse_Roundtrip(t *testing.T) {
	e := aprilEnv(t)
	name := Filename(e)
	ts, hashHex, opType, err := Parse(name)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := ts.UnixMilli(); got != e.HLC.Wall {
		t.Fatalf("Parse timestamp = %d ms, want %d ms", got, e.HLC.Wall)
	}
	if len(hashHex) != 8 {
		t.Fatalf("Parse hash len = %d, want 8", len(hashHex))
	}
	full, _ := e.FullHash()
	if hashHex != full[:8] {
		t.Fatalf("Parse hash = %q, want %q", hashHex, full[:8])
	}
	if opType != e.OpType {
		t.Fatalf("Parse op_type = %q, want %q", opType, e.OpType)
	}
}

func TestParse_AcceptsExtendedHashLengths(t *testing.T) {
	e := aprilEnv(t)
	for _, n := range []int{8, 12, 16} {
		name, err := filenameWithLen(e, n)
		if err != nil {
			t.Fatalf("filenameWithLen(%d): %v", n, err)
		}
		_, h, _, perr := Parse(name)
		if perr != nil {
			t.Fatalf("Parse(%s): %v", name, perr)
		}
		if len(h) != n {
			t.Fatalf("Parse hash len = %d, want %d", len(h), n)
		}
	}
}

func TestParse_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"not-an-op.json",
		"2026-04-15T12-34-56Z-deadbeef-create.json",                 // missing millis (new form)
		"2026-04-15T12:34:56Z-deadbeef-create.json",                 // missing millis (legacy form)
		"2026-04-15T12-34-56.789Z-XYZ-create.json",                  // non-hex
		"2026-04-15T12-34-56.789Z-deadbeef-bogus.json",              // unknown op_type
		"2026-04-15T12-34-56.789Z-dead-create.json",                 // hash too short
		"2026-04-15T12-34-56.789Z-deadbeefcafebabedead-create.json", // hash 20 chars
	}
	for _, c := range cases {
		if _, _, _, err := Parse(c); err == nil {
			t.Errorf("Parse(%q) = nil error, want error", c)
		}
	}
}

// nopLock returns an fsLock callback that records call counts.
func nopLock(acq, rel *int) func() (func(), error) {
	return func() (func(), error) {
		*acq++
		return func() { *rel++ }, nil
	}
}

func TestProbeAndWrite_LengthEight(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	body := []byte(`{"hello":"world"}`)
	var acq, rel int
	path, n, err := ProbeAndWrite(tmp, e, body, nopLock(&acq, &rel))
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	if n != 8 {
		t.Fatalf("hash len = %d, want 8", n)
	}
	if acq != 1 || rel != 1 {
		t.Fatalf("lock acq=%d rel=%d, want 1/1", acq, rel)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("file body = %q, want %q", got, body)
	}
	// Verify the path matches Filename(env) within the expected shard.
	wantName := Filename(e)
	if filepath.Base(path) != wantName {
		t.Fatalf("path basename = %q, want %q", filepath.Base(path), wantName)
	}
	// No leftover temp files.
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestProbeAndWrite_FallsBackToTwelve(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	body := []byte(`{"hello":"world"}`)

	// Pre-create the 8-hex collision file.
	shard := ShardDir(tmp, e.IssueID, e.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	name8, _ := filenameWithLen(e, 8)
	if err := os.WriteFile(filepath.Join(shard, name8), []byte("collision"), 0o644); err != nil {
		t.Fatal(err)
	}

	var acq, rel int
	path, n, err := ProbeAndWrite(tmp, e, body, nopLock(&acq, &rel))
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	if n != 12 {
		t.Fatalf("hash len = %d, want 12", n)
	}
	want12, _ := filenameWithLen(e, 12)
	if filepath.Base(path) != want12 {
		t.Fatalf("path basename = %q, want %q", filepath.Base(path), want12)
	}
	if got, _ := os.ReadFile(path); string(got) != string(body) {
		t.Fatalf("file body mismatch")
	}
	assertNoTempFiles(t, shard)
}

func TestProbeAndWrite_FallsBackToSixteen(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	body := []byte(`{"hello":"world"}`)

	shard := ShardDir(tmp, e.IssueID, e.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{8, 12} {
		name, _ := filenameWithLen(e, n)
		if err := os.WriteFile(filepath.Join(shard, name), []byte("c"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var acq, rel int
	path, n, err := ProbeAndWrite(tmp, e, body, nopLock(&acq, &rel))
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	if n != 16 {
		t.Fatalf("hash len = %d, want 16", n)
	}
	want16, _ := filenameWithLen(e, 16)
	if filepath.Base(path) != want16 {
		t.Fatalf("path basename = %q, want %q", filepath.Base(path), want16)
	}
	assertNoTempFiles(t, shard)
}

func TestProbeAndWrite_AllCollideErrors(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	body := []byte(`{"hello":"world"}`)

	shard := ShardDir(tmp, e.IssueID, e.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{8, 12, 16} {
		name, _ := filenameWithLen(e, n)
		if err := os.WriteFile(filepath.Join(shard, name), []byte("c"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var acq, rel int
	_, _, err := ProbeAndWrite(tmp, e, body, nopLock(&acq, &rel))
	if !errors.Is(err, ErrOpHashCollision) {
		t.Fatalf("err = %v, want ErrOpHashCollision", err)
	}
	if rel != 1 {
		t.Fatalf("release count = %d, want 1", rel)
	}
}

func TestProbeAndWrite_AtomicNoTempFile(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	body := []byte(`{"k":"v"}`)
	var acq, rel int
	path, _, err := ProbeAndWrite(tmp, e, body, nopLock(&acq, &rel))
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("body mismatch")
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

func TestProbeAndWrite_CreatesShardDir(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	shard := ShardDir(tmp, e.IssueID, e.HLC.Wall)
	if _, err := os.Stat(shard); !os.IsNotExist(err) {
		t.Fatalf("shard already exists before write: %v", err)
	}
	var acq, rel int
	if _, _, err := ProbeAndWrite(tmp, e, []byte("{}"), nopLock(&acq, &rel)); err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	if info, err := os.Stat(shard); err != nil || !info.IsDir() {
		t.Fatalf("shard dir not created: %v", err)
	}
}

func TestProbeAndWrite_LockErrorPropagates(t *testing.T) {
	tmp := t.TempDir()
	e := aprilEnv(t)
	wantErr := errors.New("boom")
	_, _, err := ProbeAndWrite(tmp, e, []byte("{}"), func() (func(), error) {
		return nil, wantErr
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want wrap of boom", err)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".op-") {
			t.Errorf("leftover temp file: %s", name)
		}
	}
}
