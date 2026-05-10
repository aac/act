package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// RedactOptions captures the flag knobs for `act redact`.
//
// Per spec §3 + §5.A.2 + line 1042: redact replaces the named field's
// rendered value with the configured replacement (defaulting to
// "<redacted>"). Re-redacting an already-redacted field is idempotent
// (changed=false) and writes no op.
type RedactOptions struct {
	// ID is the positional <id> argument (full or unique prefix).
	ID string
	// FieldPath is the redact target. Supported forms:
	//   - bare scalar field name: "title", "description", "assignee", ...
	//   - structured indexed path: "acceptance_criteria[N].text".
	FieldPath string
	// Replacement is the rendered substitute. Empty defaults to "<redacted>"
	// at write time so the on-disk envelope always carries an explicit
	// value.
	Replacement string

	// AsJSON toggles JSON envelope rendering.
	AsJSON bool
	// NoCommit, Push, Isolated, Verify mirror the universal write flags.
	NoCommit bool
	Push     bool
	Isolated bool
	Verify   bool
}

// RedactResult is the JSON-serialisable success envelope for a write that
// actually emitted a new redact op.
type RedactResult struct {
	ID          string `json:"id"`
	ShortID     string `json:"short_id"`
	FieldPath   string `json:"field_path"`
	Replacement string `json:"replacement"`
	OpsWritten  int    `json:"ops_written"`
	Committed   bool   `json:"committed"`
	Changed     bool   `json:"changed"`
}

// RedactNoChange is the JSON-serialisable envelope returned when the target
// field is already redacted at the current fold; no op is written.
type RedactNoChange struct {
	ID        string `json:"id"`
	FieldPath string `json:"field_path"`
	Changed   bool   `json:"changed"`
}

// RedactErrorOutput is the structured failure envelope.
type RedactErrorOutput struct {
	Error      string         `json:"error"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
	Candidates []string       `json:"-"`
}

// defaultRedactReplacement matches the spec wording (the on-the-wire
// replacement default per §"Op type payloads": redact's `new_value`
// defaults to "<redacted>").
const defaultRedactReplacement = "<redacted>"

// redactableScalarFields enumerates the bare field names that may be
// targeted by a redact op. Mirrors the rendered-state surface (so e.g.
// "deps", "accept" cannot be redacted as a whole — use the structured
// path for individual acceptance criteria).
var redactableScalarFields = map[string]bool{
	"title":         true,
	"description":   true,
	"assignee":      true,
	"closed_reason": true,
}

// acceptCriterionPathRE matches the structured indexed form for redacting
// a single acceptance criterion.
var acceptCriterionPathRE = regexp.MustCompile(`^acceptance_criteria\[(\d+)\]\.text$`)

// validateFieldPath returns nil iff path is a recognized redact target.
//
// Accepted forms (per spec §5.A.2 + the op-payload schema):
//   - "title", "description", "assignee", "closed_reason"  (scalar fields)
//   - "acceptance_criteria[N].text"                        (indexed)
func validateFieldPath(path string) error {
	if path == "" {
		return fmt.Errorf("field path is empty")
	}
	if redactableScalarFields[path] {
		return nil
	}
	if acceptCriterionPathRE.MatchString(path) {
		return nil
	}
	return fmt.Errorf("field path %q: not a recognized redact target (expected one of title|description|assignee|closed_reason or acceptance_criteria[N].text)", path)
}

// RunRedact implements `act redact <id> --field <path> [--value TEXT]`.
//
// Steps:
//  1. Require a git working tree + initialised .act/.
//  2. Validate flag combinations + the field-path shape.
//  3. Resolve <id> via the prefix pipeline.
//  4. Fold the issue. If FieldPath is already in the redacted-paths set,
//     return idempotent {changed:false} exit 0; no op is written.
//  5. Build a redact envelope; write + auto-commit; refresh index.
//
// Returns:
//   - output: RedactResult on a true redact, RedactNoChange on the
//     idempotent path, RedactErrorOutput on failure.
//   - exitCode: 0 success / idempotent no-op; 1 hook reject / write
//     failure; 2 bad flags / invalid field path / ambiguous prefix;
//     3 missing repo / missing .act/ / unknown id.
func RunRedact(repoRoot string, opts RedactOptions) (output any, exitCode int) {
	// Step 1: repo + .act/ required.
	if !hasGitDir(repoRoot) {
		return RedactErrorOutput{
			Error:   "not_in_git",
			Message: fmt.Sprintf("act redact: %s is not inside a git working tree", repoRoot),
		}, 3
	}
	paths := config.Layout(repoRoot)
	if _, err := os.Stat(paths.ConfigJSON); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RedactErrorOutput{
				Error:   "act_not_initialized",
				Message: fmt.Sprintf("act redact: %s/.act not initialized; run `act init` first", repoRoot),
			}, 3
		}
		return RedactErrorOutput{
			Error:   "act_not_initialized",
			Message: fmt.Sprintf("act redact: stat %s: %v", paths.ConfigJSON, err),
		}, 3
	}

	// Step 2a: positional id.
	if opts.ID == "" {
		return RedactErrorOutput{
			Error:   "bad_flag",
			Message: "act redact: <id> is required",
		}, 2
	}
	// Step 2b: field path required + valid.
	if opts.FieldPath == "" {
		return RedactErrorOutput{
			Error:   "bad_flag",
			Message: "act redact: --field is required",
		}, 2
	}
	if err := validateFieldPath(opts.FieldPath); err != nil {
		return RedactErrorOutput{
			Error:   "bad_flag",
			Message: fmt.Sprintf("act redact: %v", err),
		}, 2
	}
	// Step 2c: universal-write-flag conflicts.
	if opts.NoCommit && opts.Push {
		return RedactErrorOutput{
			Error:   "bad_flag",
			Message: "act redact: --no-commit and --push are mutually exclusive",
		}, 2
	}
	if opts.Isolated && opts.Push {
		return RedactErrorOutput{
			Error:   "bad_flag",
			Message: "act redact: --isolated and --push are mutually exclusive",
		}, 2
	}

	// Step 3: id resolution.
	knownIDs, err := listIssueIDs(paths.Ops)
	if err != nil {
		return RedactErrorOutput{
			Error:   "ops_scan_failed",
			Message: err.Error(),
		}, 1
	}
	full, rerr := ids.Resolve(opts.ID, knownIDs)
	if rerr != nil {
		if errors.Is(rerr, ids.ErrNotFound) {
			return RedactErrorOutput{
				Error:   "issue_not_found",
				Message: fmt.Sprintf("act redact: %q: no matching id", opts.ID),
				Details: map[string]any{"query": opts.ID},
			}, 3
		}
		var amb *ids.ErrAmbiguousID
		if errors.As(rerr, &amb) {
			candidates := amb.Candidates()
			return RedactErrorOutput{
				Error:   "id_ambiguous",
				Message: fmt.Sprintf("act redact: prefix %q matches %d issues", opts.ID, len(candidates)),
				Details: map[string]any{
					"prefix":     opts.ID,
					"candidates": candidates,
				},
				Candidates: candidates,
			}, 2
		}
		return RedactErrorOutput{
			Error:   "issue_not_found",
			Message: rerr.Error(),
			Details: map[string]any{"query": opts.ID},
		}, 3
	}

	// Step 4: fold the issue and check the redacted-paths set. If the
	// target path is already redacted, return the idempotent no-op
	// envelope and write no op (per spec edge case at line 1042 — note:
	// the spec leaves the "second redact op is written" choice to the
	// implementation; the acceptance criterion in act-g008 mandates
	// `{changed:false}` and exit 0, so we elide the write entirely).
	state, ferr := fold.FoldIssue(paths.Ops, full, fold.ApplyDispatch)
	if ferr != nil {
		return RedactErrorOutput{
			Error:   "fold_failed",
			Message: ferr.Error(),
		}, 1
	}
	if isFieldPathRedacted(state, opts.FieldPath) {
		return RedactNoChange{
			ID:        full,
			FieldPath: opts.FieldPath,
			Changed:   false,
		}, 0
	}

	// Step 5: build the redact envelope.
	cfg, cerr := config.ReadConfig(paths)
	if cerr != nil {
		return RedactErrorOutput{
			Error:   "config_read_failed",
			Message: cerr.Error(),
		}, 1
	}

	replacement := opts.Replacement
	if replacement == "" {
		replacement = defaultRedactReplacement
	}

	payload := op.RedactPayload{
		FieldPath:   opts.FieldPath,
		Replacement: replacement,
	}
	if verr := payload.Validate(); verr != nil {
		return RedactErrorOutput{
			Error:   "payload_invalid",
			Message: verr.Error(),
		}, 1
	}
	bodyPayload, perr := canonicaljson.Marshal(payload)
	if perr != nil {
		return RedactErrorOutput{
			Error:   "marshal_failed",
			Message: perr.Error(),
		}, 1
	}

	clock := hlc.NewClock(cfg.NodeID, func() int64 { return time.Now().UnixMilli() })
	stamp := clock.Send()
	stamp.NodeID = cfg.NodeID

	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "redact",
		IssueID:       full,
		Payload:       bodyPayload,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	if verr := env.Validate(); verr != nil {
		return RedactErrorOutput{
			Error:   "envelope_invalid",
			Message: verr.Error(),
		}, 1
	}
	body, merr := env.Marshal()
	if merr != nil {
		return RedactErrorOutput{
			Error:   "marshal_failed",
			Message: merr.Error(),
		}, 1
	}

	// Step 6: write + auto-commit via the shared helper.
	var gops *gitops.GitOps
	if !opts.NoCommit {
		gops = gitops.NewGitOps(repoRoot)
		gops.Verify = opts.Verify
	}
	if werr := WriteOpAndAutoCommit(env, body, paths, gops, WriteOpts{
		NoCommit: opts.NoCommit,
		Push:     opts.Push,
		Isolated: opts.Isolated,
	}); werr != nil {
		if errors.Is(werr, ErrInvalidFlags) {
			return RedactErrorOutput{
				Error:   "bad_flag",
				Message: werr.Error(),
			}, 2
		}
		return RedactErrorOutput{
			Error:   "write_failed",
			Message: werr.Error(),
		}, 1
	}

	if rerr := RefreshIndexForIssue(paths, full); rerr != nil {
		return RedactErrorOutput{
			Error:   "index_update_failed",
			Message: rerr.Error(),
		}, 1
	}

	return RedactResult{
		ID:          full,
		ShortID:     ShortIssueID(full),
		FieldPath:   opts.FieldPath,
		Replacement: replacement,
		OpsWritten:  1,
		Committed:   !opts.NoCommit,
		Changed:     true,
	}, 0
}

// isFieldPathRedacted reports whether state's redacted-paths set already
// contains the requested path. The set is stored under the reserved
// "__redacted_paths" key by applyRedact (map[string]bool).
func isFieldPathRedacted(state *fold.IssueState, fieldPath string) bool {
	if state == nil {
		return false
	}
	raw, ok := state.Fields["__redacted_paths"]
	if !ok {
		return false
	}
	if m, ok := raw.(map[string]bool); ok {
		return m[fieldPath]
	}
	if m, ok := raw.(map[string]any); ok {
		// Defensive: a JSON-roundtrip would yield map[string]any, though
		// the in-memory apply path always uses map[string]bool.
		v, exists := m[fieldPath]
		if !exists {
			return false
		}
		b, _ := v.(bool)
		return b
	}
	return false
}

// FormatRedactHuman renders a RedactResult as a single human-friendly line.
func FormatRedactHuman(res RedactResult) string {
	verb := "wrote"
	if !res.Committed {
		verb = "staged"
	}
	return fmt.Sprintf("Redacted %s field=%s (%s 1 op)\n", res.ShortID, res.FieldPath, verb)
}

// FormatRedactNoChangeHuman renders the idempotent no-change envelope.
func FormatRedactNoChangeHuman(res RedactNoChange) string {
	return fmt.Sprintf("Already redacted: %s field=%s\n", res.ID, res.FieldPath)
}
