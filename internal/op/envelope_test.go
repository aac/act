package op

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aac/act/internal/hlc"
)

func goodEnvelope() Envelope {
	return Envelope{
		OpVersion:     CurrentOpVersion,
		SchemaVersion: CurrentSchemaVersion,
		WriterVersion: WriterVersion,
		OpType:        "create",
		IssueID:       "act-abcd",
		Payload:       json.RawMessage(`{"title":"hello"}`),
		HLC: hlc.HLC{
			Wall:    1700000000000,
			Logical: 0,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

func TestValidate_OK(t *testing.T) {
	e := goodEnvelope()
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_AllOpTypes(t *testing.T) {
	for opType := range ValidOpTypes {
		e := goodEnvelope()
		e.OpType = opType
		if err := e.Validate(); err != nil {
			t.Errorf("op_type %q: Validate: %v", opType, err)
		}
	}
	if len(ValidOpTypes) != 15 {
		t.Fatalf("ValidOpTypes has %d entries, want 15", len(ValidOpTypes))
	}
}

func TestValidate_Errors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Envelope)
		wantSub string
	}{
		{"bad op_version", func(e *Envelope) { e.OpVersion = 2 }, "op_version"},
		{"bad schema_version", func(e *Envelope) { e.SchemaVersion = 2 }, "schema_version"},
		{"empty writer_version", func(e *Envelope) { e.WriterVersion = "" }, "writer_version"},
		{"non-semver writer_version", func(e *Envelope) { e.WriterVersion = "wat" }, "writer_version"},
		{"unknown op_type", func(e *Envelope) { e.OpType = "frob" }, "op_type"},
		{"bad issue_id", func(e *Envelope) { e.IssueID = "not-an-id" }, "issue_id"},
		{"empty payload", func(e *Envelope) { e.Payload = nil }, "payload"},
		{"bad node_id length", func(e *Envelope) { e.NodeID = "abc" }, "node_id"},
		{"bad node_id chars", func(e *Envelope) { e.NodeID = "ZZZZZZZZ" }, "node_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := goodEnvelope()
			tc.mutate(&e)
			err := e.Validate()
			if err == nil {
				t.Fatalf("Validate: nil, want error containing %q", tc.wantSub)
			}
			if !bytes.Contains([]byte(err.Error()), []byte(tc.wantSub)) {
				t.Fatalf("Validate: %v, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	e := goodEnvelope()
	b, err := e.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.OpVersion != e.OpVersion ||
		got.SchemaVersion != e.SchemaVersion ||
		got.WriterVersion != e.WriterVersion ||
		got.OpType != e.OpType ||
		got.IssueID != e.IssueID ||
		got.NodeID != e.NodeID {
		t.Fatalf("scalar fields differ:\n got=%+v\nwant=%+v", got, e)
	}
	if got.HLC != e.HLC {
		t.Fatalf("HLC differs: got=%+v want=%+v", got.HLC, e.HLC)
	}
	// Payload bytes should round-trip; the canonical encoding of an object is
	// deterministic, so equality holds key-for-key.
	var gotObj, wantObj map[string]any
	if err := json.Unmarshal(got.Payload, &gotObj); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if err := json.Unmarshal(e.Payload, &wantObj); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if len(gotObj) != len(wantObj) || gotObj["title"] != wantObj["title"] {
		t.Fatalf("payload differs: got=%v want=%v", gotObj, wantObj)
	}
}

func TestMarshal_Deterministic(t *testing.T) {
	e := goodEnvelope()
	b1, err := e.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := e.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("Marshal not deterministic:\n %s\n vs\n %s", b1, b2)
	}
}

func TestHash_Deterministic(t *testing.T) {
	e := goodEnvelope()
	h1, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := e.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("Hash not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 8 {
		t.Fatalf("Hash length %d, want 8", len(h1))
	}
}

func TestHash_DiffersOnPayload(t *testing.T) {
	a := goodEnvelope()
	b := goodEnvelope()
	b.Payload = json.RawMessage(`{"title":"different"}`)
	ah, _ := a.Hash()
	bh, _ := b.Hash()
	if ah == bh {
		t.Fatalf("Hash unchanged across payload diff: %q", ah)
	}
}

func TestHash_DiffersOnHLC(t *testing.T) {
	a := goodEnvelope()
	b := goodEnvelope()
	b.HLC.Logical = 1
	ah, _ := a.Hash()
	bh, _ := b.Hash()
	if ah == bh {
		t.Fatalf("Hash unchanged across hlc diff: %q", ah)
	}
}

func TestHash_DiffersOnNodeID(t *testing.T) {
	a := goodEnvelope()
	b := goodEnvelope()
	b.NodeID = "ffffffff"
	// HLC.NodeID stays the same to isolate the envelope NodeID change.
	ah, _ := a.Hash()
	bh, _ := b.Hash()
	if ah == bh {
		t.Fatalf("Hash unchanged across node_id diff: %q", ah)
	}
}

func TestHash_StableAcrossWriterVersionOnly(t *testing.T) {
	// writer_version is not in the hash input; flipping it must not change
	// the hash.
	a := goodEnvelope()
	b := goodEnvelope()
	b.WriterVersion = "9.9.9"
	ah, _ := a.Hash()
	bh, _ := b.Hash()
	if ah != bh {
		t.Fatalf("Hash changed across writer_version: %q vs %q", ah, bh)
	}
}
