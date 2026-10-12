package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/op"
)

// Regression cover for act-f95d6e. An op file with a newer op_version fails
// envelope validation, so the fold aborts. Plain `act doctor` used to return
// the fold error as its only finding and never reach unknown-op-version, the
// one check that reads op_version from the raw JSON and names the file.

// foldfailFindingsFor returns the findings in res carrying the given check.
func foldfailFindingsFor(res DoctorResult, check string) []Finding {
	var out []Finding
	for _, f := range res.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// TestDocClaim_Doctor_PlainRunReportsUnknownOpVersionWhenFoldFails asserts
// the spec's edge-case bullet: `act doctor` with no --check, on a store
// holding an op file with a newer op_version, reports an unknown-op-version
// finding naming that file alongside the fold-error finding, and exits 1.
func TestDocClaim_Doctor_PlainRunReportsUnknownOpVersionWhenFoldFails(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	bogus := seedCreateWithOpVersion(t, root, "act-eeee", 1700000040000, op.CurrentOpVersion+1, op.WriterVersion)
	base := filepath.Base(bogus)

	out, code := RunDoctor(root, DoctorOptions{NoFetch: true})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("doctor output type = %T, want DoctorResult", out)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if res.Count != len(res.Findings) {
		t.Errorf("count = %d, but %d findings", res.Count, len(res.Findings))
	}

	unknown := foldfailFindingsFor(res, "unknown-op-version")
	if len(unknown) != 1 {
		t.Fatalf("plain doctor: %d unknown-op-version findings, want 1 naming %s; all findings: %+v",
			len(unknown), base, res.Findings)
	}
	if unknown[0].Severity != "error" || !strings.Contains(unknown[0].Message, base) {
		t.Errorf("unknown-op-version finding = %+v, want severity error and a message naming %s", unknown[0], base)
	}

	// The fold failure is still reported, under the label it has always had.
	var foldErr bool
	for _, f := range foldfailFindingsFor(res, "orphan-ops") {
		if strings.HasPrefix(f.Message, "fold error: ") {
			foldErr = true
		}
	}
	if !foldErr {
		t.Errorf("no fold-error finding in %+v; the fold failure must still be reported", res.Findings)
	}
}

// TestDoctor_FoldFailSingleFoldCheckUnchanged pins what the fix must not
// change: a --check that needs the fold still answers with the fold-error
// finding alone, and --check unknown-op-version still answers with its own
// finding alone.
func TestDoctor_FoldFailSingleFoldCheckUnchanged(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	seedCreateWithOpVersion(t, root, "act-eeee", 1700000040000, op.CurrentOpVersion+1, op.WriterVersion)

	for check, wantCheck := range map[string]string{
		"cycle":              "orphan-ops",
		"unknown-op-version": "unknown-op-version",
	} {
		out, code := RunDoctor(root, DoctorOptions{Check: check})
		res, ok := out.(DoctorResult)
		if !ok {
			t.Fatalf("--check %s: output type = %T, want DoctorResult", check, out)
		}
		if code != 1 || len(res.Findings) != 1 || res.Findings[0].Check != wantCheck {
			t.Errorf("--check %s: exit = %d, findings = %+v; want exit 1 and exactly one %s finding",
				check, code, res.Findings, wantCheck)
		}
	}
}
