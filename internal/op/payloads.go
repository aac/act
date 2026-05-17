package op

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aac/act/internal/ids"
)

// Per-op-type payload structs. These mirror the wire shape described in
// spec-v2.md §Data model > Op type payloads. Each carries a Validate method
// used at write time to reject malformed payloads before they hit disk.
//
// Status changes are NOT representable via update_field; per spec §5.A.4,
// status moves through claim/close ops only.

// validIssueTypes is the set of issue types accepted by create.
var validIssueTypes = map[string]bool{
	"task":  true,
	"bug":   true,
	"epic":  true,
	"chore": true,
}

// validUpdateFields is the closed set of fields that update_field may target.
// "status" is included but constrained: the value must NOT be "closed" or
// "in_progress" per §5.A.4 (those transitions go through close/claim).
// status=blocked and status=open are reachable via update_field; act_block
// in particular relies on this.
var validUpdateFields = map[string]bool{
	"title":       true,
	"description": true,
	"priority":    true,
	"assignee":    true,
	"type":        true,
	"parent":      true,
	"status":      true,
}

// statusUpdateFieldForbidden enumerates status values that MUST go through
// claim/close ops rather than update_field, per §5.A.4.
var statusUpdateFieldForbidden = map[string]bool{
	"closed":      true,
	"in_progress": true,
}

// validEdgeTypes is the closed set of dependency edge types.
var validEdgeTypes = map[string]bool{
	"blocks":     true,
	"relates":    true,
	"supersedes": true,
}

// validMigrateKinds is the closed set of recognized migrate transform kinds.
var validMigrateKinds = map[string]bool{
	"rename_field": true,
	"drop_field":   true,
	"coerce_type":  true,
}

// CreatePayload is the payload for op_type=create.
type CreatePayload struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	Type        string   `json:"type"`
	Parent      string   `json:"parent,omitempty"`
	Accept      []string `json:"accept,omitempty"`
	Nonce       string   `json:"nonce"`
}

// Validate implements the create-payload write-time rules.
func (p CreatePayload) Validate() error {
	if p.Title == "" {
		return fmt.Errorf("op: create.title is empty")
	}
	if len(p.Title) > 200 {
		return fmt.Errorf("op: create.title length %d > 200", len(p.Title))
	}
	if !validIssueTypes[p.Type] {
		return fmt.Errorf("op: create.type %q: not one of {task,bug,epic,chore}", p.Type)
	}
	if p.Priority != nil {
		if *p.Priority < 0 || *p.Priority > 3 {
			return fmt.Errorf("op: create.priority %d out of range [0,3]", *p.Priority)
		}
	}
	if p.Parent != "" && !ids.IsValidID(p.Parent) {
		return fmt.Errorf("op: create.parent %q: not a valid id", p.Parent)
	}
	for i, c := range p.Accept {
		if c == "" {
			return fmt.Errorf("op: create.accept[%d] is empty", i)
		}
		if len(c) > 500 {
			return fmt.Errorf("op: create.accept[%d] length %d > 500 bytes (see 'act help workflow' for cap rationale)", i, len(c))
		}
	}
	if !nonceLooksValid(p.Nonce) {
		return fmt.Errorf("op: create.nonce %q: must be 32 hex chars", p.Nonce)
	}
	return nil
}

// UpdateFieldPayload is the payload for op_type=update_field.
type UpdateFieldPayload struct {
	Field string          `json:"field"`
	Value json.RawMessage `json:"value"`
}

// Validate implements the update_field write-time rules.
func (p UpdateFieldPayload) Validate() error {
	if !validUpdateFields[p.Field] {
		return fmt.Errorf("op: update_field.field %q: not in valid set", p.Field)
	}
	if len(p.Value) == 0 {
		return fmt.Errorf("op: update_field.value is empty")
	}
	if p.Field == "status" {
		// §5.A.4: status=closed and status=in_progress MUST go through
		// the close op and claim op respectively. status=blocked and
		// status=open are reachable via update_field.
		var s string
		if err := json.Unmarshal(p.Value, &s); err != nil {
			return fmt.Errorf("op: update_field.value (status): %w", err)
		}
		if statusUpdateFieldForbidden[s] {
			return fmt.Errorf("op: update_field status=%s: MUST go through claim/close", s)
		}
	}
	return nil
}

// AddDepPayload is the payload for op_type=add_dep.
type AddDepPayload struct {
	Parent   string `json:"parent"`
	EdgeType string `json:"edge_type"`
}

// Validate implements the add_dep write-time rules.
func (p AddDepPayload) Validate() error {
	if !ids.IsValidID(p.Parent) {
		return fmt.Errorf("op: add_dep.parent %q: not a valid id", p.Parent)
	}
	if !validEdgeTypes[p.EdgeType] {
		return fmt.Errorf("op: add_dep.edge_type %q: not one of {blocks,relates,supersedes}", p.EdgeType)
	}
	return nil
}

// RemoveDepPayload is the payload for op_type=remove_dep.
type RemoveDepPayload struct {
	Parent   string `json:"parent"`
	EdgeType string `json:"edge_type"`
}

// Validate implements the remove_dep write-time rules.
func (p RemoveDepPayload) Validate() error {
	if !ids.IsValidID(p.Parent) {
		return fmt.Errorf("op: remove_dep.parent %q: not a valid id", p.Parent)
	}
	if !validEdgeTypes[p.EdgeType] {
		return fmt.Errorf("op: remove_dep.edge_type %q: not one of {blocks,relates,supersedes}", p.EdgeType)
	}
	return nil
}

// MaxExternalRefLen is the byte cap on an external dep ref. External refs are
// opaque identifiers from sibling trackers (URLs, IDs, slugs); 256 bytes is
// generous for any well-formed identifier and keeps wire shape predictable.
const MaxExternalRefLen = 256

// AddExternalDepPayload is the payload for op_type=add_external_dep. Ref is
// an opaque string the caller owns the meaning of; act stores it verbatim.
type AddExternalDepPayload struct {
	Ref string `json:"ref"`
}

// Validate implements the add_external_dep write-time rules: ref must be a
// non-empty, length-capped string of printable characters (no control chars,
// no embedded NUL). The printable-check rejects the typical wire-corruption
// vector — embedded NUL or newline accidentally pasted into an id — without
// being overly restrictive about which tracker the ref came from.
func (p AddExternalDepPayload) Validate() error {
	return validateExternalRef("add_external_dep", p.Ref)
}

// RemoveExternalDepPayload is the payload for op_type=remove_external_dep.
type RemoveExternalDepPayload struct {
	Ref string `json:"ref"`
}

// Validate mirrors AddExternalDepPayload.Validate: same cap, same character
// rules. A remove on a not-present ref is valid at the wire level; the apply
// layer handles idempotent absence.
func (p RemoveExternalDepPayload) Validate() error {
	return validateExternalRef("remove_external_dep", p.Ref)
}

// validateExternalRef centralises ref-shape rules so add and remove can't
// drift apart. opType is woven into the error message so a caller staring at
// a validation failure can tell which side rejected.
func validateExternalRef(opType, ref string) error {
	if ref == "" {
		return fmt.Errorf("op: %s.ref is empty", opType)
	}
	if len(ref) > MaxExternalRefLen {
		return fmt.Errorf("op: %s.ref length %d > %d bytes", opType, len(ref), MaxExternalRefLen)
	}
	for i, r := range ref {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("op: %s.ref contains a control character at byte %d", opType, i)
		}
	}
	return nil
}

// AddAcceptPayload is the payload for op_type=add_accept.
type AddAcceptPayload struct {
	Criterion string `json:"criterion"`
}

// Validate implements the add_accept write-time rules.
func (p AddAcceptPayload) Validate() error {
	if p.Criterion == "" {
		return fmt.Errorf("op: add_accept.criterion is empty")
	}
	if len(p.Criterion) > 500 {
		return fmt.Errorf("op: add_accept.criterion length %d > 500 bytes (see 'act help workflow' for cap rationale)", len(p.Criterion))
	}
	return nil
}

// RemoveAcceptPayload is the payload for op_type=remove_accept.
type RemoveAcceptPayload struct {
	Index int `json:"index"`
}

// Validate implements the remove_accept write-time rules.
func (p RemoveAcceptPayload) Validate() error {
	if p.Index < 0 {
		return fmt.Errorf("op: remove_accept.index %d: must be >= 0", p.Index)
	}
	return nil
}

// ClaimPayload is the payload for op_type=claim.
type ClaimPayload struct {
	Assignee string `json:"assignee"`
}

// Validate implements the claim write-time rules.
func (p ClaimPayload) Validate() error {
	if p.Assignee == "" {
		return fmt.Errorf("op: claim.assignee is empty")
	}
	return nil
}

// ClosePayload is the payload for op_type=close.
type ClosePayload struct {
	Reason string `json:"reason,omitempty"`
}

// Validate implements the close write-time rules.
func (p ClosePayload) Validate() error {
	if len(p.Reason) > 500 {
		return fmt.Errorf("op: close.reason length %d > 500 bytes (see 'act help workflow' for cap rationale)", len(p.Reason))
	}
	return nil
}

// ReopenPayload is the payload for op_type=reopen. Per spec §5.B.4, a
// reopen op clears `closed_at` and `closed_reason` and resets their
// `last_hlc` so subsequent updates aren't blocked by stale HLCs from
// before the close.
type ReopenPayload struct {
	Reason string `json:"reason,omitempty"`
}

// Validate implements the reopen write-time rules.
func (p ReopenPayload) Validate() error {
	if len(p.Reason) > 500 {
		return fmt.Errorf("op: reopen.reason length %d > 500 bytes (see 'act help workflow' for cap rationale)", len(p.Reason))
	}
	return nil
}

// RedactPayload is the payload for op_type=redact.
type RedactPayload struct {
	FieldPath   string `json:"field_path"`
	Replacement string `json:"replacement,omitempty"`
}

// Validate implements the redact write-time rules.
func (p RedactPayload) Validate() error {
	if p.FieldPath == "" {
		return fmt.Errorf("op: redact.field_path is empty")
	}
	return nil
}

// ImportPayload is the payload for op_type=import.
type ImportPayload struct {
	SourceRef string            `json:"source_ref"`
	Mapping   map[string]string `json:"mapping,omitempty"`
}

// Validate implements the import write-time rules.
func (p ImportPayload) Validate() error {
	if p.SourceRef == "" {
		return fmt.Errorf("op: import.source_ref is empty")
	}
	return nil
}

// MigratePayload is the payload for op_type=migrate.
type MigratePayload struct {
	FromVersion int             `json:"from_version"`
	ToVersion   int             `json:"to_version"`
	Transform   json.RawMessage `json:"transform,omitempty"`
}

// Validate implements the migrate write-time rules.
func (p MigratePayload) Validate() error {
	if p.FromVersion <= 0 {
		return fmt.Errorf("op: migrate.from_version %d: must be > 0", p.FromVersion)
	}
	if p.ToVersion <= 0 {
		return fmt.Errorf("op: migrate.to_version %d: must be > 0", p.ToVersion)
	}
	if p.FromVersion >= p.ToVersion {
		return fmt.Errorf("op: migrate.from_version %d: must be < to_version %d", p.FromVersion, p.ToVersion)
	}
	return nil
}

// TombstonePayload is the payload for op_type=tombstone.
type TombstonePayload struct {
	DeletedAt string `json:"deleted_at"`
}

// Validate implements the tombstone write-time rules.
func (p TombstonePayload) Validate() error {
	if p.DeletedAt == "" {
		return fmt.Errorf("op: tombstone.deleted_at is empty")
	}
	if _, err := time.Parse(time.RFC3339, p.DeletedAt); err != nil {
		return fmt.Errorf("op: tombstone.deleted_at %q: not RFC3339: %w", p.DeletedAt, err)
	}
	return nil
}

// nonceLooksValid reports whether s is a 32-char lowercase-hex nonce.
func nonceLooksValid(s string) bool {
	if len(s) != 2*ids.NonceBytes {
		return false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return false
	}
	return true
}

// ValidatePayload unmarshals payload bytes per opType and dispatches to the
// per-type Validate method. It returns a wrapped error on JSON parse failure
// or on validation rule violation.
func ValidatePayload(opType string, payload []byte) error {
	if !ValidOpTypes[opType] {
		return fmt.Errorf("op: op_type %q: not a known op type", opType)
	}
	if len(payload) == 0 {
		return fmt.Errorf("op: payload is empty")
	}
	switch opType {
	case "create":
		var p CreatePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal create payload: %w", err)
		}
		return p.Validate()
	case "update_field":
		var p UpdateFieldPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal update_field payload: %w", err)
		}
		return p.Validate()
	case "add_dep":
		var p AddDepPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal add_dep payload: %w", err)
		}
		return p.Validate()
	case "remove_dep":
		var p RemoveDepPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal remove_dep payload: %w", err)
		}
		return p.Validate()
	case "add_external_dep":
		var p AddExternalDepPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal add_external_dep payload: %w", err)
		}
		return p.Validate()
	case "remove_external_dep":
		var p RemoveExternalDepPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal remove_external_dep payload: %w", err)
		}
		return p.Validate()
	case "add_accept":
		var p AddAcceptPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal add_accept payload: %w", err)
		}
		return p.Validate()
	case "remove_accept":
		var p RemoveAcceptPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal remove_accept payload: %w", err)
		}
		return p.Validate()
	case "claim":
		var p ClaimPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal claim payload: %w", err)
		}
		return p.Validate()
	case "close":
		var p ClosePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal close payload: %w", err)
		}
		return p.Validate()
	case "reopen":
		var p ReopenPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal reopen payload: %w", err)
		}
		return p.Validate()
	case "redact":
		var p RedactPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal redact payload: %w", err)
		}
		return p.Validate()
	case "import":
		var p ImportPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal import payload: %w", err)
		}
		return p.Validate()
	case "migrate":
		var p MigratePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal migrate payload: %w", err)
		}
		return p.Validate()
	case "tombstone":
		var p TombstonePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("op: unmarshal tombstone payload: %w", err)
		}
		return p.Validate()
	}
	return fmt.Errorf("op: op_type %q: no payload validator registered", opType)
}
