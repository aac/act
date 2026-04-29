// Package importer implements the bootstrap JSONL importer described in
// spec-v2.md §3 (Errors, hooks, migration, bootstrap, compaction, tests).
//
// The importer reads a JSONL file (one op envelope per line), validates every
// line up front, and on success replays each op via the normal write path:
// fresh local HLC, local node_id, fresh act-style id derived from the
// create-op payload. Bootstrap ids referenced by later ops in the same input
// are rewritten to local ids using an in-process mapping table.
//
// The procedure is all-or-nothing: validation failures abort before any side
// effects; successful imports persist a single .act/imports/<iso-utc>.json
// mapping file and (unless --no-commit) a single git commit.
package importer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// gitOpsCommitter is the minimal git surface the importer needs: stage files
// (op files plus the mapping file) and create a single commit. *gitops.GitOps
// satisfies this interface; tests pass a fake.
type gitOpsCommitter interface {
	StageOpFile(path string) error
	Commit(message string) error
	Push() error
}

// Options captures the flags accepted by `act import`.
type Options struct {
	// JSONLPath is the absolute or relative path to the input JSONL file.
	JSONLPath string
	// AsJSON toggles JSON envelope output. Currently advisory: the importer
	// returns a structured Result regardless; the cmd-level wrapper renders
	// human vs JSON.
	AsJSON bool
	// NoCommit suppresses the auto-commit step.
	NoCommit bool
	// Push runs `git push` after the commit. Combining with NoCommit yields
	// an error.
	Push bool
}

// Result is the return shape of Run. Idempotent==true means a previous import
// already landed the same source bytes; OpsImported and IssuesCreated are 0
// in that case and MappingFile points to the existing mapping file.
type Result struct {
	OpsImported   int    `json:"ops_imported"`
	IssuesCreated int    `json:"issues_created"`
	MappingFile   string `json:"mapping_file"`
	Idempotent    bool   `json:"idempotent"`
	SourceSHA     string `json:"source_sha"`
}

// inputLine is the wire shape of each JSONL line in the input. The HLC and
// node_id fields are read but discarded on replay (the importer issues fresh
// local values).
type inputLine struct {
	OpVersion     int             `json:"op_version"`
	SchemaVersion int             `json:"schema_version"`
	OpType        string          `json:"op_type"`
	IssueID       string          `json:"issue_id"`
	Payload       json.RawMessage `json:"payload"`
	HLC           json.RawMessage `json:"hlc"`
	NodeID        string          `json:"node_id"`
}

// mappingFile is the on-disk shape of .act/imports/<iso-utc>.json.
type mappingFile struct {
	Source        string            `json:"source"`
	Mapping       map[string]string `json:"mapping"`
	ImportedAtHLC string            `json:"imported_at_hlc,omitempty"`
}

// Run is the importer entry point. It performs the four-step bootstrap
// procedure (validate → replay → mapping → commit) atomically.
func Run(repoRoot string, opts Options, gitops gitOpsCommitter) (Result, error) {
	if opts.NoCommit && opts.Push {
		return Result{}, fmt.Errorf("importer: --no-commit and --push are mutually exclusive")
	}
	if !opts.NoCommit && gitops == nil {
		return Result{}, fmt.Errorf("importer: gitops is required unless --no-commit is set")
	}

	// Step 0: hash the input file.
	raw, err := os.ReadFile(opts.JSONLPath)
	if err != nil {
		return Result{}, fmt.Errorf("importer: read %s: %w", opts.JSONLPath, err)
	}
	sum := sha256.Sum256(raw)
	sourceSHA := hex.EncodeToString(sum[:])
	basename := filepath.Base(opts.JSONLPath)
	sourceRef := fmt.Sprintf("%s@%s", basename, sourceSHA)

	paths := config.Layout(repoRoot)
	if err := os.MkdirAll(paths.Imports, 0o755); err != nil {
		return Result{}, fmt.Errorf("importer: mkdir %s: %w", paths.Imports, err)
	}

	// Step 1a: idempotency check. Walk imports/*.json for any prior run that
	// already recorded the same source@<sha>.
	if existing, err := findExistingImport(paths.Imports, sourceRef); err != nil {
		return Result{}, err
	} else if existing != "" {
		return Result{
			OpsImported:   0,
			IssuesCreated: 0,
			MappingFile:   existing,
			Idempotent:    true,
			SourceSHA:     sourceSHA,
		}, nil
	}

	// Step 1b: parse + validate every line BEFORE any side effect.
	lines, err := parseAndValidate(raw)
	if err != nil {
		return Result{}, err
	}

	// Step 2: read the local config so we can stamp the local node_id and
	// initialize a fresh HLC clock.
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		return Result{}, fmt.Errorf("importer: read config: %w", err)
	}
	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })

	// Step 3: enumerate currently known ids on disk so PickUnique avoids
	// collisions with the existing repo state.
	knownSet, err := listKnownIssueIDs(paths.Ops)
	if err != nil {
		return Result{}, err
	}

	// Step 4: replay each line with the in-process mapping table.
	mapping := make(map[string]string)
	exists := func(id string) bool {
		if knownSet[id] {
			return true
		}
		// already-assigned ids in this batch.
		for _, v := range mapping {
			if v == id {
				return true
			}
		}
		return false
	}

	var (
		writtenPaths  []string
		opsImported   int
		issuesCreated int
	)

	// On any failure during replay, remove any op files we have already
	// written so the repo is left untouched (atomic rollback). This is
	// belt-and-braces given step-1 validation already screens malformed
	// input; failures here would be filesystem-level (write errors).
	rollback := func() {
		for _, p := range writtenPaths {
			_ = os.Remove(p)
		}
	}

	for i, ln := range lines {
		bootstrapID := ln.IssueID
		var localID string
		var payloadBytes []byte

		switch ln.OpType {
		case "create":
			// Derive a fresh local id from the create-payload (using its
			// own nonce so the id is stable for the same source bytes).
			var cp ids.CreatePayload
			if err := json.Unmarshal(ln.Payload, &cp); err != nil {
				rollback()
				return Result{}, fmt.Errorf("importer: line %d: unmarshal create payload: %w", i+1, err)
			}
			id, perr := ids.PickUnique(cp, exists)
			if perr != nil {
				rollback()
				return Result{}, fmt.Errorf("importer: line %d: id collision: %w", i+1, perr)
			}
			localID = id
			mapping[bootstrapID] = localID
			payloadBytes = []byte(ln.Payload)
			issuesCreated++
		default:
			// Non-create ops: rewrite issue_id to the mapped local id.
			mapped, ok := mapping[bootstrapID]
			if !ok {
				// The reference might already be an id (e.g. re-imports
				// where prior runs landed). Pass through verbatim.
				if ids.IsValidID(bootstrapID) {
					localID = bootstrapID
				} else {
					rollback()
					return Result{}, fmt.Errorf("importer: line %d: op_type %q references unmapped issue_id %q", i+1, ln.OpType, bootstrapID)
				}
			} else {
				localID = mapped
			}
			payloadBytes = []byte(ln.Payload)
		}

		// Stamp a fresh local HLC.
		stamp := clock.Send()

		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        ln.OpType,
			IssueID:       localID,
			Payload:       payloadBytes,
			HLC:           stamp,
			NodeID:        cfg.NodeID,
		}
		if verr := env.Validate(); verr != nil {
			rollback()
			return Result{}, fmt.Errorf("importer: line %d: envelope invalid: %w", i+1, verr)
		}
		body, merr := env.Marshal()
		if merr != nil {
			rollback()
			return Result{}, fmt.Errorf("importer: line %d: marshal: %w", i+1, merr)
		}

		fsLock := func() (func(), error) { return func() {}, nil }
		opPath, _, werr := op.ProbeAndWrite(paths.Ops, env, body, fsLock)
		if werr != nil {
			rollback()
			return Result{}, fmt.Errorf("importer: line %d: write: %w", i+1, werr)
		}
		writtenPaths = append(writtenPaths, opPath)
		opsImported++
	}

	// Step 5: write the mapping file via canonical JSON.
	importedAtStamp := clock.Send()
	importedAtHLC := time.UnixMilli(importedAtStamp.Wall).UTC().Format("2006-01-02T15:04:05.000Z")
	mappingFileName := time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + ".json"
	mappingPath := filepath.Join(paths.Imports, mappingFileName)

	mf := mappingFile{
		Source:        sourceRef,
		Mapping:       mapping,
		ImportedAtHLC: importedAtHLC,
	}
	mfBytes, err := canonicaljson.Marshal(mf)
	if err != nil {
		rollback()
		return Result{}, fmt.Errorf("importer: marshal mapping: %w", err)
	}

	// Atomic write: temp + rename in the imports/ dir.
	if err := atomicWriteFile(paths.Imports, mappingPath, mfBytes); err != nil {
		rollback()
		return Result{}, fmt.Errorf("importer: write mapping: %w", err)
	}

	// Step 6: stage everything and commit.
	if !opts.NoCommit {
		for _, p := range writtenPaths {
			if err := gitops.StageOpFile(p); err != nil {
				return Result{}, fmt.Errorf("importer: stage op: %w", err)
			}
		}
		if err := gitops.StageOpFile(mappingPath); err != nil {
			return Result{}, fmt.Errorf("importer: stage mapping: %w", err)
		}
		msg := fmt.Sprintf("act-import: %s sha=%s", basename, sourceSHA)
		if err := gitops.Commit(msg); err != nil {
			return Result{}, fmt.Errorf("importer: commit: %w", err)
		}
		if opts.Push {
			if err := gitops.Push(); err != nil {
				return Result{}, fmt.Errorf("importer: push: %w", err)
			}
		}
	}

	return Result{
		OpsImported:   opsImported,
		IssuesCreated: issuesCreated,
		MappingFile:   mappingPath,
		Idempotent:    false,
		SourceSHA:     sourceSHA,
	}, nil
}

// parseAndValidate splits raw on newlines and validates every line up front.
// Empty lines (including a trailing newline) are skipped. Any failure returns
// an error tagged with the offending line number (1-based).
func parseAndValidate(raw []byte) ([]inputLine, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	// Allow large lines: payloads (e.g. long descriptions) may exceed the
	// 64KiB default buffer.
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var out []inputLine
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ln inputLine
		if err := json.Unmarshal([]byte(line), &ln); err != nil {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: %w", lineNum, err)
		}
		if ln.OpVersion == 0 {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: missing op_version", lineNum)
		}
		if ln.SchemaVersion == 0 {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: missing schema_version", lineNum)
		}
		if ln.OpType == "" {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: missing op_type", lineNum)
		}
		if !op.ValidOpTypes[ln.OpType] {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: unknown op_type %q", lineNum, ln.OpType)
		}
		if ln.IssueID == "" {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: missing issue_id", lineNum)
		}
		if len(ln.Payload) == 0 {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: missing payload", lineNum)
		}
		if err := op.ValidatePayload(ln.OpType, ln.Payload); err != nil {
			return nil, fmt.Errorf("import_invalid_jsonl: line %d: payload: %w", lineNum, err)
		}
		out = append(out, ln)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("importer: scan: %w", err)
	}
	return out, nil
}

// findExistingImport scans importsDir for any *.json file whose `source`
// field equals sourceRef. Returns the path of the first match (lex order)
// or "" when none exists.
func findExistingImport(importsDir, sourceRef string) (string, error) {
	entries, err := os.ReadDir(importsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("importer: read imports dir: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		full := filepath.Join(importsDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			return "", fmt.Errorf("importer: read %s: %w", full, err)
		}
		var mf mappingFile
		if err := json.Unmarshal(data, &mf); err != nil {
			// Corrupt mapping file: skip rather than fail the import.
			continue
		}
		if mf.Source == sourceRef {
			return full, nil
		}
	}
	return "", nil
}

// listKnownIssueIDs returns the set of full ids known to the repo by
// enumerating per-issue subdirectories under opsDir. A missing opsDir is
// reported as an empty set, not an error.
func listKnownIssueIDs(opsDir string) (map[string]bool, error) {
	out := make(map[string]bool)
	entries, err := os.ReadDir(opsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("importer: read ops dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !ids.IsValidID(name) {
			continue
		}
		out[name] = true
	}
	return out, nil
}

// atomicWriteFile writes body to target via a temp file in dir followed by
// os.Rename. The temp file is removed on any error path.
func atomicWriteFile(dir, target string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".import-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := f.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Resolve returns the local act id corresponding to bootstrapID by
// consulting all .act/imports/*.json mapping files in lex order. The first
// hit wins (per spec §3 resolution order). Returns ok==false when no mapping
// matches.
//
// Bootstrap ids are never re-used across imports, so the lex-order = creation
// order semantic is sufficient for unambiguous resolution.
func Resolve(importsDir, bootstrapID string) (localID string, ok bool, err error) {
	all, err := LoadAllMappings(importsDir)
	if err != nil {
		return "", false, err
	}
	v, hit := all[bootstrapID]
	return v, hit, nil
}

// TODO(act-6eff): wire Resolve into cmd/act show and cmd/act log so the
// resolver pipeline (spec §"Pre-import id resolution") falls through to
// .act/imports/*.json after on-disk prefix resolution misses.
