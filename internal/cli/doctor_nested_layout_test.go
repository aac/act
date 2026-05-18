package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCheckNestedLayout_PassesOnFreshInit verifies the dogfood-gate check
// reports zero findings on a freshly-init'd repo. RunInit produces the
// nested-repo layout from day one (Phase 1 design), so a clean init must
// pass nested-layout out of the box.
func TestCheckNestedLayout_PassesOnFreshInit(t *testing.T) {
	root := makeRepo(t)
	_, code := RunInit(root, false, "m", "u@e",
		fakeNow(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)))
	if code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	out, code := RunDoctor(root, DoctorOptions{Check: "nested-layout"})
	if code != 0 {
		t.Fatalf("doctor exit = %d; out=%+v", code, out)
	}
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output = %T, want DoctorResult", out)
	}
	if len(res.Findings) != 0 {
		t.Errorf("nested-layout findings on freshly-init'd repo: %+v", res.Findings)
	}
}

// TestCheckNestedLayout_FailsOnLegacyRepo verifies the check reports the
// four expected findings on an un-migrated legacy fixture: no nested
// .git, no gitignore entry, tracked .act/ paths, no host hook.
//
// This is the "fails clearly on an un-migrated fixture" path called out
// in the verify checklist.
func TestCheckNestedLayout_FailsOnLegacyRepo(t *testing.T) {
	root := makeLegacyActRepo(t)

	out, code := RunDoctor(root, DoctorOptions{Check: "nested-layout"})
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1 (error findings); out=%+v", code, out)
	}
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output = %T, want DoctorResult", out)
	}
	// Expect exactly four findings on a fully-legacy fixture: nested
	// .git missing, gitignore line missing, .act/ tracked, hook missing.
	if len(res.Findings) != 4 {
		t.Errorf("findings count = %d, want 4; got %+v", len(res.Findings), res.Findings)
	}
	for _, f := range res.Findings {
		if f.Check != "nested-layout" {
			t.Errorf("finding check = %q, want nested-layout", f.Check)
		}
		if f.Severity != "error" {
			t.Errorf("finding severity = %q, want error", f.Severity)
		}
		if !strings.Contains(f.Message, "act migrate-to-nested") {
			t.Errorf("finding message missing remedy: %q", f.Message)
		}
	}
}

// TestCheckNestedLayout_PassesAfterMigrate verifies the dogfood-gate
// check passes on a legacy repo after the migration has run.
func TestCheckNestedLayout_PassesAfterMigrate(t *testing.T) {
	root := makeLegacyActRepo(t)
	_, mcode := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if mcode != 0 {
		t.Fatalf("migrate exit = %d", mcode)
	}

	out, code := RunDoctor(root, DoctorOptions{Check: "nested-layout"})
	if code != 0 {
		t.Fatalf("doctor exit = %d after migrate; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) != 0 {
		t.Errorf("nested-layout findings after migrate: %+v", res.Findings)
	}
}

// TestCheckNestedLayout_PartialMigratedRepo verifies the check still
// flags specific anomalies when only some of the four invariants hold.
// We test the case where the nested .git exists but the gitignore entry
// is missing (e.g. operator removed it post-migration by accident).
func TestCheckNestedLayout_PartialMigratedRepo(t *testing.T) {
	root := makeRepo(t)
	_, code := RunInit(root, false, "m", "u@e",
		fakeNow(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)))
	if code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	// Stomp the gitignore — simulating an operator who removed the entry.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# unrelated\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	out, dcode := RunDoctor(root, DoctorOptions{Check: "nested-layout"})
	if dcode != 1 {
		t.Fatalf("doctor exit = %d, want 1; out=%+v", dcode, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) == 0 {
		t.Fatalf("expected at least one finding; got none")
	}
	// One of the findings should call out the missing gitignore line.
	var sawGitignore bool
	for _, f := range res.Findings {
		if strings.Contains(f.Message, ".gitignore") {
			sawGitignore = true
			break
		}
	}
	if !sawGitignore {
		t.Errorf("findings missing the .gitignore-line message: %+v", res.Findings)
	}
}

// TestCheckNestedLayout_RunsByDefault verifies that nested-layout is
// part of the default doctor check set (no --check flag, all checks run).
// This is the "runs by default" half of the dogfood gate's CI contract.
func TestCheckNestedLayout_RunsByDefault(t *testing.T) {
	root := makeLegacyActRepo(t)

	out, code := RunDoctor(root, DoctorOptions{})
	if code != 1 {
		t.Fatalf("doctor exit = %d on legacy repo, want 1", code)
	}
	res := out.(DoctorResult)
	// At least one nested-layout finding should appear in the default run.
	var sawNested bool
	for _, f := range res.Findings {
		if f.Check == "nested-layout" {
			sawNested = true
			break
		}
	}
	if !sawNested {
		t.Errorf("default doctor run did not include nested-layout findings: %+v", res.Findings)
	}
}
