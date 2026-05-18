// Package hlc implements the Hybrid Logical Clock used to timestamp ops.
//
// The HLC algorithm is specified in docs/spec-v2.md §Op-fold and concurrency.
// Each writer maintains a Clock that produces strictly-increasing HLC values
// regardless of local wall-clock skew. Send is used for locally-generated ops;
// Receive folds in an external HLC observed on disk or over the wire.
//
// The package is intentionally I/O free: callers supply a now function that
// returns unix milliseconds. This keeps the algorithm deterministically
// testable with a fake clock and avoids monotonic-clock contamination from
// time.Time.
package hlc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PlausibilityBudgetMs is the maximum allowed skew between an op's wall and
// the local reference (max(now, repo high-water-mark)) before the op is
// rejected at write-time. Five minutes per spec §1.5.
const PlausibilityBudgetMs int64 = 5 * 60 * 1000

// ErrHLCImplausible is returned by Plausible when an op's wall clock is more
// than PlausibilityBudgetMs away from the local reference.
var ErrHLCImplausible = errors.New("hlc: implausible wall clock skew")

// ErrLogicalOverflow is returned by Send/Receive if the logical counter would
// exceed math.MaxUint32. With millisecond walls this is unreachable outside
// adversarial tests, but the explicit error keeps the contract honest.
var ErrLogicalOverflow = errors.New("hlc: logical counter overflow")

// HLC is a single Hybrid Logical Clock reading.
//
// Wall is unix milliseconds in UTC. Logical is the per-millisecond tiebreak
// counter. NodeID is exactly 8 lowercase hex characters identifying the
// writer; it is opaque to this package.
type HLC struct {
	Wall    int64  // milliseconds since unix epoch UTC
	Logical uint32 // tiebreak counter within a single wall-ms
	NodeID  string // 8 lowercase hex chars
}

// Less reports whether a sorts before b. Ordering is lexicographic over the
// tuple (Wall, Logical, NodeID). Equal tuples return false.
//
// NOTE: HLC.Less is the bare-HLC primitive (e.g. ordering claim windows that
// only carry an HLC, or comparing clock states). For per-field LWW gating and
// any other path where op-level identity matters, use Stamp.Less — spec
// §Op-fold mandates that ties on (wall, logical) resolve by op_hash (a full
// SHA-256 over the canonical {payload, hlc, node_id}), NOT by node_id. node_id
// is already mixed into op_hash; tiebreaking by node_id here is preserved only
// as a deterministic-but-spec-irrelevant ordering for the bare-HLC case where
// no op_hash is in scope. Any new LWW-shaped caller should reach for
// Stamp.Less, not HLC.Less.
func (a HLC) Less(b HLC) bool {
	if a.Wall != b.Wall {
		return a.Wall < b.Wall
	}
	if a.Logical != b.Logical {
		return a.Logical < b.Logical
	}
	return a.NodeID < b.NodeID
}

// Stamp pairs an HLC reading with the full op_hash of the op that produced it.
// It is the canonical ordering key for any spec-defined comparison that
// participates in fold semantics:
//
//   - per-field LWW gates (internal/fold/apply.go)
//   - claim winner selection (internal/claim/claim.go)
//   - any future surface that must agree with §5.B's tiebreak rule
//
// Hash is the full 64-hex-char SHA-256 of canonical_json({payload, hlc, node_id})
// per spec §5.B.1. The package treats Hash as opaque; callers that compare
// Stamps must supply consistent hashes for the rule to hold (i.e. both Stamps
// in a comparison must come from the same hashing pipeline).
type Stamp struct {
	HLC  HLC
	Hash string
}

// Less reports whether a sorts before b under the spec-mandated tuple
// (Wall, Logical, Hash). Equal tuples return false.
//
// This is the single comparison primitive used by both the LWW apply path
// (internal/fold/apply.go gateLWW) and the claim winner-selection path
// (internal/claim/claim.go) so the two cannot disagree on operations with
// identical (wall, logical) but distinct hashes — the bug originally tracked
// in act-492e.
func (a Stamp) Less(b Stamp) bool {
	if a.HLC.Wall != b.HLC.Wall {
		return a.HLC.Wall < b.HLC.Wall
	}
	if a.HLC.Logical != b.HLC.Logical {
		return a.HLC.Logical < b.HLC.Logical
	}
	return a.Hash < b.Hash
}

// Clock is a mutex-guarded HLC generator.
//
// A Clock is the canonical source of HLC values for a single writer. It is
// safe for concurrent use; Send and Receive each take the lock for the
// duration of one HLC update.
type Clock struct {
	mu     sync.Mutex
	prev   HLC
	now    func() int64
	nodeID string
}

// NewClock returns a Clock identified by nodeID. now must return the current
// time as unix milliseconds UTC; tests pass a fake. nodeID must be 8 lowercase
// hex characters.
func NewClock(nodeID string, now func() int64) *Clock {
	return &Clock{
		now:    now,
		nodeID: nodeID,
		prev:   HLC{NodeID: nodeID},
	}
}

// Send advances the clock for a locally-generated op and returns the new HLC.
//
// Per spec §1.3:
//
//	wall' = max(now(), prev.wall)
//	logical' = (wall' == prev.wall) ? prev.logical+1 : 0
func (c *Clock) Send() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	wall := now
	if c.prev.Wall > wall {
		wall = c.prev.Wall
	}
	var logical uint32
	if wall == c.prev.Wall {
		if c.prev.Logical == ^uint32(0) {
			panic(ErrLogicalOverflow)
		}
		logical = c.prev.Logical + 1
	}
	c.prev = HLC{Wall: wall, Logical: logical, NodeID: c.nodeID}
	return c.prev
}

// Receive advances the clock after observing an external HLC msg and returns
// the new HLC.
//
// Per spec §1.4:
//
//	wall' = max(now(), msg.wall, prev.wall)
//	if wall' == prev.wall and wall' == msg.wall: logical' = max(prev.logical, msg.logical)+1
//	elif wall' == prev.wall:                     logical' = prev.logical+1
//	elif wall' == msg.wall:                      logical' = msg.logical+1
//	else:                                        logical' = 0
func (c *Clock) Receive(msg HLC) HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	wall := now
	if msg.Wall > wall {
		wall = msg.Wall
	}
	if c.prev.Wall > wall {
		wall = c.prev.Wall
	}
	var logical uint32
	switch {
	case wall == c.prev.Wall && wall == msg.Wall:
		hi := c.prev.Logical
		if msg.Logical > hi {
			hi = msg.Logical
		}
		if hi == ^uint32(0) {
			panic(ErrLogicalOverflow)
		}
		logical = hi + 1
	case wall == c.prev.Wall:
		if c.prev.Logical == ^uint32(0) {
			panic(ErrLogicalOverflow)
		}
		logical = c.prev.Logical + 1
	case wall == msg.Wall:
		if msg.Logical == ^uint32(0) {
			panic(ErrLogicalOverflow)
		}
		logical = msg.Logical + 1
	default:
		logical = 0
	}
	c.prev = HLC{Wall: wall, Logical: logical, NodeID: c.nodeID}
	return c.prev
}

// Plausible returns ErrHLCImplausible if msg.Wall differs by more than the
// 5-minute budget from the local reference, defined as max(now(), repoRef.Wall)
// per spec §1.5.
func (c *Clock) Plausible(msg HLC, repoRef HLC) error {
	now := c.now()
	ref := now
	if repoRef.Wall > ref {
		ref = repoRef.Wall
	}
	delta := msg.Wall - ref
	if delta < 0 {
		delta = -delta
	}
	if delta > PlausibilityBudgetMs {
		return fmt.Errorf("%w: msg.wall=%d ref=%d delta=%dms budget=%dms",
			ErrHLCImplausible, msg.Wall, ref, delta, PlausibilityBudgetMs)
	}
	return nil
}

// formatWall renders ms as YYYY-MM-DDTHH:MM:SS.sssZ (24 chars).
func formatWall(ms int64) string {
	t := time.UnixMilli(ms).UTC()
	return t.Format("2006-01-02T15:04:05.000Z")
}

// parseWall is strict: it requires the YYYY-MM-DDTHH:MM:SS.sssZ form.
func parseWall(s string) (int64, error) {
	if len(s) != 24 {
		return 0, fmt.Errorf("hlc: wall %q: want 24 chars, got %d", s, len(s))
	}
	if !strings.HasSuffix(s, "Z") {
		return 0, fmt.Errorf("hlc: wall %q: must end in Z", s)
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05.000Z", s, time.UTC)
	if err != nil {
		return 0, fmt.Errorf("hlc: wall %q: %w", s, err)
	}
	return t.UnixMilli(), nil
}

// validateNodeID checks the 8-lowercase-hex shape.
func validateNodeID(s string) error {
	if len(s) != 8 {
		return fmt.Errorf("hlc: node_id %q: want 8 chars, got %d", s, len(s))
	}
	if s != strings.ToLower(s) {
		return fmt.Errorf("hlc: node_id %q: must be lowercase", s)
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("hlc: node_id %q: not hex: %w", s, err)
	}
	return nil
}

type wireHLC struct {
	Wall    string `json:"wall"`
	Logical uint32 `json:"logical"`
	NodeID  string `json:"node_id"`
}

// MarshalJSON encodes the HLC as
// {"wall":"<RFC3339Millis>","logical":N,"node_id":"<hex8>"}.
func (a HLC) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireHLC{
		Wall:    formatWall(a.Wall),
		Logical: a.Logical,
		NodeID:  a.NodeID,
	})
}

// UnmarshalJSON decodes the wire form back into an HLC, validating the wall
// format and the 8-hex node_id shape.
func (a *HLC) UnmarshalJSON(b []byte) error {
	var w wireHLC
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	ms, err := parseWall(w.Wall)
	if err != nil {
		return err
	}
	if err := validateNodeID(w.NodeID); err != nil {
		return err
	}
	a.Wall = ms
	a.Logical = w.Logical
	a.NodeID = w.NodeID
	return nil
}
