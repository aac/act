package gitops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// makeGitDir creates a directory with a `.git` subdirectory containing a
// HEAD file. This is enough for FindHostRepoRoot's os.Stat-based detection
// without paying the cost of a real `git init`. Returns the directory path.
func makeGitDir(t *testing.T, dir string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
}

// realPath collapses macOS /var → /private/var so comparisons against
// filepath.Abs results survive the symlink hop on TempDir paths.
func realPath(t *testing.T, p string) string {
	t.Helper()
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("evalsymlinks %s: %v", p, err)
	}
	return rp
}

// TestFindHostRepoRoot_HostWithAct covers the standard Phase 1 layout:
// a host repo with a nested .act/.git/ inside it. The resolver must return
// the host repo root from every position inside that tree, including from
// within the nested .act/ and .act/.git/ themselves.
func TestFindHostRepoRoot_HostWithAct(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "project")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	makeGitDir(t, host)
	// Nested act repo at host/.act/.
	actDir := filepath.Join(host, ".act")
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeGitDir(t, actDir)
	// A host subdir, so we can test "host repo subdir" launch.
	subDir := filepath.Join(host, "internal", "cli")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wantHost := realPath(t, host)
	cases := []struct {
		name string
		from string
	}{
		{"from host root", host},
		{"from host subdir", subDir},
		{"from nested .act/", actDir},
		{"from nested .act/.git/", filepath.Join(actDir, ".git")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FindHostRepoRoot(tc.from)
			if err != nil {
				t.Fatalf("FindHostRepoRoot(%s): %v", tc.from, err)
			}
			gotReal := realPath(t, got)
			if gotReal != wantHost {
				t.Fatalf("FindHostRepoRoot(%s) = %s, want %s", tc.from, gotReal, wantHost)
			}
		})
	}
}

// TestFindHostRepoRoot_HostWithoutAct covers a host repo that hasn't run
// `act init` yet. FindHostRepoRoot still finds the host root; the absence of
// .act/ surfaces from FindActStatePath, not from this function.
func TestFindHostRepoRoot_HostWithoutAct(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "project")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	makeGitDir(t, host)
	subDir := filepath.Join(host, "src")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wantHost := realPath(t, host)
	for _, from := range []string{host, subDir} {
		got, err := FindHostRepoRoot(from)
		if err != nil {
			t.Fatalf("FindHostRepoRoot(%s): %v", from, err)
		}
		if realPath(t, got) != wantHost {
			t.Fatalf("FindHostRepoRoot(%s) = %s, want %s", from, got, wantHost)
		}
	}
}

// TestFindHostRepoRoot_NoHostRepo covers the case where the start directory
// is not inside any git repo at all. The resolver must return ErrNoHostRepo,
// not silently succeed with the filesystem root.
func TestFindHostRepoRoot_NoHostRepo(t *testing.T) {
	// t.TempDir() lives inside the testing harness's tempdir, which on most
	// systems is not itself inside a git repo. Guard by creating an explicit
	// non-repo directory and using it.
	root := t.TempDir()
	leaf := filepath.Join(root, "not", "a", "repo")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}

	// If a parent of t.TempDir() happens to be a git repo (unlikely but
	// possible), FindHostRepoRoot will succeed and find that. Skip in that
	// case rather than fail — the test is about the negative path.
	if _, err := FindHostRepoRoot(filepath.Dir(root)); err == nil {
		t.Skip("tempdir lives inside a git repo; can't test no-host-repo case")
	}

	_, err := FindHostRepoRoot(leaf)
	if !errors.Is(err, ErrNoHostRepo) {
		t.Fatalf("FindHostRepoRoot(%s) = %v, want ErrNoHostRepo", leaf, err)
	}
}

// TestFindHostRepoRoot_StandaloneActUnsupported covers the operator-decided-
// scope case explicitly out-of-scope for Phase 1: act invoked from inside a
// .act/ whose parent is not itself a git repo. The walk skips the nested
// .git, then exhausts without finding a host — the standalone signal
// distinguishes this from the "never saw any .git" case.
func TestFindHostRepoRoot_StandaloneActUnsupported(t *testing.T) {
	// Same parent-not-a-repo guard as above.
	root := t.TempDir()
	if _, err := FindHostRepoRoot(filepath.Dir(root)); err == nil {
		t.Skip("tempdir lives inside a git repo; can't test standalone case")
	}
	actDir := filepath.Join(root, "standalone", ".act")
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeGitDir(t, actDir)

	for _, from := range []string{actDir, filepath.Join(actDir, ".git")} {
		_, err := FindHostRepoRoot(from)
		if !errors.Is(err, ErrStandaloneActUnsupported) {
			t.Fatalf("FindHostRepoRoot(%s) = %v, want ErrStandaloneActUnsupported", from, err)
		}
	}
}

// TestFindHostRepoRoot_NonexistentStart confirms a clear error when the
// start path doesn't exist, rather than the walk silently climbing to a
// filesystem ancestor.
func TestFindHostRepoRoot_NonexistentStart(t *testing.T) {
	_, err := FindHostRepoRoot(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error for nonexistent start, got nil")
	}
	if errors.Is(err, ErrNoHostRepo) {
		t.Fatalf("expected stat error, got ErrNoHostRepo")
	}
}

// TestFindHostRepoRoot_EmptyStart confirms the empty-input guard.
func TestFindHostRepoRoot_EmptyStart(t *testing.T) {
	_, err := FindHostRepoRoot("")
	if err == nil {
		t.Fatal("expected error for empty start, got nil")
	}
}

// TestFindActStatePath_Present returns the .act path when the directory
// exists at host root.
func TestFindActStatePath_Present(t *testing.T) {
	host := t.TempDir()
	actDir := filepath.Join(host, ".act")
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindActStatePath(host)
	if err != nil {
		t.Fatalf("FindActStatePath(%s): %v", host, err)
	}
	if realPath(t, got) != realPath(t, actDir) {
		t.Fatalf("FindActStatePath(%s) = %s, want %s", host, got, actDir)
	}
}

// TestFindActStatePath_Absent returns ErrNoActState when the .act directory
// is not present.
func TestFindActStatePath_Absent(t *testing.T) {
	host := t.TempDir()
	_, err := FindActStatePath(host)
	if !errors.Is(err, ErrNoActState) {
		t.Fatalf("FindActStatePath(%s) = %v, want ErrNoActState", host, err)
	}
}

// TestFindActStatePath_NotADirectory returns a clear error (not ErrNoActState)
// when .act exists but is a regular file. Catches the case where someone
// accidentally created a file named .act at the host root.
func TestFindActStatePath_NotADirectory(t *testing.T) {
	host := t.TempDir()
	actPath := filepath.Join(host, ".act")
	if err := os.WriteFile(actPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := FindActStatePath(host)
	if err == nil {
		t.Fatal("expected error for non-directory .act, got nil")
	}
	if errors.Is(err, ErrNoActState) {
		t.Fatalf("got ErrNoActState, want a non-sentinel error for non-directory")
	}
}

// TestFindActStatePath_EmptyHost confirms the empty-input guard.
func TestFindActStatePath_EmptyHost(t *testing.T) {
	_, err := FindActStatePath("")
	if err == nil {
		t.Fatal("expected error for empty host repo root, got nil")
	}
}
