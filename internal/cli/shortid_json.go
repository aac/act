package cli

import "encoding/json"

// short_id is emitted ONLY when it differs from id (act-8a6536).
//
// `short_id` is the shortest unique prefix an agent or human can type. Ids
// are generated at exactly the prefix floor (`act-` + ids.MinShortHexLen hex),
// so for every id that has NOT been extended to break a collision the two
// values are byte-identical — and every row of every listing was shipping
// the same string twice. In a 200-row `act list` that is ~5 KB of pure
// duplication, re-read on every turn when the listing came back over MCP.
//
// The contract for consumers, stated in docs/spec.md and the act skill:
// **short_id absent means short_id == id.** Read it as
// `if short_id == "" { short_id = id }`, never as "this row has no short
// handle". The field reappears the moment it carries information — an
// extended id, where the prefix genuinely differs from the full id.
//
// This is deliberately implemented at the JSON boundary rather than by
// clearing the struct field: the human renderers (`act list`, `act ready`)
// print ShortID as the row handle and must keep seeing the real value.
func omitShortIDWhenSameAsID(id, short string) string {
	if short == id {
		return ""
	}
	return short
}

// MarshalJSON applies the omit-when-identical rule to a listing row. The
// alias type breaks the recursion into the default struct marshaler.
func (l ListedIssue) MarshalJSON() ([]byte, error) {
	type alias ListedIssue
	a := alias(l)
	a.ShortID = omitShortIDWhenSameAsID(l.ID, l.ShortID)
	return json.Marshal(a)
}

// MarshalJSON applies the omit-when-identical rule to a ready-set row.
func (r ReadyIssue) MarshalJSON() ([]byte, error) {
	type alias ReadyIssue
	a := alias(r)
	a.ShortID = omitShortIDWhenSameAsID(r.ID, r.ShortID)
	return json.Marshal(a)
}

// MarshalJSON applies the omit-when-identical rule to a search hit.
func (s SearchMatch) MarshalJSON() ([]byte, error) {
	type alias SearchMatch
	a := alias(s)
	a.ShortID = omitShortIDWhenSameAsID(s.ID, s.ShortID)
	return json.Marshal(a)
}

// The write envelopes carry one id each, not a listing's worth — they are
// covered by the same rule so `short_id` means one thing everywhere it
// appears, not "omitted in reads, duplicated in writes".

// MarshalJSON applies the omit-when-identical rule to a close envelope.
func (c CloseResult) MarshalJSON() ([]byte, error) {
	type alias CloseResult
	a := alias(c)
	a.ShortID = omitShortIDWhenSameAsID(c.ID, c.ShortID)
	return json.Marshal(a)
}

// MarshalJSON applies the omit-when-identical rule to a delete envelope.
func (d DeleteResult) MarshalJSON() ([]byte, error) {
	type alias DeleteResult
	a := alias(d)
	a.ShortID = omitShortIDWhenSameAsID(d.ID, d.ShortID)
	return json.Marshal(a)
}

// MarshalJSON applies the omit-when-identical rule to a reopen envelope.
func (r ReopenResult) MarshalJSON() ([]byte, error) {
	type alias ReopenResult
	a := alias(r)
	a.ShortID = omitShortIDWhenSameAsID(r.ID, r.ShortID)
	return json.Marshal(a)
}
