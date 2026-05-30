package fold

import (
	"github.com/aac/act/internal/hlc"
)

// Per-field LWW rules.
//
// All scalar fields use Last-Writer-Wins keyed by the per-field stamp tuple
// (wall, logical, op_hash) per spec §Op-fold and concurrency. The canonical
// comparator is hlc.Stamp.Less (defined in internal/hlc); gateLWW in apply.go
// uses it to enforce a strict-greater-than gate. node_id is intentionally
// excluded: it is already mixed into op_hash, which is the canonical tiebreak
// for equal (wall, logical) tuples — and matches the comparator used by the
// claim winner-selection path in internal/claim, so the two cannot disagree
// on operations with identical (wall, logical) from distinct nodes
// (the act-492e bug).
//
// Field-by-field rules:
//
//   - title, description, priority, type, parent, assignee
//     Pure LWW by per-field HLC. update_field is the only writer; close/claim
//     do not touch these (except claim, which atomically writes assignee with
//     "earliest claim wins" semantics — see below).
//
//   - status
//     Not writable via update_field (payload validation rejects field=status,
//     and apply ignores it defensively). The only writers are claim (sets
//     in_progress) and close (sets closed). Status is the only field whose
//     LWW gate is shared between two distinct op types: close uses gateLWW on
//     "status"; once a close stamp lands, the close LWW gate prevents later
//     claims from reverting the value. Reopen is deferred (see act-296e Out
//     of scope). Once status=closed, the issue is terminal until a future
//     reopen op is implemented.
//
//   - assignee + status (claim)
//     Earliest-HLC wins per §5.B.3 — distinct from LWW elsewhere. The first
//     claim to land (smallest HLC tuple) sets assignee and status=in_progress
//     atomically; later claims are ignored even if their HLC is greater. The
//     prior-claim HLC is recorded under the reserved key __claim_hlc.
//
//   - acceptance_criteria (accept)
//     Grow-shrink CRDT with an LWW replace. add_accept appends a criterion to
//     the visible list. remove_accept marks a criterion text into
//     __accept_removed by resolving the requested index against the current
//     effective (visible) list; the reserved set is filtered out at render
//     time. This makes add/remove commutative: add+remove or remove-of-not-
//     yet-added both converge. set_accept (the `act update --accept` replace
//     primitive) carries the full list and overwrites it wholesale via the
//     "accept" LWW gate, clearing __accept_removed; later-stamped add/remove
//     ops resume grow-shrink from the new baseline.
//
//   - deps
//     Grow-shrink CRDT keyed by (parent, edge_type) per §5.C.5. add_dep is
//     idempotent on the (parent, edge_type) pair — the same pair from
//     multiple writers collapses to one entry. remove_dep removes only that
//     exact tuple, leaving entries with matching parent but different
//     edge_type intact.
//
//   - tombstone
//     Sticky terminal. Once Tombstoned is true, RenderState yields nil for
//     the issue regardless of any subsequent op (no op can untombstone).

// IsClosedTerminal reports whether the issue is in the terminal closed state.
//
// Callers (commands such as update_field, claim, close itself) use this to
// short-circuit further writes that would be ignored at apply time anyway.
// This is purely an optimisation: the apply layer remains the source of
// truth, and IsClosedTerminal must agree with what apply will conclude given
// the same state.
//
// A nil state returns false (no decision can be made).
func IsClosedTerminal(state *IssueState) bool {
	if state == nil {
		return false
	}
	v, ok := state.Fields["status"]
	if !ok {
		return false
	}
	s, _ := v.(string)
	return s == "closed"
}

// ResolveStatus returns the effective status string and the HLC at which it
// was last written. The two are kept in sync by the apply layer:
//
//   - close writes status="closed" via gateLWW on "status"
//   - claim writes status="in_progress" via the earliest-claim path, also
//     stamping LastHLC["status"] for the LWW gate consumed by close
//   - create initialises status="open" without a LastHLC stamp (the open
//     state is the implicit baseline)
//
// If state has no status entry, ResolveStatus returns ("open", zero-HLC).
// A nil state returns ("", zero-HLC).
func ResolveStatus(state *IssueState) (string, hlc.HLC) {
	if state == nil {
		return "", hlc.HLC{}
	}
	v, ok := state.Fields["status"]
	if !ok {
		return "open", hlc.HLC{}
	}
	s, _ := v.(string)
	if s == "" {
		s = "open"
	}
	stamp := state.LastHLC["status"]
	return s, stamp.HLC
}
