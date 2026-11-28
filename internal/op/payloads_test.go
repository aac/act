package op

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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
	p := CreatePayload{Title: strings.Repeat("x", MaxTitleLen+1), Type: "task", Nonce: validNonce}
	if err := p.Validate(); err == nil {
		t.Fatal("want error")
	}
}

// The op-layer cap is the CLI's 256, not the old 200 (act-65b0ec).
func TestCreatePayload_Validate_TitleAtCap(t *testing.T) {
	if MaxTitleLen != 256 {
		t.Fatalf("MaxTitleLen = %d, want 256 (docs/spec.md)", MaxTitleLen)
	}
	p := CreatePayload{Title: strings.Repeat("x", MaxTitleLen), Type: "task", Nonce: validNonce}
	if err := p.Validate(); err != nil {
		t.Fatalf("256-byte title rejected: %v", err)
	}
}

func TestUpdateFieldPayload_Validate_TitleCap(t *testing.T) {
	at := UpdateFieldPayload{Field: "title", Value: []byte(`"` + strings.Repeat("x", MaxTitleLen) + `"`)}
	if err := at.Validate(); err != nil {
		t.Fatalf("at cap: %v", err)
	}
	over := UpdateFieldPayload{Field: "title", Value: []byte(`"` + strings.Repeat("x", MaxTitleLen+1) + `"`)}
	if err := over.Validate(); err == nil {
		t.Fatal("over cap: want error")
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

// status=open and status=blocked are now allowed via update_field.
// Only status=closed and status=in_progress are forbidden (§5.A.4).
func TestUpdateFieldPayload_Validate_StatusOpenAllowed(t *testing.T) {
	p := UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"open"`)}
	if err := p.Validate(); err != nil {
		t.Fatalf("status=open should be allowed: %v", err)
	}
}

func TestUpdateFieldPayload_Validate_StatusBlockedAllowed(t *testing.T) {
	p := UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"blocked"`)}
	if err := p.Validate(); err != nil {
		t.Fatalf("status=blocked should be allowed: %v", err)
	}
}

func TestUpdateFieldPayload_Validate_StatusClosedRejected(t *testing.T) {
	p := UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"closed"`)}
	err := p.Validate()
	if err == nil {
		t.Fatal("want error for status=closed via update_field")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("want error mentioning status, got %v", err)
	}
}

func TestUpdateFieldPayload_Validate_StatusInProgressRejected(t *testing.T) {
	p := UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"in_progress"`)}
	err := p.Validate()
	if err == nil {
		t.Fatal("want error for status=in_progress via update_field")
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

// AddExternalDepPayload ------------------------------------------------------

func TestAddExternalDepPayload_Validate_OK(t *testing.T) {
	p := AddExternalDepPayload{Ref: "linear:ENG-123"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAddExternalDepPayload_Validate_RefEmpty(t *testing.T) {
	p := AddExternalDepPayload{Ref: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for empty ref")
	}
}

func TestAddExternalDepPayload_Validate_RefTooLong(t *testing.T) {
	p := AddExternalDepPayload{Ref: string(make([]byte, MaxExternalRefLen+1))}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for over-cap ref")
	}
}

func TestAddExternalDepPayload_Validate_RefControlChar(t *testing.T) {
	p := AddExternalDepPayload{Ref: "bad\x01ref"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for control char in ref")
	}
}

func TestAddExternalDepPayload_Validate_RefNewline(t *testing.T) {
	p := AddExternalDepPayload{Ref: "bad\nref"}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for newline in ref")
	}
}

// RemoveExternalDepPayload ---------------------------------------------------

func TestRemoveExternalDepPayload_Validate_OK(t *testing.T) {
	p := RemoveExternalDepPayload{Ref: "linear:ENG-123"}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRemoveExternalDepPayload_Validate_RefEmpty(t *testing.T) {
	p := RemoveExternalDepPayload{Ref: ""}
	if err := p.Validate(); err == nil {
		t.Fatal("want error for empty ref")
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

// RedactPayload --------------------------------------------------------------

func TestRedactPayload_Validate(t *testing.T) {
	for _, tc := range []struct {
		p     RedactPayload
		valid bool
	}{
		{RedactPayload{OpsRewritten: 1, Replacements: 1}, true},
		{RedactPayload{OpsRewritten: 2, Replacements: 3}, true},
		{RedactPayload{OpsRewritten: 0, Replacements: 1}, false},
		{RedactPayload{OpsRewritten: 1, Replacements: 0}, false},
		{RedactPayload{OpsRewritten: -1, Replacements: 5}, false},
	} {
		if err := tc.p.Validate(); (err == nil) != tc.valid {
			t.Errorf("%+v.Validate() = %v, want valid=%v", tc.p, err, tc.valid)
		}
	}
}

// TestDocClaim_RedactRecord_PayloadIsTwoCountsAtLeastOne pins the spec's
// "Op type payloads" entry for the redact record: two integers, both >= 1.
// It goes through ValidatePayload, the dispatcher every write path calls,
// not the struct method alone, and checks that the pre-act-8d1d shape the
// same entry describes as read-only is refused for writing.
func TestDocClaim_RedactRecord_PayloadIsTwoCountsAtLeastOne(t *testing.T) {
	if err := ValidatePayload("redact", []byte(`{"ops_rewritten":1,"replacements":1}`)); err != nil {
		t.Fatalf("ValidatePayload(redact, 1/1) = %v, want nil", err)
	}
	for _, bad := range []string{
		`{"ops_rewritten":0,"replacements":1}`,
		`{"ops_rewritten":1,"replacements":0}`,
		`{"ops_rewritten":1}`,
		`{"ops_rewritten":"1","replacements":1}`,
		`{"field_path":"description","new_value":"<redacted>"}`,
	} {
		if err := ValidatePayload("redact", []byte(bad)); err == nil {
			t.Errorf("ValidatePayload(redact, %s) = nil, want an error", bad)
		}
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
		{"add_external_dep", `{"ref":"linear:ENG-123"}`},
		{"remove_external_dep", `{"ref":"linear:ENG-123"}`},
		{"add_accept", `{"criterion":"works"}`},
		{"remove_accept", `{"index":0}`},
		{"set_accept", `{"criteria":["a","b"]}`},
		{"claim", `{"assignee":"alice"}`},
		{"unclaim", `{"reason":"cannot finish"}`},
		{"close", `{"reason":"done"}`},
		{"reopen", `{"reason":"regressed"}`},
		{"import", `{"source_ref":"github:owner/repo#1"}`},
		{"migrate", `{"from_version":1,"to_version":2}`},
		{"tombstone", `{"deleted_at":"2026-04-29T12:00:00Z"}`},
		{"redact", `{"ops_rewritten":2,"replacements":3}`},
	}
	// Every op type has a validator, "redact" included: redaction writes a
	// record op again (docs/redaction-design.md §5.5), so the count is the
	// whole of ValidOpTypes and a missing case for any type fails here.
	if len(cases) != len(ValidOpTypes) {
		t.Fatalf("dispatcher smoke test covers %d types, want %d (every type in ValidOpTypes)",
			len(cases), len(ValidOpTypes))
	}
	// The pre-act-8d1d redact payload is still accepted on read (nothing
	// validates on read) but must never pass the write-time validator, so
	// no writer can emit the removed tombstone shape again.
	for _, legacy := range []string{
		`{"field_path":"x"}`,
		`{"field_path":"description","new_value":"<redacted>"}`,
	} {
		if err := ValidatePayload("redact", []byte(legacy)); err == nil {
			t.Fatalf("ValidatePayload(redact, %s): want error (pre-act-8d1d payload)", legacy)
		}
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

func TestValidatePayload_StatusClosedViaUpdateFieldRejected(t *testing.T) {
	raw := []byte(`{"field":"status","value":"closed"}`)
	err := ValidatePayload("update_field", raw)
	if err == nil {
		t.Fatal("want error: status=closed MUST go through close")
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

// TestDocClaim_LengthCaps_ByteBoundary pins docs/spec.md "Length caps": each
// write-time length cap sits exactly at the number the spec names, and each
// counts bytes, not characters. The caps are literals on purpose — this test
// is the spec's side of the contract, so changing a constant without the
// spec fails here (act-1d044c).
//
// For every field: a value of exactly cap bytes is accepted and cap+1 bytes
// is rejected. For fields that accept non-ASCII, cap/2 copies of a two-byte
// character (exactly cap bytes) is accepted, and one more ASCII byte — cap+1
// bytes but only cap/2+1 characters — is rejected, which a character count
// would have let through.
func TestDocClaim_LengthCaps_ByteBoundary(t *testing.T) {
	cases := []struct {
		name      string
		cap       int
		multiByte bool // the field accepts non-ASCII (machine labels do not)
		validate  func(s string) error
	}{
		{"create.title", 256, true, func(s string) error {
			return CreatePayload{Title: s, Type: "task", Nonce: validNonce}.Validate()
		}},
		{"create.description", 1048576, true, func(s string) error {
			return CreatePayload{Title: "t", Type: "task", Description: s, Nonce: validNonce}.Validate()
		}},
		{"update_field.description", 1048576, true, func(s string) error {
			// Through the ValidatePayload dispatcher on the wire bytes,
			// the path `act import` and every CLI/MCP write run.
			val, err := json.Marshal(s)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(UpdateFieldPayload{Field: "description", Value: val})
			if err != nil {
				return err
			}
			return ValidatePayload("update_field", raw)
		}},
		{"create.accept", 500, true, func(s string) error {
			return CreatePayload{Title: "t", Type: "task", Accept: []string{s}, Nonce: validNonce}.Validate()
		}},
		{"add_accept.criterion", 500, true, func(s string) error {
			return AddAcceptPayload{Criterion: s}.Validate()
		}},
		{"set_accept.criteria", 500, true, func(s string) error {
			return SetAcceptPayload{Criteria: []string{s}}.Validate()
		}},
		{"close.reason", 500, true, func(s string) error { return ClosePayload{Reason: s}.Validate() }},
		{"reopen.reason", 500, true, func(s string) error { return ReopenPayload{Reason: s}.Validate() }},
		{"unclaim.reason", 500, true, func(s string) error { return UnclaimPayload{Reason: s}.Validate() }},
		{"add_external_dep.ref", 256, true, func(s string) error { return AddExternalDepPayload{Ref: s}.Validate() }},
		{"remove_external_dep.ref", 256, true, func(s string) error { return RemoveExternalDepPayload{Ref: s}.Validate() }},
		{"machine", 64, false, func(s string) error { return ValidateMachineLabel("create.machine", s) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.validate(strings.Repeat("x", c.cap)); err != nil {
				t.Errorf("%d bytes rejected, want accepted: %v", c.cap, err)
			}
			wantMsg := fmt.Sprintf("length %d > %d bytes", c.cap+1, c.cap)
			if err := c.validate(strings.Repeat("x", c.cap+1)); err == nil {
				t.Errorf("%d bytes accepted, want rejected", c.cap+1)
			} else if !strings.Contains(err.Error(), wantMsg) {
				t.Errorf("error %q does not name %q", err, wantMsg)
			}
			if !c.multiByte {
				return
			}
			atCap := strings.Repeat("\u00e9", c.cap/2) // two bytes each
			if len(atCap) != c.cap {
				t.Fatalf("fixture is %d bytes, want %d", len(atCap), c.cap)
			}
			if err := c.validate(atCap); err != nil {
				t.Errorf("%d two-byte characters (%d bytes) rejected, want accepted: %v", c.cap/2, c.cap, err)
			}
			over := atCap + "x"
			if chars := utf8.RuneCountInString(over); chars > c.cap {
				t.Fatalf("fixture has %d characters; it must stay under %d to prove a byte count", chars, c.cap)
			}
			if err := c.validate(over); err == nil {
				t.Errorf("%d bytes in %d characters accepted, want rejected: the cap must count bytes",
					len(over), utf8.RuneCountInString(over))
			} else if !strings.Contains(err.Error(), wantMsg) {
				t.Errorf("error %q does not name %q", err, wantMsg)
			}
		})
	}
}
