package fold

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// Reserved internal field keys. These never appear in the public render; they
// are used to track per-op bookkeeping that downstream rendering consumes.
const (
	keyAcceptRemoved = "__accept_removed"
	keyRedactedPaths = "__redacted_paths"
	keyImportSource  = "__import_source"
	keyLastMigration = "__last_migration"

	keyClaimHLC = "__claim_hlc"
)

// formatRFC3339Millis renders unix-ms as the canonical RFC3339Millis form
// (matches hlc.formatWall, but exported via apply for use in payloads).
func formatRFC3339Millis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// updateLastHLC records env.HLC as the new high-water mark for fields, but
// only when env.HLC is strictly greater than any prior value (LWW gate).
// Returns true when the caller should proceed to mutate the field.
func gateLWW(state *IssueState, field string, h hlc.HLC) bool {
	cur, ok := state.LastHLC[field]
	if !ok || cur.Less(h) {
		state.LastHLC[field] = h
		return true
	}
	return false
}

// ApplyDispatch returns the per-op-type apply function for opType. Unknown
// op types yield nil, which fold.applyAll surfaces as an error.
func ApplyDispatch(opType string) ApplyFunc {
	switch opType {
	case "create":
		return applyCreate
	case "update_field":
		return applyUpdateField
	case "add_dep":
		return applyAddDep
	case "remove_dep":
		return applyRemoveDep
	case "add_accept":
		return applyAddAccept
	case "remove_accept":
		return applyRemoveAccept
	case "claim":
		return applyClaim
	case "close":
		return applyClose
	case "redact":
		return applyRedact
	case "import":
		return applyImport
	case "migrate":
		return applyMigrate
	case "tombstone":
		return applyTombstone
	}
	return nil
}

// applyCreate populates initial fields. It refuses a second create on a
// state that already carries a "created_at" stamp — first-create wins.
func applyCreate(state *IssueState, env op.Envelope, payload []byte) error {
	if state.ID != env.IssueID {
		return fmt.Errorf("create: state.ID %q != env.IssueID %q", state.ID, env.IssueID)
	}
	if _, exists := state.Fields["created_at"]; exists {
		// Idempotent ignore: per acceptance "first-create wins; subsequent
		// creates ignored at apply layer."
		return nil
	}
	var p op.CreatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("create: unmarshal payload: %w", err)
	}
	state.Fields["title"] = p.Title
	state.LastHLC["title"] = env.HLC
	if p.Description != "" {
		state.Fields["description"] = p.Description
		state.LastHLC["description"] = env.HLC
	}
	priority := 1
	if p.Priority != nil {
		priority = *p.Priority
	}
	state.Fields["priority"] = priority
	state.LastHLC["priority"] = env.HLC
	itype := p.Type
	if itype == "" {
		itype = "task"
	}
	state.Fields["type"] = itype
	state.LastHLC["type"] = env.HLC
	if p.Parent != "" {
		state.Fields["parent"] = p.Parent
		state.LastHLC["parent"] = env.HLC
	}
	// Initialize accept list (may be empty).
	accept := make([]string, len(p.Accept))
	copy(accept, p.Accept)
	state.Fields["accept"] = accept
	state.LastHLC["accept"] = env.HLC

	state.Fields["status"] = "open"
	state.Fields["created_at"] = formatRFC3339Millis(env.HLC.Wall)
	return nil
}

// applyUpdateField applies an LWW per-field write. The "status" field is
// rejected by validate; if such a payload reaches here we still ignore it
// to keep the apply layer defensive (per the acceptance criteria).
func applyUpdateField(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.UpdateFieldPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("update_field: unmarshal: %w", err)
	}
	if p.Field == "status" {
		// Defensive: writer rejects this; apply ignores rather than mutates.
		return nil
	}
	if !isAllowedUpdateField(p.Field) {
		// Unknown field name: leave state unchanged (per spec acceptance).
		return nil
	}
	if !gateLWW(state, p.Field, env.HLC) {
		return nil
	}
	var v any
	if err := json.Unmarshal(p.Value, &v); err != nil {
		return fmt.Errorf("update_field: unmarshal value: %w", err)
	}
	state.Fields[p.Field] = v
	return nil
}

func isAllowedUpdateField(name string) bool {
	switch name {
	case "title", "description", "priority", "assignee", "type", "parent":
		return true
	}
	return false
}

// applyAddDep set-adds (parent, edge_type) into state.Fields["deps"].
func applyAddDep(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.AddDepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("add_dep: unmarshal: %w", err)
	}
	deps := getDeps(state)
	for _, d := range deps {
		if d["parent"] == p.Parent && d["edge_type"] == p.EdgeType {
			return nil // already present, idempotent
		}
	}
	deps = append(deps, map[string]string{"parent": p.Parent, "edge_type": p.EdgeType})
	state.Fields["deps"] = deps
	if cur, ok := state.LastHLC["deps"]; !ok || cur.Less(env.HLC) {
		state.LastHLC["deps"] = env.HLC
	}
	return nil
}

// applyRemoveDep removes any matching (parent, edge_type) tuple.
func applyRemoveDep(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.RemoveDepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("remove_dep: unmarshal: %w", err)
	}
	deps := getDeps(state)
	out := deps[:0:0]
	for _, d := range deps {
		if d["parent"] == p.Parent && d["edge_type"] == p.EdgeType {
			continue
		}
		out = append(out, d)
	}
	state.Fields["deps"] = out
	if cur, ok := state.LastHLC["deps"]; !ok || cur.Less(env.HLC) {
		state.LastHLC["deps"] = env.HLC
	}
	return nil
}

// getDeps returns the deps slice as []map[string]string (lazy-init if absent).
func getDeps(state *IssueState) []map[string]string {
	raw, ok := state.Fields["deps"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []map[string]string:
		return v
	case []any:
		out := make([]map[string]string, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				parent, _ := m["parent"].(string)
				edge, _ := m["edge_type"].(string)
				out = append(out, map[string]string{"parent": parent, "edge_type": edge})
			}
		}
		return out
	}
	return nil
}

// applyAddAccept appends a criterion to state.Fields["accept"] (a []string).
func applyAddAccept(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.AddAcceptPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("add_accept: unmarshal: %w", err)
	}
	accept := getAccept(state)
	accept = append(accept, p.Criterion)
	state.Fields["accept"] = accept
	if cur, ok := state.LastHLC["accept"]; !ok || cur.Less(env.HLC) {
		state.LastHLC["accept"] = env.HLC
	}
	return nil
}

// applyRemoveAccept resolves Index against the current accept list and adds
// the matching text to the removed-set. Out-of-range indices are ignored.
func applyRemoveAccept(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.RemoveAcceptPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("remove_accept: unmarshal: %w", err)
	}
	accept := getAccept(state)
	// Compute the effective (visible) list: accept minus removed-set.
	removed := getRemovedAcceptSet(state)
	effective := make([]string, 0, len(accept))
	for _, c := range accept {
		if !removed[c] {
			effective = append(effective, c)
		}
	}
	if p.Index < 0 || p.Index >= len(effective) {
		return nil // out-of-range removed: idempotent ignore
	}
	target := effective[p.Index]
	removed[target] = true
	setRemovedAcceptSet(state, removed)
	if cur, ok := state.LastHLC["accept"]; !ok || cur.Less(env.HLC) {
		state.LastHLC["accept"] = env.HLC
	}
	return nil
}

// getAccept returns the accept list as []string (lazy-init).
func getAccept(state *IssueState) []string {
	raw, ok := state.Fields["accept"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func getRemovedAcceptSet(state *IssueState) map[string]bool {
	raw, ok := state.Fields[keyAcceptRemoved]
	if !ok {
		return map[string]bool{}
	}
	if m, ok := raw.(map[string]bool); ok {
		return m
	}
	return map[string]bool{}
}

func setRemovedAcceptSet(state *IssueState, m map[string]bool) {
	state.Fields[keyAcceptRemoved] = m
}

// applyClaim sets (assignee, status=in_progress). Earliest-HLC wins per
// §5.B.3: if state already records a claim with smaller HLC, this op is
// ignored. Otherwise the HLC tuple is recorded under keyClaimHLC and used
// for subsequent comparison.
func applyClaim(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.ClaimPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("claim: unmarshal: %w", err)
	}
	// Once closed, a later claim should not change status. The close LWW
	// gate keeps "status" pinned to "closed" via state.LastHLC["status"].
	if isClosed(state) {
		return nil
	}
	priorHLC, hasPrior := state.LastHLC[keyClaimHLC]
	if hasPrior && !env.HLC.Less(priorHLC) {
		// Existing claim is earlier (or equal); ignore this op.
		return nil
	}
	state.LastHLC[keyClaimHLC] = env.HLC
	state.Fields["assignee"] = p.Assignee
	state.LastHLC["assignee"] = env.HLC
	state.Fields["status"] = "in_progress"
	state.LastHLC["status"] = env.HLC
	return nil
}

// applyClose sets status=closed plus closed_at and closed_reason. Once
// closed, the LWW gate on "status" prevents subsequent claims from
// reverting it (because the close stamp dominates).
func applyClose(state *IssueState, env op.Envelope, payload []byte) error {
	var p op.ClosePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("close: unmarshal: %w", err)
	}
	if !gateLWW(state, "status", env.HLC) {
		return nil
	}
	state.Fields["status"] = "closed"
	state.Fields["closed_at"] = formatRFC3339Millis(env.HLC.Wall)
	state.LastHLC["closed_at"] = env.HLC
	state.Fields["closed_reason"] = p.Reason
	state.LastHLC["closed_reason"] = env.HLC
	return nil
}

func isClosed(state *IssueState) bool {
	v, ok := state.Fields["status"]
	if !ok {
		return false
	}
	s, _ := v.(string)
	return s == "closed"
}

// applyRedact records a redacted field path. Rendering enforcement happens
// at read time via RenderState.
func applyRedact(state *IssueState, _ op.Envelope, payload []byte) error {
	var p op.RedactPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("redact: unmarshal: %w", err)
	}
	set := getStringSet(state, keyRedactedPaths)
	set[p.FieldPath] = true
	state.Fields[keyRedactedPaths] = set
	return nil
}

// applyImport records the source ref under a reserved bookkeeping key.
func applyImport(state *IssueState, _ op.Envelope, payload []byte) error {
	var p op.ImportPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("import: unmarshal: %w", err)
	}
	state.Fields[keyImportSource] = p.SourceRef
	return nil
}

// applyMigrate records the latest migration (from->to). The actual transform
// is the concern of act-5af9.
func applyMigrate(state *IssueState, _ op.Envelope, payload []byte) error {
	var p op.MigratePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("migrate: unmarshal: %w", err)
	}
	state.Fields[keyLastMigration] = map[string]any{
		"from_version": p.FromVersion,
		"to_version":   p.ToVersion,
	}
	return nil
}

// applyTombstone marks the issue tombstoned. The fold engine's stub also
// flagged this; here we own the semantics canonically.
func applyTombstone(state *IssueState, _ op.Envelope, _ []byte) error {
	state.Tombstoned = true
	return nil
}

func getStringSet(state *IssueState, key string) map[string]bool {
	raw, ok := state.Fields[key]
	if !ok {
		return map[string]bool{}
	}
	if m, ok := raw.(map[string]bool); ok {
		return m
	}
	return map[string]bool{}
}

// RenderState produces a public-facing view of state.
//
// Tombstoned issues yield nil. Reserved __* keys are stripped. The accept
// list is filtered through __accept_removed to produce the visible criteria.
// Any redacted scalar field is replaced with the string "<redacted>".
func RenderState(state *IssueState) map[string]any {
	if state == nil || state.Tombstoned {
		return nil
	}
	out := map[string]any{}
	out["id"] = state.ID

	removed := getRemovedAcceptSet(state)
	redacted := getStringSet(state, keyRedactedPaths)

	for k, v := range state.Fields {
		if strings.HasPrefix(k, "__") {
			continue
		}
		if k == "accept" {
			accept := getAccept(state)
			visible := make([]string, 0, len(accept))
			for _, c := range accept {
				if !removed[c] {
					visible = append(visible, c)
				}
			}
			out[k] = visible
			continue
		}
		if redacted[k] {
			out[k] = "<redacted>"
			continue
		}
		out[k] = v
	}
	return out
}
