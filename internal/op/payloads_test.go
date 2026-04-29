package op

import (
	"encoding/json"
	"strings"
	"testing"
)

func intPtr(v int) *int { return &v }

const validNonce = "0123456789abcdef0123456789abcdef"
const validParent = "act-abcd"

// CreatePayload --------------------------------------------------------------

func TestCreatePayload_Validate_OK(t *testing.T) {
	p := CreatePayload{
		Title:       "do the thing",
		Description: "a description",
		Priority:    intPtr(2),
		Type:        "task",
		Parent:      validParent,
		Accept:      []string{"works", "is documented"},
		Nonce:       validNonce,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCreatePayload_Validate_TitleEmpty(t *testing.T) {
	p := CreatePayload{Title: "", Type: "task", Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_TitleTooLong(t *testing.T) {
	p := CreatePayload{Title: strings.Repeat("x", 201), Type: "task", Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_TypeUnknown(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "story", Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_PriorityOutOfRange(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Priority: intPtr(4), Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for priority=4")
	}
	p.Priority = intPtr(-1)
	if err := p.Validate(); err == nil {
		t.Fatal("want error for priority=-1")
	}
}

func TestCreatePayload_Validate_ParentInvalid(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Parent: "not-an-id", Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_AcceptEmpty(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Accept: []string{""}, Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_AcceptTooLong(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Accept: []string{strings.Repeat("x", 501)}, Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_NonceBadLength(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Nonce: "abcd"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestCreatePayload_Validate_NonceNotHex(t *testing.T) {
	p := CreatePayload{Title: "t", Type: "task", Nonce: strings.Repeat("z", 32)}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// UpdateFieldPayload ---------------------------------------------------------

func TestUpdateFieldPayload_Validate_OK(t *testing.T) {
	p := UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"new title"`)}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestUpdateFieldPayload_Validate_StatusRejected(t *testing.T) {
	p := UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"open"`)}
	err := p.Validate()
	if err == nil {
		t.Fatal("want error for status field")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("want error mentioning status, got %v", err)
	}
}

func TestUpdateFieldPayload_Validate_FieldUnknown(t *testing.T) {
	p := UpdateFieldPayload{Field: "frobnitz", Value: json.RawMessage(`1`)}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestUpdateFieldPayload_Validate_ValueEmpty(t *testing.T) {
	p := UpdateFieldPayload{Field: "title"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// AddDepPayload --------------------------------------------------------------

func TestAddDepPayload_Validate_OK(t *testing.T) {
	p := AddDepPayload{Parent: validParent, EdgeType: "blocks"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAddDepPayload_Validate_ParentInvalid(t *testing.T) {
	p := AddDepPayload{Parent: "x", EdgeType: "blocks"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestAddDepPayload_Validate_EdgeTypeInvalid(t *testing.T) {
	p := AddDepPayload{Parent: validParent, EdgeType: "duplicates"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// RemoveDepPayload -----------------------------------------------------------

func TestRemoveDepPayload_Validate_OK(t *testing.T) {
	p := RemoveDepPayload{Parent: validParent, EdgeType: "relates"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRemoveDepPayload_Validate_ParentInvalid(t *testing.T) {
	p := RemoveDepPayload{Parent: "", EdgeType: "relates"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestRemoveDepPayload_Validate_EdgeTypeInvalid(t *testing.T) {
	p := RemoveDepPayload{Parent: validParent, EdgeType: "is-a"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// AddAcceptPayload -----------------------------------------------------------

func TestAddAcceptPayload_Validate_OK(t *testing.T) {
	p := AddAcceptPayload{Criterion: "tests pass"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAddAcceptPayload_Validate_CriterionEmpty(t *testing.T) {
	p := AddAcceptPayload{Criterion: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestAddAcceptPayload_Validate_CriterionTooLong(t *testing.T) {
	p := AddAcceptPayload{Criterion: strings.Repeat("x", 501)}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// RemoveAcceptPayload --------------------------------------------------------

func TestRemoveAcceptPayload_Validate_OK(t *testing.T) {
	p := RemoveAcceptPayload{Index: 0}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRemoveAcceptPayload_Validate_IndexNegative(t *testing.T) {
	p := RemoveAcceptPayload{Index: -1}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// ClaimPayload ---------------------------------------------------------------

func TestClaimPayload_Validate_OK(t *testing.T) {
	p := ClaimPayload{Assignee: "alice"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestClaimPayload_Validate_AssigneeEmpty(t *testing.T) {
	p := ClaimPayload{Assignee: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// ClosePayload ---------------------------------------------------------------

func TestClosePayload_Validate_OK_EmptyReason(t *testing.T) {
	p := ClosePayload{Reason: ""}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestClosePayload_Validate_OK_WithReason(t *testing.T) {
	p := ClosePayload{Reason: "fixed in 1.2"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestClosePayload_Validate_ReasonTooLong(t *testing.T) {
	p := ClosePayload{Reason: strings.Repeat("x", 501)}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// RedactPayload --------------------------------------------------------------

func TestRedactPayload_Validate_OK(t *testing.T) {
	p := RedactPayload{FieldPath: "description", Replacement: "<redacted>"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRedactPayload_Validate_FieldPathEmpty(t *testing.T) {
	p := RedactPayload{FieldPath: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// ImportPayload --------------------------------------------------------------

func TestImportPayload_Validate_OK(t *testing.T) {
	p := ImportPayload{SourceRef: "github:owner/repo#1", Mapping: map[string]string{"x": "y"}}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestImportPayload_Validate_SourceRefEmpty(t *testing.T) {
	p := ImportPayload{SourceRef: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// MigratePayload -------------------------------------------------------------

func TestMigratePayload_Validate_OK(t *testing.T) {
	p := MigratePayload{FromVersion: 1, ToVersion: 2, Transform: json.RawMessage(`{"kind":"rename_field"}`)}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestMigratePayload_Validate_FromZero(t *testing.T) {
	p := MigratePayload{FromVersion: 0, ToVersion: 2}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestMigratePayload_Validate_ToZero(t *testing.T) {
	p := MigratePayload{FromVersion: 1, ToVersion: 0}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestMigratePayload_Validate_FromNotLessThanTo(t *testing.T) {
	p := MigratePayload{FromVersion: 2, ToVersion: 2}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for equal versions")
	}
	p = MigratePayload{FromVersion: 3, ToVersion: 2}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for from > to")
	}
}

// TombstonePayload -----------------------------------------------------------

func TestTombstonePayload_Validate_OK(t *testing.T) {
	p := TombstonePayload{DeletedAt: "2026-04-29T12:00:00.000Z"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTombstonePayload_Validate_Empty(t *testing.T) {
	p := TombstonePayload{DeletedAt: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

func TestTombstonePayload_Validate_NotRFC3339(t *testing.T) {
	p := TombstonePayload{DeletedAt: "yesterday"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// ValidatePayload dispatcher -------------------------------------------------

func TestValidatePayload_AllOpTypes(t *testing.T) {
	cases := []struct {
		opType  string
		payload string
	}{
		{"create", `{"title":"t","type":"task","nonce":"` + validNonce + `"}`},
		{"update_field", `{"field":"title","value":"new"}`},
		{"add_dep", `{"parent":"` + validParent + `","edge_type":"blocks"}`},
		{"remove_dep", `{"parent":"` + validParent + `","edge_type":"relates"}`},
		{"add_accept", `{"criterion":"works"}`},
		{"remove_accept", `{"index":0}`},
		{"claim", `{"assignee":"alice"}`},
		{"close", `{"reason":"done"}`},
		{"redact", `{"field_path":"description","replacement":"<redacted>"}`},
		{"import", `{"source_ref":"github:owner/repo#1"}`},
		{"migrate", `{"from_version":1,"to_version":2}`},
		{"tombstone", `{"deleted_at":"2026-04-29T12:00:00Z"}`},
	}
	if len(cases) != len(ValidOpTypes) {
		t.Fatalf("dispatcher smoke test covers %d types, but ValidOpTypes has %d",
			len(cases), len(ValidOpTypes))
	}
	for _, tc := range cases {
		t.Run(tc.opType, func(t *testing.T) {
			if err := ValidatePayload(tc.opType, []byte(tc.payload)); err != nil {
				t.Fatalf("ValidatePayload(%s): %v", tc.opType, err)
			}
		})
	}
}

func TestValidatePayload_UnknownOpType(t *testing.T) {
	if err := ValidatePayload("frobnicate", []byte(`{}`)); err == nil {
		t.Fatal("want error for unknown op_type")
	}
}

func TestValidatePayload_EmptyPayload(t *testing.T) {
	if err := ValidatePayload("create", nil); err == nil {
		t.Fatal("want error for empty payload")
	}
}

func TestValidatePayload_BadJSON(t *testing.T) {
	if err := ValidatePayload("create", []byte(`{not json`)); err == nil {
		t.Fatal("want JSON parse error")
	}
}

func TestValidatePayload_StatusViaUpdateFieldRejected(t *testing.T) {
	raw := []byte(`{"field":"status","value":"open"}`)
	err := ValidatePayload("update_field", raw)
	if err == nil {
		t.Fatal("want error: status MUST go through claim/close")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("want error mentioning status, got %v", err)
	}
}

func TestValidatePayload_DispatchesToValidate(t *testing.T) {
	// Surfaces a per-type rule violation through the dispatcher.
	raw := []byte(`{"title":"t","type":"task","priority":4,"nonce":"` + validNonce + `"}`)
	if err := ValidatePayload("create", raw); err == nil {
		t.Fatal("want error: priority out of range")
	}
}
