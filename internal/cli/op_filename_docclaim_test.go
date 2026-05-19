package cli

// Doc-claim test for the NTFS-safe op-filename format (act-2f3d).
//
// The spec sections (docs/spec-v2.md, docs/spec-section-data-model.md,
// docs/spec.md) document the on-disk filename layout as
// `YYYY-MM-DDTHH-MM-SS.sssZ-<hash>-<op_type>.json`. The user-visible
// claim is that the time-component separators are `-`, not `:`, so the
// tree is checkoutable on Windows (NTFS reserves `:` in paths).
//
// This test asserts both halves the doc-claim discipline requires:
//
//   1. The format string is present in the spec (caught by the
//      docs_sweep registry entry; this test makes the assertion
//      explicit so an orphaned doc edit fails here, not just at the
//      registry level).
//
//   2. The act-writer surface — internal/op.Filename, which every
//      writer eventually funnels through — produces no-colon
//      filenames at the user-visible boundary (the bytes that
//      become the filename on disk).
//
// What this catches: a future writer change that re-introduces ':'
// (e.g. by reverting the isoLayout constant or by minting filenames
// from a different layout string).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

func TestDocClaim_OpFilename_NoColon(t *testing.T) {
	// (1) Spec surface: the format string is present in the on-disk
	// spec. The sweep registry pins this with the same literal
	// substring; the assertion here is explicit so a doc-revert
	// surfaces a meaningful failure (rather than only the generic
	// sweep diagnostic).
	root := repoRootForDocClaim(t)
	for _, rel := range []string{"docs/spec-v2.md", "docs/spec-section-data-model.md", "docs/spec.md"} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(body)
		if !strings.Contains(text, "`YYYY-MM-DDTHH-MM-SS.sssZ`") {
			t.Errorf("%s: missing NTFS-safe op-filename format string `YYYY-MM-DDTHH-MM-SS.sssZ`", rel)
		}
		if !strings.Contains(text, "act-2f3d") {
			t.Errorf("%s: NTFS-safe format claim should cite act-2f3d for traceability", rel)
		}
	}

	// (2) Writer surface: building an op filename for a representative
	// timestamp produces no ':' in the resulting basename. We can't
	// construct an op.Envelope directly from outside the package (its
	// hash inputs are unexported), so we use the same writer the rest
	// of the CLI uses: build a minimal valid envelope via the JSON
	// shape and round-trip through op.Filename.
	//
	// Approach: use a fixed wall, walk every ValidOpType, and check
	// each rendered basename. The hash component is opaque hex from
	// the envelope, so the colon (if it appeared) could only come
	// from the timestamp segment.
	wall := time.Date(2026, 4, 15, 12, 34, 56, 789_000_000, time.UTC).UnixMilli()
	for opType := range op.ValidOpTypes {
		e := opFilenameTestEnv(t, wall, opType)
		name := op.Filename(e)
		if strings.Contains(name, ":") {
			t.Errorf("op_type %q: Filename = %q contains ':' (NTFS-unsafe)", opType, name)
		}
	}
}

// opFilenameTestEnv builds a minimal-valid op.Envelope for the writer
// surface assertion. Mirrors the canonical envelope used by
// internal/op tests; here we live in package cli so we have to build
// it from public fields.
func opFilenameTestEnv(t *testing.T, wall int64, opType string) op.Envelope {
	t.Helper()
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       "act-abcd",
		Payload:       json.RawMessage(`{"title":"hello"}`),
		HLC: hlc.HLC{
			Wall:    wall,
			Logical: 0,
			NodeID:  "deadbeef",
		},
		NodeID: "deadbeef",
	}
}
