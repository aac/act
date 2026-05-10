// Package cli — error envelope.
//
// Per spec-v2.md §error-envelope: every non-zero exit with --json MUST emit
// a JSON object on stdout of the shape:
//
//	{"error": "<code-slug>", "message": "<human-readable>", "details": {<obj>}}
//
// `details` is always present in the encoded JSON (may be an empty object)
// so that downstream agents can rely on the key existing.
//
// This file defines:
//   - Error code constants for every class used across the codebase.
//   - The Envelope type that mirrors the spec shape exactly.
//   - New() / Normalize() constructors that gracefully accept the shapes
//     the various per-command *ErrorOutput structs return today and shape
//     them into a uniform Envelope.
//   - Emit() which writes an Envelope to stdout (under --json) or a one-line
//     human message to stderr.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aac/act/internal/hooks"
)

// Error code slugs. The first block matches the spec table verbatim; the
// second block covers internal/per-command codes that appear in existing
// envelopes (kept stable to avoid breaking JSON consumers). When in doubt,
// add a new constant rather than re-using one — codes are stable.
const (
	// Spec §error-envelope canonical codes.
	ErrNotInGit          = "not_in_git"
	ErrActNotInitialized = "act_not_initialized"
	ErrIssueNotFound     = "issue_not_found"
	ErrIDAmbiguous       = "id_ambiguous"
	ErrVersionSkew       = "version_skew"
	ErrClaimLost         = "claim_lost"
	ErrCycleDetected     = "cycle_detected"
	ErrDepNotFound       = "dep_not_found"
	ErrHookFailed        = "hook_failed"
	ErrOpInvalid         = "op_invalid"
	ErrHLCDrift          = "hlc_drift"
	ErrIndexCorrupt      = "index_corrupt"
	ErrImportInvalidJSON = "import_invalid_jsonl"
	ErrCompactionLocked  = "compaction_locked"
	ErrRedactNotFound    = "redact_target_not_found"

	// Internal/per-command error slugs in active use across cli/*.go.
	// They are surfaced as the `error` field of the envelope; tests and
	// JSON consumers depend on them, so renaming is a breaking change.
	ErrBadFlag           = "bad_flag"
	ErrAmbiguousID       = "ambiguous_id" // legacy alias for id_ambiguous
	ErrCycle             = "cycle"        // legacy slug used by `dep add`
	ErrClaimFailed       = "claim_failed"
	ErrConfigReadFailed  = "config_read_failed"
	ErrIndexOpenFailed   = "index_open_failed"
	ErrIndexQueryFailed  = "index_query_failed"
	ErrIndexRebuildFail  = "index_rebuild_failed"
	ErrIndexUpdateFailed = "index_update_failed"
	ErrEnvelopeInvalid   = "envelope_invalid"
	ErrPayloadInvalid    = "payload_invalid"
	ErrMarshalFailed     = "marshal_failed"
	ErrFoldFailed        = "fold_failed"
	ErrOpsScanFailed     = "ops_scan_failed"
	ErrOpsWalkFailed     = "ops_walk_failed"
	ErrOpsReadFailed     = "ops_read_failed"
	ErrPushFailed        = "push_failed"
	ErrWriteFailed       = "write_failed"
	ErrStatFailed        = "stat_failed"
	ErrWalkFailed        = "walk_failed"
	ErrNoRepo            = "no_repo"
	ErrImportFailed      = "import_failed"
)

// Envelope is the canonical JSON shape emitted on every non-zero exit
// under --json. Field order in the encoded JSON is fixed by struct
// declaration order to keep golden tests stable.
//
// Details is a map (rather than `any`) so its JSON representation is
// always an object. The MarshalJSON method below substitutes an empty
// object literal for a nil map, satisfying the "Details always present,
// may be empty object" rule.
type Envelope struct {
	Error   string         `json:"error"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// MarshalJSON renders the envelope. nil Details becomes `{}` rather than
// `null` so the contract holds even when callers pass no detail map.
func (e Envelope) MarshalJSON() ([]byte, error) {
	d := e.Details
	if d == nil {
		d = map[string]any{}
	}
	return json.Marshal(struct {
		Error   string         `json:"error"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}{
		Error:   e.Error,
		Message: e.Message,
		Details: d,
	})
}

// New is the canonical Envelope constructor. nil details is preserved
// (and rendered as `{}` on encode); pass an empty map explicitly if you
// want to be unambiguous in callers.
func New(code, message string, details map[string]any) Envelope {
	return Envelope{Error: code, Message: message, Details: details}
}

// Normalize accepts the various ad-hoc payload shapes the per-command
// *ErrorOutput structs use today (typed structs with Error/Message/...,
// or arbitrary map[string]any envelopes) and squashes them into a single
// Envelope. Unknown fields end up under details so no information is lost.
//
// Recognised top-level keys: error, message, details, candidates, path.
// Anything else is moved into details verbatim. If `error` is itself an
// object (the legacy DepAddCycleOutput shape `{"error":{"kind":"cycle",
// "path":[...]}}`), it is flattened: `kind` becomes the error code and
// the remaining keys move into details.
func Normalize(payload any) Envelope {
	if payload == nil {
		return Envelope{Error: "unknown", Message: "", Details: map[string]any{}}
	}
	// Round-trip through JSON to handle typed structs uniformly.
	data, err := json.Marshal(payload)
	if err != nil {
		return Envelope{Error: "marshal_failed", Message: err.Error(), Details: map[string]any{}}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		// Payload was not an object (e.g. a raw string). Salvage what we can.
		return Envelope{Error: "unknown", Message: fmt.Sprintf("%v", payload), Details: map[string]any{}}
	}
	env := Envelope{Details: map[string]any{}}

	// `error` may be a string (modern shape) or an object (legacy cycle shape).
	switch e := m["error"].(type) {
	case string:
		env.Error = e
	case map[string]any:
		// Legacy `{"error":{"kind":"...","path":[...]}}` form.
		if k, ok := e["kind"].(string); ok {
			env.Error = k
		}
		for k, v := range e {
			if k == "kind" {
				continue
			}
			env.Details[k] = v
		}
	}
	if msg, ok := m["message"].(string); ok {
		env.Message = msg
	}
	// If a details map was provided, merge it (caller takes precedence).
	if d, ok := m["details"].(map[string]any); ok {
		for k, v := range d {
			env.Details[k] = v
		}
	}
	// Promote well-known auxiliary fields into details.
	for _, k := range []string{"candidates", "path", "winner", "winner_node_id", "winner_hlc", "stderr_tail", "issue_id", "query", "prefix", "target", "exit_code", "hook"} {
		if v, ok := m[k]; ok {
			env.Details[k] = v
		}
	}
	return env
}

// MaxStderrTail caps the stderr_tail length captured into Details so a
// failing hook or git command can't blow up the JSON envelope size. The
// value is in bytes; truncation keeps the LAST N bytes (the most recent
// output is usually the most diagnostic).
const MaxStderrTail = 4096

// CaptureStderrTail trims a stderr blob to the last MaxStderrTail bytes
// so it can safely live under details.stderr_tail.
func CaptureStderrTail(s string) string {
	if len(s) <= MaxStderrTail {
		return s
	}
	return s[len(s)-MaxStderrTail:]
}

// SplitWrappedError separates a wrapped command error into a clean prefix
// and the trailing `(output: ...)` blob produced by the gitops/claim
// runGit wrappers. Used by claim_failed (and any future code path that
// wraps subprocess stderr inside a Go error message) to keep raw stderr
// out of the user-facing `message` and into `details.stderr_tail`.
//
// When no `(output: ...)` marker is present the entire string becomes the
// prefix and tail is empty.
func SplitWrappedError(s string) (prefix, tail string) {
	const marker = "(output: "
	i := indexLast(s, marker)
	if i < 0 {
		return s, ""
	}
	rest := s[i+len(marker):]
	if n := len(rest); n > 0 && rest[n-1] == ')' {
		rest = rest[:n-1]
	}
	pfx := s[:i]
	for len(pfx) > 0 && (pfx[len(pfx)-1] == ' ' || pfx[len(pfx)-1] == ':') {
		pfx = pfx[:len(pfx)-1]
	}
	return pfx, rest
}

// indexLast returns the index of the last occurrence of substr in s, or
// -1 if absent. Tiny stand-in for strings.LastIndex so this file does not
// take a strings dependency (keeps the import surface minimal).
func indexLast(s, substr string) int {
	if len(substr) == 0 || len(s) < len(substr) {
		return -1
	}
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Emit writes an Envelope to one of two destinations:
//   - asJSON=true: the JSON encoding goes to stdout, followed by a newline.
//     stderr stays empty (the spec mandates this so agents parsing stdout
//     don't have to also drain stderr).
//   - asJSON=false: a single-line human-readable string goes to stderr.
//     Format: "<message>" if message is non-empty, else "<code>". No
//     trailing period; no ANSI.
//
// Either writer may be nil; in that case the corresponding output is
// silently dropped. (Tests use this to capture only one stream.)
func Emit(env Envelope, asJSON bool, stdout, stderr io.Writer) {
	if asJSON {
		if stdout == nil {
			return
		}
		data, err := json.Marshal(env)
		if err != nil {
			fmt.Fprintf(stdout, "{\"error\":\"marshal_failed\",\"message\":%q,\"details\":{}}\n", err.Error())
			return
		}
		fmt.Fprintln(stdout, string(data))
		return
	}
	if stderr == nil {
		return
	}
	if env.Message != "" {
		fmt.Fprintln(stderr, env.Message)
		return
	}
	if env.Error != "" {
		fmt.Fprintln(stderr, env.Error)
	}
}

// hookStderrExcerptLines is the number of trailing stderr lines surfaced
// inline in the human message. The full StderrTail (up to MaxStderrTail
// bytes) still lands in Details["hook_stderr"] so JSON consumers get the
// complete diagnostic.
const hookStderrExcerptLines = 10

// HookFailureDetails extracts a structured envelope from an error returned
// by a write-op path that ran a hook. When err wraps *hooks.HookFailedError
// (which carries the hook's exit code + last 4096 bytes of its stderr),
// the human Message includes the last hookStderrExcerptLines of that
// stderr so the user can diagnose without re-running the hook, and the
// returned details map carries hook_stderr / hook_exit_code /
// hook_truncated for JSON consumers. isHookFailure==false means err was
// not a HookFailedError; callers should fall back to err.Error() under
// their existing error code.
func HookFailureDetails(err error) (message string, details map[string]any, isHookFailure bool) {
	var herr *hooks.HookFailedError
	if !errors.As(err, &herr) {
		return err.Error(), nil, false
	}
	details = map[string]any{
		"hook_exit_code": herr.Code,
		"hook_truncated": herr.Truncated,
	}
	tail := herr.StderrTail
	if tail != "" {
		details["hook_stderr"] = tail
	}
	excerpt := lastLines(tail, hookStderrExcerptLines)
	if excerpt == "" {
		return fmt.Sprintf("hook exited %d", herr.Code), details, true
	}
	return fmt.Sprintf("hook exited %d:\n%s", herr.Code, excerpt), details, true
}

// lastLines returns the last n lines of s, joined by '\n'. Trailing newlines
// in s are trimmed before splitting so the excerpt never has a hanging blank
// line. If s has ≤ n lines, all lines are returned. Empty s → empty result.
func lastLines(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	// Trim a single trailing newline so a hook that ends its stderr with
	// "\n" doesn't produce a phantom empty last line.
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	if s == "" {
		return ""
	}
	// Walk backward counting newlines; cheaper than splitting + slicing
	// and avoids a strings dependency consistent with indexLast above.
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[i+1:]
			}
		}
	}
	return s
}
