package op

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
)

// CurrentOpVersion is the op envelope version this writer emits.
const CurrentOpVersion = 1

// CurrentSchemaVersion is the schema version this writer emits.
const CurrentSchemaVersion = 1

// WriterVersion is the human-readable version of the writer producing ops.
const WriterVersion = "0.1.0"

// Envelope is the wire shape of every op file. The canonical (lex-sorted) JSON
// rendering is the byte stream stored on disk and used for hashing.
type Envelope struct {
	OpVersion     int             `json:"op_version"`
	SchemaVersion int             `json:"schema_version"`
	WriterVersion string          `json:"writer_version"`
	OpType        string          `json:"op_type"`
	IssueID       string          `json:"issue_id"`
	Payload       json.RawMessage `json:"payload"`
	HLC           hlc.HLC         `json:"hlc"`
	NodeID        string          `json:"node_id"`
}

// ValidOpTypes enumerates the op types defined by the spec.
//
// add_external_dep / remove_external_dep carry opaque-string refs to entities
// in trackers act doesn't import. They are intentionally distinct from
// add_dep / remove_dep: the latter constrain Parent to a valid act id and
// edge_type to a closed set, while external refs are unstructured strings
// the orchestrator owns the lifecycle of.
//
// "redact" is retained as a parse-only legacy entry (act-8d1d removed the
// command, payload, and apply path). Envelope.Validate continues to accept
// historical redact op files so existing .act/ops/ trees fold cleanly; the
// fold dispatch returns nil for "redact" and applyAll silently skips it.
// Writers must not emit new redact ops; the only path for true secret
// removal is git-filter-repo.
var ValidOpTypes = map[string]bool{
	"create":              true,
	"update_field":        true,
	"add_dep":             true,
	"remove_dep":          true,
	"add_external_dep":    true,
	"remove_external_dep": true,
	"add_accept":          true,
	"remove_accept":       true,
	"claim":               true,
	"close":               true,
	"reopen":              true,
	"redact":              true, // legacy, parse-only; no writer or apply path
	"import":              true,
	"migrate":             true,
	"tombstone":           true,
}

var (
	semverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	nodeIDRe = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

// Validate checks the structural invariants of the envelope. It does not
// validate the payload contents (that is per-op-type, handled elsewhere).
func (e Envelope) Validate() error {
	if e.OpVersion != CurrentOpVersion {
		return fmt.Errorf("op: op_version %d: want %d", e.OpVersion, CurrentOpVersion)
	}
	if e.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("op: schema_version %d: want %d", e.SchemaVersion, CurrentSchemaVersion)
	}
	if e.WriterVersion == "" {
		return fmt.Errorf("op: writer_version is empty")
	}
	if !semverRe.MatchString(e.WriterVersion) {
		return fmt.Errorf("op: writer_version %q: not semver", e.WriterVersion)
	}
	if !ValidOpTypes[e.OpType] {
		return fmt.Errorf("op: op_type %q: not a known op type", e.OpType)
	}
	if !ids.IsValidID(e.IssueID) {
		return fmt.Errorf("op: issue_id %q: not a valid id", e.IssueID)
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("op: payload is empty")
	}
	if !nodeIDRe.MatchString(e.NodeID) {
		return fmt.Errorf("op: node_id %q: must be 8 lowercase hex chars", e.NodeID)
	}
	return nil
}

// Marshal returns the canonical JSON encoding of the envelope.
//
// Top-level keys are emitted in lexicographic order (the canonicaljson
// invariant), so the on-disk byte stream is a function of the envelope value
// alone. We first emit via encoding/json (which honours hlc.HLC.MarshalJSON
// for the string wall form) and then recanonicalize to lex-sort keys.
func (e Envelope) Marshal() ([]byte, error) {
	intermediate, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("op: marshal: %w", err)
	}
	var generic any
	if err := json.Unmarshal(intermediate, &generic); err != nil {
		return nil, fmt.Errorf("op: marshal: %w", err)
	}
	return canonicaljson.Marshal(generic)
}

// Unmarshal parses b as an envelope and validates it. It is the inverse of
// Marshal for any envelope that round-trips through canonical JSON.
func Unmarshal(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("op: unmarshal: %w", err)
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// hashInput is the subset of the envelope that participates in the op hash:
// payload, HLC, and node_id. The struct field names here drive the
// canonicaljson key order, which lex-sorts to (hlc, node_id, payload).
type hashInput struct {
	Payload json.RawMessage `json:"payload"`
	HLC     hlc.HLC         `json:"hlc"`
	NodeID  string          `json:"node_id"`
}

// Hash returns the 8-hex-char op hash, defined as the first 8 hex digits of
// sha256(canonical_json({hlc, node_id, payload})). Per spec resolution.
func (e Envelope) Hash() (string, error) {
	full, err := e.FullHash()
	if err != nil {
		return "", err
	}
	return full[:8], nil
}

// FullHash returns the full 64-hex-char sha256 of the canonical hash input
// (hlc, node_id, payload). Filename collision extension slices longer
// prefixes from this value (8, 12, 16) per spec §Op file naming; per spec
// the slicing must NOT re-hash with a different algorithm.
func (e Envelope) FullHash() (string, error) {
	intermediate, err := json.Marshal(hashInput{
		Payload: e.Payload,
		HLC:     e.HLC,
		NodeID:  e.NodeID,
	})
	if err != nil {
		return "", fmt.Errorf("op: hash marshal: %w", err)
	}
	var generic any
	if err := json.Unmarshal(intermediate, &generic); err != nil {
		return "", fmt.Errorf("op: hash marshal: %w", err)
	}
	canon, err := canonicaljson.Marshal(generic)
	if err != nil {
		return "", fmt.Errorf("op: hash canonicaljson: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}
