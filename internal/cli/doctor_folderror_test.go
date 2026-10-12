package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/op"
)

// Regression cover for act-bd1d30. When the fold fails, `act doctor` used to
// report the failure under the name of a different check (`orphan-ops`) and
// skip every other check, including the ones that never read the fold.

// folderrSeedStore builds a store whose fold fails (an op file with a newer
// op_version) and in which the fold-free `time-travel` check also fires (one
// valid op file's mtime moved an hour off its filename timestamp). It returns
// the repo root and the base name of the back-dated op file.
func folderrSeedStore(t *testing.T) (root, driftedBase string) {
	t.Helper()
	root = makeCreateRepo(t)
	mustCreate(t, root, "A")

	opsDir := filepath.Join(root, ".act", "ops")
	var drifted string
	_ = filepath.WalkDir(opsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || drifted != "" || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		ts, _, _, perr := op.Parse(d.Name())
		if perr != nil {
			return nil
		}
		off := ts.Add(-time.Hour)
		if cerr := os.Chtimes(path, off, off); cerr != nil {
			t.Fatalf("chtimes %s: %v", path, cerr)
		}
		drifted = path
		return nil
	})
	if drifted == "" {
		t.Fatalf("no parseable op file under %s to back-date", opsDir)
	}

	seedCreateWithOpVersion(t, root, "act-eeee", 1700000040000, op.CurrentOpVersion+1, op.WriterVersion)
	return root, filepath.Base(drifted)
}

// folderrRun runs RunDoctor and returns the DoctorResult and exit code.
func folderrRun(t *testing.T, root string, opts DoctorOptions) (DoctorResult, int) {
	t.Helper()
	out, code := RunDoctor(root, opts)
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("doctor %+v: output type = %T (%+v), want DoctorResult", opts, out, out)
	}
	if res.Count != len(res.Findings) {
		t.Errorf("doctor %+v: count = %d, but %d findings", opts, res.Count, len(res.Findings))
	}
	return res, code
}

// TestDocClaim_Doctor_FoldErrorHasOwnCheckAndFoldFreeChecksStillRun asserts
// the spec's `fold-error` table row: plain `act doctor` on a store whose fold
// fails reports the failure as one `fold-error` finding (never `orphan-ops`),
// still runs the read-only checks that do not need the fold, and exits 1.
func TestDocClaim_Doctor_FoldErrorHasOwnCheckAndFoldFreeChecksStillRun(t *testing.T) {
	root, driftedBase := folderrSeedStore(t)

	// Path proof: time-travel fires on this store by itself, so its
	// presence in the plain run below comes from the check, not the seed.
	alone, _ := folderrRun(t, root, DoctorOptions{Check: "time-travel"})
	if len(foldfailFindingsFor(alone, "time-travel")) != 1 {
		t.Fatalf("seed: --check time-travel findings = %+v, want exactly one", alone.Findings)
	}

	res, code := folderrRun(t, root, DoctorOptions{NoFetch: true})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}

	if mis := foldfailFindingsFor(res, "orphan-ops"); len(mis) != 0 {
		t.Errorf("fold failure reported under orphan-ops: %+v", mis)
	}
	fe := foldfailFindingsFor(res, "fold-error")
	if len(fe) != 1 {
		t.Fatalf("%d fold-error findings, want 1; all findings: %+v", len(fe), res.Findings)
	}
	if fe[0].Severity != "error" || !strings.HasPrefix(fe[0].Message, "fold error: ") {
		t.Errorf("fold-error finding = %+v, want severity error and a message starting \"fold error: \"", fe[0])
	}
	if res.Findings[0].Check != "fold-error" {
		t.Errorf("first finding = %+v, want the fold-error finding first", res.Findings[0])
	}

	tt := foldfailFindingsFor(res, "time-travel")
	if len(tt) != 1 || !strings.Contains(tt[0].Message, driftedBase) {
		t.Errorf("time-travel findings = %+v, want one naming %s; all findings: %+v", tt, driftedBase, res.Findings)
	}
	if len(foldfailFindingsFor(res, "unknown-op-version")) != 1 {
		t.Errorf("want one unknown-op-version finding; all findings: %+v", res.Findings)
	}

	// Nothing that needs the fold, touches the index, or fetches may run.
	allowed := map[string]bool{
		"fold-error": true, "stale-git-lock": true, "time-travel": true,
		"unknown-op-version": true, "gitignore-effective": true,
	}
	for _, f := range res.Findings {
		if !allowed[f.Check] {
			t.Errorf("finding from a check that must not run after a fold failure: %+v", f)
		}
	}
}

// TestDoctor_FoldErrorCheckSelection pins what `--check` does around a fold
// failure: `--check fold-error` is a valid name that reports only the fold
// failure, and a single fold-needing check answers with the same finding.
func TestDoctor_FoldErrorCheckSelection(t *testing.T) {
	root, _ := folderrSeedStore(t)
	for _, check := range []string{"fold-error", "orphan-ops", "cycle"} {
		res, code := folderrRun(t, root, DoctorOptions{Check: check})
		if code != 1 || len(res.Findings) != 1 || res.Findings[0].Check != "fold-error" {
			t.Errorf("--check %s: exit = %d, findings = %+v; want exit 1 and exactly one fold-error finding",
				check, code, res.Findings)
		}
	}

	// On a store that folds, --check fold-error is clean.
	clean := makeCreateRepo(t)
	mustCreate(t, clean, "A")
	res, code := folderrRun(t, clean, DoctorOptions{Check: "fold-error"})
	if code != 0 || len(res.Findings) != 0 {
		t.Errorf("--check fold-error on a readable store: exit = %d, findings = %+v; want 0 and none", code, res.Findings)
	}
}

// TestDoctor_FoldErrorAppliesNoFixesAndHonoursStrict: with the fold failed,
// --fix and --fix-index leave the index file alone (the index checks do not
// run), and --strict still promotes the surviving warn findings.
func TestDoctor_FoldErrorAppliesNoFixesAndHonoursStrict(t *testing.T) {
	root, _ := folderrSeedStore(t)
	indexPath := filepath.Join(root, ".act", "index.db")
	// A garbage index is what index-malformed + --fix-index would move aside.
	if err := os.WriteFile(indexPath, []byte("not a sqlite file"), 0o644); err != nil {
		t.Fatalf("write %s: %v", indexPath, err)
	}

	res, code := folderrRun(t, root, DoctorOptions{NoFetch: true, Fix: true, FixIndex: true, Strict: true})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	body, err := os.ReadFile(indexPath)
	if err != nil || string(body) != "not a sqlite file" {
		t.Errorf("index.db after --fix --fix-index on a failed fold: %q, err %v; want it untouched", body, err)
	}
	backups, _ := filepath.Glob(indexPath + ".malformed-*")
	if len(backups) != 0 {
		t.Errorf("--fix-index ran despite the fold failure: %v", backups)
	}
	for _, f := range res.Findings {
		if strings.HasPrefix(f.Check, "index-") {
			t.Errorf("index check ran after a fold failure: %+v", f)
		}
		if f.Severity != "error" {
			t.Errorf("--strict left a non-error finding: %+v", f)
		}
	}
	if len(foldfailFindingsFor(res, "time-travel")) != 1 {
		t.Errorf("want the time-travel finding under --strict too; findings: %+v", res.Findings)
	}
}
