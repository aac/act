package fold

// Test helpers shared across property and fuzz tests.
//
// makeOpFile and makeRandomOps live here so the property tests, the fuzzer,
// and any future cross-test code can build well-formed envelopes without
// duplicating boilerplate. The generator covers all 12 op types and
// deliberately partitions writes per (issue_id, field) so commutative-disjoint
// permutations remain LWW-equivalent.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeOpFile writes an op envelope to disk under <dir>/<issueID>/<YYYY-MM>/
// using the canonical filename. It returns the full path. The envelope is
// rebuilt from the (opType, hlc, payload) tuple supplied by the caller.
func makeOpFile(t *testing.T, dir, issueID, opType string, h hlc.HLC, payload any) string {
	t.Helper()
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("makeOpFile: marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pb,
		HLC:           h,
		NodeID:        h.NodeID,
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("makeOpFile: envelope marshal: %v", err)
	}
	shard := op.ShardDir(dir, issueID, h.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("makeOpFile: mkdir %s: %v", shard, err)
	}
	path := filepath.Join(shard, op.Filename(env))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("makeOpFile: write %s: %v", path, err)
	}
	return path
}

// allOpTypes is the spec's 12-op closure, listed in declaration order so the
// generator's modulo selection is stable across runs.
var allOpTypes = []string{
	"create",
	"update_field",
	"add_dep",
	"remove_dep",
	"add_accept",
	"remove_accept",
	"claim",
	"close",
	"redact",
	"import",
	"migrate",
	"tombstone",
}

// fixedNodes is a small set of valid 8-hex node ids the generator picks from.
var fixedNodes = []string{"11111111", "22222222", "33333333", "44444444"}

// fixedIssues is a small set of valid issue ids — the generator partitions ops
// across these so cross-issue ops stay disjoint regardless of permutation.
var fixedIssues = []string{"act-aaaa", "act-bbbb", "act-cccc", "act-dddd"}

// fixedFields is the closed update_field set.
var fixedFields = []string{"title", "description", "priority", "type", "parent", "assignee"}

// fixedEdges is the closed dep edge_type set.
var fixedEdges = []string{"blocks", "relates", "supersedes"}

// fixedAccepts is a small criterion alphabet so add/remove ops collide and
// exercise the grow-shrink CRDT.
var fixedAccepts = []string{"ac-1", "ac-2", "ac-3", "ac-4"}

// fixedAssignees is the small assignee alphabet.
var fixedAssignees = []string{"alice", "bob", "carol", "dave"}

// makeRandomOps returns n deterministic op envelopes for the supplied seed.
// Every emitted envelope is well-formed: writer-validated payloads, valid
// HLC, valid node_id, and parent ids drawn from fixedIssues so add_dep and
// remove_dep payloads pass ids.IsValidID.
//
// The generator emits at most one create per issue (the apply layer ignores
// double-creates, but emitting one keeps the op stream realistic) and every
// non-create op is targeted to an issue that already received a create
// earlier in the sequence.
func makeRandomOps(seed int64, n int) []op.Envelope {
	r := rand.New(rand.NewSource(seed))
	out := make([]op.Envelope, 0, n)
	created := map[string]bool{}

	// Always seed every issue with a create so subsequent ops have a state to
	// mutate. We pre-seed at HLC walls 1..len(fixedIssues).
	var wall int64 = 1
	var logical uint32
	for i, id := range fixedIssues {
		create := op.CreatePayload{
			Title:    fmt.Sprintf("issue-%d", i),
			Type:     "task",
			Nonce:    deterministicNonce(r),
			Priority: ptrInt(1),
		}
		out = append(out, buildEnvelope(id, "create", hlc.HLC{
			Wall: wall, Logical: logical, NodeID: fixedNodes[r.Intn(len(fixedNodes))],
		}, create))
		created[id] = true
		wall++
	}

	for i := 0; i < n; i++ {
		// Step the wall in a small range to provoke ties on (wall, logical)
		// across writers; the op_hash tiebreak path is then exercised.
		wall += int64(r.Intn(3))
		logical = uint32(r.Intn(8))
		node := fixedNodes[r.Intn(len(fixedNodes))]
		issue := fixedIssues[r.Intn(len(fixedIssues))]
		if !created[issue] {
			// Defensive: shouldn't happen since we pre-seeded all issues.
			continue
		}
		opType := allOpTypes[r.Intn(len(allOpTypes))]
		payload := generatePayload(r, opType, issue)
		if payload == nil {
			// Skip op types we can't represent without recursive sequencing
			// (e.g. a second create on the same issue is ignored at apply,
			// but emitting one would inflate hash collisions).
			i--
			continue
		}
		out = append(out, buildEnvelope(issue, opType, hlc.HLC{
			Wall: wall, Logical: logical, NodeID: node,
		}, payload))
	}
	return out
}

// generatePayload builds a writer-valid payload value for opType. Returns
// nil to signal "skip this draw and try another op type" (used to suppress
// double-creates).
func generatePayload(r *rand.Rand, opType, issue string) any {
	switch opType {
	case "create":
		// Suppress: duplicate creates are ignored at apply time and would
		// inflate the op stream without changing semantics.
		return nil
	case "update_field":
		field := fixedFields[r.Intn(len(fixedFields))]
		var value json.RawMessage
		switch field {
		case "title":
			value = json.RawMessage(fmt.Sprintf(`"t-%d"`, r.Intn(8)))
		case "description":
			value = json.RawMessage(fmt.Sprintf(`"d-%d"`, r.Intn(8)))
		case "priority":
			value = json.RawMessage(fmt.Sprintf(`%d`, r.Intn(4)))
		case "type":
			types := []string{"task", "bug", "epic", "chore"}
			value = json.RawMessage(fmt.Sprintf(`"%s"`, types[r.Intn(len(types))]))
		case "parent":
			value = json.RawMessage(fmt.Sprintf(`"%s"`, fixedIssues[r.Intn(len(fixedIssues))]))
		case "assignee":
			value = json.RawMessage(fmt.Sprintf(`"%s"`, fixedAssignees[r.Intn(len(fixedAssignees))]))
		}
		return op.UpdateFieldPayload{Field: field, Value: value}
	case "add_dep":
		// Pick a parent that's not the issue itself.
		var parent string
		for {
			parent = fixedIssues[r.Intn(len(fixedIssues))]
			if parent != issue {
				break
			}
		}
		return op.AddDepPayload{Parent: parent, EdgeType: fixedEdges[r.Intn(len(fixedEdges))]}
	case "remove_dep":
		var parent string
		for {
			parent = fixedIssues[r.Intn(len(fixedIssues))]
			if parent != issue {
				break
			}
		}
		return op.RemoveDepPayload{Parent: parent, EdgeType: fixedEdges[r.Intn(len(fixedEdges))]}
	case "add_accept":
		return op.AddAcceptPayload{Criterion: fixedAccepts[r.Intn(len(fixedAccepts))]}
	case "remove_accept":
		return op.RemoveAcceptPayload{Index: r.Intn(4)}
	case "claim":
		return op.ClaimPayload{Assignee: fixedAssignees[r.Intn(len(fixedAssignees))]}
	case "close":
		reasons := []string{"done", "wontfix", "duplicate", ""}
		return op.ClosePayload{Reason: reasons[r.Intn(len(reasons))]}
	case "redact":
		return op.RedactPayload{FieldPath: fixedFields[r.Intn(len(fixedFields))]}
	case "import":
		return op.ImportPayload{SourceRef: fmt.Sprintf("src://%d", r.Intn(8))}
	case "migrate":
		from := 1 + r.Intn(2)
		to := from + 1 + r.Intn(2)
		return op.MigratePayload{FromVersion: from, ToVersion: to}
	case "tombstone":
		// Use a fixed time string so the payload validates and the generator
		// remains deterministic.
		return op.TombstonePayload{DeletedAt: time.UnixMilli(1700000000000).UTC().Format(time.RFC3339)}
	}
	return nil
}

// buildEnvelope wraps a payload value in a fully-stamped envelope. The
// payload is marshalled to canonical-friendly JSON via encoding/json; that's
// sufficient for the envelope hash, which is computed by the op package.
func buildEnvelope(issueID, opType string, h hlc.HLC, payload any) op.Envelope {
	pb, _ := json.Marshal(payload)
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pb,
		HLC:           h,
		NodeID:        h.NodeID,
	}
}

// deterministicNonce returns a 32-hex nonce drawn from r.
func deterministicNonce(r *rand.Rand) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, 32)
	for i := range b {
		b[i] = hexdigits[r.Intn(16)]
	}
	return string(b)
}

func ptrInt(v int) *int { return &v }
