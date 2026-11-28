package redaction

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/op"
)

// GenerationKey is the payload key a re-issued op carries: "redacted": N,
// N >= 1. It lives in the payload, not the envelope, so it is covered by the
// op hash with no change to op.Envelope or its hash input, and every payload
// decoder that does not look for it ignores it (docs/redaction-design.md §3).
const GenerationKey = "redacted"

// Generation returns env's redaction generation: the payload's "redacted"
// value when it is an integer >= 1, otherwise 0 (absent, not a number, not
// an integer, or below 1 all read as 0, per docs/redaction-design.md §4).
//
// This mirrors the generation read in the fold's slot rule. Both must agree
// on every input; see the package's follow-up note on consolidating with
// internal/fold once its slot helper is exported.
func Generation(env op.Envelope) int {
	var peek struct {
		Redacted json.RawMessage `json:"redacted"`
	}
	if err := json.Unmarshal(env.Payload, &peek); err != nil || len(peek.Redacted) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(string(peek.Redacted), 10, 64)
	if err != nil || n < 1 {
		return 0
	}
	return int(n)
}

// Apply replaces every pattern in env's payload text fields with
// Replacement (docs/redaction-design.md §5.2) and returns the rewritten
// envelope and the number of occurrences replaced. It does not set the
// generation marker; Reissue does.
//
// Matching runs on the decoded strings, so a pattern holding a quote, a
// newline or non-ASCII text matches whatever its JSON escaping in the file.
// Everything else in the payload is copied through: the payload is decoded
// to a generic value and re-encoded with canonicaljson, exactly the round
// trip op.Envelope.Marshal applies to every op, so an op with no hit
// re-marshals to its original bytes.
//
// The text fields are an allowlist, not "every string in the payload":
// the other strings are structure (nonce, parent, field, edge_type, ids)
// and a replacement inside one would corrupt the op. A pattern found
// anywhere else is a residual for the plan to report, never silently
// skipped.
func Apply(env op.Envelope, patterns []string) (out op.Envelope, replacements int, err error) {
	return apply(env, ordered(patterns))
}

// apply is Apply with patterns already in apply order.
func apply(env op.Envelope, patterns []string) (op.Envelope, int, error) {
	var payload any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return op.Envelope{}, 0, fmt.Errorf("redaction: decode %s payload: %w", env.OpType, err)
	}
	n := 0
	if obj, ok := payload.(map[string]any); ok {
		n = replaceTextFields(env.OpType, obj, patterns)
	}
	body, err := canonicaljson.Marshal(payload)
	if err != nil {
		return op.Envelope{}, 0, fmt.Errorf("redaction: encode %s payload: %w", env.OpType, err)
	}
	out := env
	out.Payload = json.RawMessage(body)
	return out, n, nil
}

// replaceTextFields applies patterns to the text fields of one payload, per
// the table in docs/redaction-design.md §5.2, and returns the number of
// occurrences replaced. A listed field that is absent or not a string is
// left alone. Every op type not named here has no text fields.
func replaceTextFields(opType string, obj map[string]any, patterns []string) int {
	n := 0
	switch opType {
	case "create":
		n += replaceKey(obj, "title", patterns)
		n += replaceKey(obj, "description", patterns)
		n += replaceArray(obj, "accept", patterns)
	case "update_field":
		if f, _ := obj["field"].(string); f == "title" || f == "description" {
			n += replaceKey(obj, "value", patterns)
		}
	case "add_accept":
		n += replaceKey(obj, "criterion", patterns)
	case "set_accept":
		n += replaceArray(obj, "criteria", patterns)
	case "close", "reopen", "unclaim":
		n += replaceKey(obj, "reason", patterns)
	case "add_external_dep", "remove_external_dep":
		n += replaceKey(obj, "ref", patterns)
	case "redact":
		// Only the pre-act-8d1d tombstone payload has a text field; the
		// current record holds two integers.
		n += replaceKey(obj, "new_value", patterns)
	}
	return n
}

func replaceKey(obj map[string]any, key string, patterns []string) int {
	s, ok := obj[key].(string)
	if !ok {
		return 0
	}
	s2, c := replaceString(s, patterns)
	if c > 0 {
		obj[key] = s2
	}
	return c
}

func replaceArray(obj map[string]any, key string, patterns []string) int {
	arr, ok := obj[key].([]any)
	if !ok {
		return 0
	}
	n := 0
	for i, v := range arr {
		s, ok := v.(string)
		if !ok {
			continue
		}
		s2, c := replaceString(s, patterns)
		if c > 0 {
			arr[i] = s2
			n += c
		}
	}
	return n
}

// Reissue returns env with the payload's "redacted" key set to generation
// (>= 1), replacing any earlier value. Nothing else in the envelope changes:
// op_version, schema_version, writer_version, op_type, issue_id, node_id and
// hlc are what identify the op's slot, and a re-issued op must stay in it.
//
// The new payload has a new hash, so the re-issued op takes a new file name
// (op.Filename): a file's bytes are never changed in place. The old name
// carries a hash of the old content, and with the rest of the payload public
// in the same file that hash prefix is a filter candidates for the removed
// string can be run through; it must not survive.
func Reissue(env op.Envelope, generation int) (op.Envelope, error) {
	if generation < 1 {
		return op.Envelope{}, fmt.Errorf("redaction: generation %d: must be >= 1", generation)
	}
	var payload any
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return op.Envelope{}, fmt.Errorf("redaction: decode %s payload: %w", env.OpType, err)
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return op.Envelope{}, fmt.Errorf("redaction: %s payload is not a JSON object", env.OpType)
	}
	obj[GenerationKey] = generation
	body, err := canonicaljson.Marshal(obj)
	if err != nil {
		return op.Envelope{}, fmt.Errorf("redaction: encode %s payload: %w", env.OpType, err)
	}
	out := env
	out.Payload = json.RawMessage(body)
	return out, nil
}
