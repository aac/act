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
	keyImportSource  = "__import_source"
	keyLastMigration = "__last_migration"

	keyClaimHLC = "__claim_hlc"
)

// formatRFC3339Millis renders unix-ms as the canonical RFC3339Millis form
// (matches hlc.formatWall, but exported via apply for use in payloads).
func formatRFC3339Millis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// gateLWW records stamp as the new high-water mark for field, but only when
// stamp is strictly greater than any prior value under hlc.Stamp.Less (the
// (wall, logical, op_hash) ordering mandated by spec §Op-fold). Returns true
// when the caller should proceed to mutate the field.
//
// Using Stamp.Less here is what keeps the LWW path in agreement with the
// claim winner-selection path (internal/claim.claimLess), which has always
// tiebroken by op_hash. Prior to act-492e this used hlc.HLC.Less, which
// tiebreaks by NodeID — a deterministic but spec-wrong ordering for the LWW
// gate. Two ops with identical (wall, logical) from different nodes would
// resolve by NodeID for LWW and by op_hash for claims, producing divergent
// converged state across replicas.
func gateLWW(state *IssueState, field string, stamp hlc.Stamp) bool {
	cur, ok := state.LastHLC[field]
	if !ok || cur.Less(stamp) {
		state.LastHLC[field] = stamp
		return true
	}
	return false
}

// ApplyDispatch returns the per-op-type apply function for opType. Unknown
// or legacy-no-op-path op types (e.g. "redact" after act-8d1d) yield nil;
// fold.applyAll silently skips such ops so historical .act/ops/ trees fold
// cleanly without the removed handler.
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
	case "add_external_dep":
		return applyAddExternalDep
	case "remove_external_dep":
		return applyRemoveExternalDep
	case "add_accept":
		return applyAddAccept
	case "remove_accept":
		return applyRemoveAccept
	case "claim":
		return applyClaim
	case "close":
		return applyClose
	case "reopen":
		return applyReopen
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
func applyCreate(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
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
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	state.Fields["title"] = p.Title
	state.LastHLC["title"] = stamp
	if p.Description != "" {
		state.Fields["description"] = p.Description
		state.LastHLC["description"] = stamp
	}
	priority := 1
	if p.Priority != nil {
		priority = *p.Priority
	}
	state.Fields["priority"] = priority
	state.LastHLC["priority"] = stamp
	itype := p.Type
	if itype == "" {
		itype = "task"
	}
	state.Fields["type"] = itype
	state.LastHLC["type"] = stamp
	if p.Parent != "" {
		state.Fields["parent"] = p.Parent
		state.LastHLC["parent"] = stamp
	}
	// Initialize accept list (may be empty).
	accept := make([]string, len(p.Accept))
	copy(accept, p.Accept)
	state.Fields["accept"] = accept
	state.LastHLC["accept"] = stamp

	state.Fields["status"] = "open"
	state.Fields["created_at"] = formatRFC3339Millis(env.HLC.Wall)
	return nil
}

// applyUpdateField applies an LWW per-field write. The "status" field is
// rejected by validate; if such a payload reaches here we still ignore it
// to keep the apply layer defensive (per the acceptance criteria).
func applyUpdateField(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
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
	if !gateLWW(state, p.Field, hlc.Stamp{HLC: env.HLC, Hash: fullHash}) {
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
func applyAddDep(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
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
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["deps"]; !ok || cur.Less(stamp) {
		state.LastHLC["deps"] = stamp
	}
	return nil
}

// applyRemoveDep removes any matching (parent, edge_type) tuple.
func applyRemoveDep(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
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
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["deps"]; !ok || cur.Less(stamp) {
		state.LastHLC["deps"] = stamp
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

// applyAddExternalDep set-adds Ref into state.Fields["external_deps"]. Re-adding
// an already-present ref is a no-op so the orchestrator can re-fire safely.
//
// External deps are stored as a flat []string rather than (parent, edge_type)
// tuples because act treats refs as opaque — there is no second endpoint to
// store and no edge taxonomy to model. The high-water HLC tracks the
// "external_deps" field key for any add/remove on this issue.
func applyAddExternalDep(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.AddExternalDepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("add_external_dep: unmarshal: %w", err)
	}
	refs := getExternalDeps(state)
	for _, r := range refs {
		if r == p.Ref {
			return nil // already present, idempotent
		}
	}
	refs = append(refs, p.Ref)
	state.Fields["external_deps"] = refs
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["external_deps"]; !ok || cur.Less(stamp) {
		state.LastHLC["external_deps"] = stamp
	}
	return nil
}

// applyRemoveExternalDep removes Ref from state.Fields["external_deps"]. A
// remove that targets a not-present ref is a no-op (idempotent absence) —
// the orchestrator owns the lifecycle and may re-fire a clear without
// having to first observe whether the ref still exists.
func applyRemoveExternalDep(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.RemoveExternalDepPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("remove_external_dep: unmarshal: %w", err)
	}
	refs := getExternalDeps(state)
	out := refs[:0:0]
	for _, r := range refs {
		if r == p.Ref {
			continue
		}
		out = append(out, r)
	}
	state.Fields["external_deps"] = out
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["external_deps"]; !ok || cur.Less(stamp) {
		state.LastHLC["external_deps"] = stamp
	}
	return nil
}

// getExternalDeps returns the external_deps slice as []string. Like getDeps,
// it handles both the live in-process type and the post-JSON-round-trip type
// the fold engine produces when reading test fixtures or migrated state.
func getExternalDeps(state *IssueState) []string {
	raw, ok := state.Fields["external_deps"]
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

// applyAddAccept appends a criterion to state.Fields["accept"] (a []string).
func applyAddAccept(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.AddAcceptPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("add_accept: unmarshal: %w", err)
	}
	accept := getAccept(state)
	accept = append(accept, p.Criterion)
	state.Fields["accept"] = accept
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["accept"]; !ok || cur.Less(stamp) {
		state.LastHLC["accept"] = stamp
	}
	return nil
}

// applyRemoveAccept resolves Index against the current accept list and adds
// the matching text to the removed-set. Out-of-range indices are ignored.
func applyRemoveAccept(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
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
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["accept"]; !ok || cur.Less(stamp) {
		state.LastHLC["accept"] = stamp
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
func applyClaim(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.ClaimPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("claim: unmarshal: %w", err)
	}
	// Once closed, a later claim should not change status. The close LWW
	// gate keeps "status" pinned to "closed" via state.LastHLC["status"].
	if isClosed(state) {
		return nil
	}
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	priorStamp, hasPrior := state.LastHLC[keyClaimHLC]
	if hasPrior && !stamp.Less(priorStamp) {
		// Existing claim is earlier (or equal under (wall, logical, hash));
		// ignore this op. The Stamp.Less tiebreak matches the spec's claim
		// winner selection in internal/claim.
		return nil
	}
	state.LastHLC[keyClaimHLC] = stamp
	state.Fields["assignee"] = p.Assignee
	state.LastHLC["assignee"] = stamp
	state.Fields["status"] = "in_progress"
	state.LastHLC["status"] = stamp
	// claimed_at: wall time of the winning claim, formatted RFC3339Millis,
	// so the index (and `act ready`) can render relative timestamps without
	// re-reading the op log. Symmetrical with created_at / closed_at.
	state.Fields["claimed_at"] = formatRFC3339Millis(env.HLC.Wall)
	state.LastHLC["claimed_at"] = stamp
	return nil
}

// applyClose sets status=closed plus closed_at, closed_reason, and the
// closer's node_id (closed_by_node) for audit. Once closed, the LWW gate
// on "status" prevents subsequent claims from reverting it (because the
// close stamp dominates).
//
// closed_by_tree (the git tree hash at close time) is intentionally NOT
// surfaced here: the envelope does not carry it, and the SQLite index's
// closed_by_tree column is populated post-commit by the close writer.
// Surfacing it through the fold path is deferred until the envelope
// schema carries it; for now `act show` reads closed_by_node from the
// op stream (which is sufficient for the audit acceptance criteria).
func applyClose(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.ClosePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("close: unmarshal: %w", err)
	}
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if !gateLWW(state, "status", stamp) {
		return nil
	}
	state.Fields["status"] = "closed"
	state.Fields["closed_at"] = formatRFC3339Millis(env.HLC.Wall)
	state.LastHLC["closed_at"] = stamp
	state.Fields["closed_reason"] = p.Reason
	state.LastHLC["closed_reason"] = stamp
	state.Fields["closed_by_node"] = env.NodeID
	state.LastHLC["closed_by_node"] = stamp
	// Phase 1 reconcile-lite (act-37f7): persist the close payload's
	// no_code flag so doctor's case (b) check can suppress legitimate
	// no-code closes. Only set when true to keep the rendered state
	// compact for the typical code-producing close.
	if p.NoCode {
		state.Fields["closed_no_code"] = true
		state.LastHLC["closed_no_code"] = stamp
	}
	return nil
}

// applyReopen reverses a close: it sets status back to "open" and clears
// closed_at / closed_reason / closed_by_node. Per spec §5.B.4 the LWW
// high-water marks for these fields are reset to the reopen op's HLC so
// subsequent updates aren't blocked by stale HLCs from before the close.
//
// The op is idempotent on an already-open issue: the status LWW gate
// admits the reopen only when env.HLC dominates the current status
// stamp. A reopen older than a subsequent close is dominated and
// ignored, preserving close-LWW semantics.
func applyReopen(state *IssueState, env op.Envelope, payload []byte, fullHash string) error {
	var p op.ReopenPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("reopen: unmarshal: %w", err)
	}
	_ = p // reason is recorded in the op log; not persisted to state today.
	stamp := hlc.Stamp{HLC: env.HLC, Hash: fullHash}
	if cur, ok := state.LastHLC["status"]; ok && !cur.Less(stamp) {
		return nil
	}
	state.Fields["status"] = "open"
	state.LastHLC["status"] = stamp
	delete(state.Fields, "closed_at")
	delete(state.Fields, "closed_reason")
	delete(state.Fields, "closed_by_node")
	delete(state.Fields, "closed_no_code")
	state.LastHLC["closed_at"] = stamp
	state.LastHLC["closed_reason"] = stamp
	state.LastHLC["closed_by_node"] = stamp
	state.LastHLC["closed_no_code"] = stamp
	// Reopen also drops claim state — the assignee is from a stale claim and
	// the next claim op will write a fresh assignee/claimed_at pair.
	delete(state.Fields, "claimed_at")
	state.LastHLC["claimed_at"] = stamp
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

// applyImport records the source ref under a reserved bookkeeping key.
func applyImport(state *IssueState, _ op.Envelope, payload []byte, _ string) error {
	var p op.ImportPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("import: unmarshal: %w", err)
	}
	state.Fields[keyImportSource] = p.SourceRef
	return nil
}

// applyMigrate records the latest migration (from->to). The actual transform
// is the concern of act-5af9.
func applyMigrate(state *IssueState, _ op.Envelope, payload []byte, _ string) error {
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
func applyTombstone(state *IssueState, _ op.Envelope, _ []byte, _ string) error {
	state.Tombstoned = true
	return nil
}

// RenderState produces a public-facing view of state.
//
// Tombstoned issues yield nil. Reserved __* keys are stripped. The accept
// list is filtered through __accept_removed to produce the visible criteria.
//
// Collection fields are normalised to a single canonical Go type so consumers
// (the SQLite index, callers iterating rendered state) can rely on one type
// assertion regardless of whether state came from a live fold (typed slices)
// or a JSON-deserialised snapshot (untyped []any with map[string]any elems):
//
//   - "accept"        → []string
//   - "deps"          → []map[string]string
//   - "external_deps" → []string
//
// Without this normalisation, a snapshot round-trip silently drops dep edges
// at the upsertTx assertion (see act-8c78).
func RenderState(state *IssueState) map[string]any {
	if state == nil || state.Tombstoned {
		return nil
	}
	out := map[string]any{}
	out["id"] = state.ID

	removed := getRemovedAcceptSet(state)

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
		if k == "deps" {
			// Normalise to []map[string]string so upsertTx's type
			// assertion holds for both live and post-snapshot state.
			out[k] = getDeps(state)
			continue
		}
		if k == "external_deps" {
			// Normalise to []string for the same reason.
			out[k] = getExternalDeps(state)
			continue
		}
		out[k] = v
	}
	return out
}
