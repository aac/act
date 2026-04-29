package canonicaljson

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%#v) returned error: %v", v, err)
	}
	return string(b)
}

func TestSortedKeyInvariance(t *testing.T) {
	a := map[string]any{"b": 1, "a": 2, "c": 3}
	b := map[string]any{"c": 3, "a": 2, "b": 1}
	got1 := mustMarshal(t, a)
	got2 := mustMarshal(t, b)
	if got1 != got2 {
		t.Fatalf("expected identical output, got %q vs %q", got1, got2)
	}
	want := `{"a":2,"b":1,"c":3}`
	if got1 != want {
		t.Fatalf("got %q, want %q", got1, want)
	}
}

func TestIntegerFormatting(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{0, "0"},
		{int64(-1), "-1"},
		{int64(math.MaxInt64), "9223372036854775807"},
		{int64(math.MinInt64), "-9223372036854775808"},
		{uint64(math.MaxUint64), "18446744073709551615"},
		// Floats with integer values are accepted and emitted as integers.
		{1.0, "1"},
		{-42.0, "-42"},
	}
	for _, c := range cases {
		got := mustMarshal(t, c.in)
		if got != c.want {
			t.Errorf("Marshal(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStringEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello", "\"hello\""},
		{"\"", "\"\\\"\""},
		{"\\", "\"\\\\\""},
		{"\b\f\n\r\t", "\"\\b\\f\\n\\r\\t\""},
		{"\x00", "\"\\u0000\""},
		{"\x01", "\"\\u0001\""},
		{"\x1f", "\"\\u001f\""},
		{"\x7f", "\"\\u007f\""},   // DEL
		{"\u0085", "\"\\u0085\""}, // NEL, C1 control
		{"\u009f", "\"\\u009f\""},
		{"slash/here", "\"slash/here\""}, // forward slash NOT escaped
	}
	for _, c := range cases {
		got := mustMarshal(t, c.in)
		if got != c.want {
			t.Errorf("Marshal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUnicodePreservation(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ümlaut", "\"ümlaut\""},
		{"日本語", "\"日本語\""},
		{"emoji \U0001F600", "\"emoji \U0001F600\""},
		// U+FEFF (BOM) inside a string is non-control; emitted as raw UTF-8.
		{"\uFEFF", "\"\uFEFF\""},
	}
	for _, c := range cases {
		got := mustMarshal(t, c.in)
		if got != c.want {
			t.Errorf("Marshal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNestedObjects(t *testing.T) {
	v := map[string]any{
		"outer": map[string]any{
			"z": 1,
			"a": map[string]any{
				"y": 2,
				"x": 3,
			},
		},
		"list": []any{3, 1, 2},
	}
	got := mustMarshal(t, v)
	want := `{"list":[3,1,2],"outer":{"a":{"x":3,"y":2},"z":1}}`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestEmptyContainers(t *testing.T) {
	if got := mustMarshal(t, map[string]any{}); got != "{}" {
		t.Errorf("empty map: got %q, want {}", got)
	}
	if got := mustMarshal(t, []any{}); got != "[]" {
		t.Errorf("empty slice: got %q, want []", got)
	}
}

func TestErrorOnFloat(t *testing.T) {
	cases := []any{
		1.5,
		float64(0.1),
		float32(2.5),
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
	}
	for _, c := range cases {
		_, err := Marshal(c)
		if !errors.Is(err, ErrFloatNotAllowed) {
			t.Errorf("Marshal(%v): expected ErrFloatNotAllowed, got %v", c, err)
		}
	}
}

func TestArrayOrderingPreserved(t *testing.T) {
	v := []any{"b", "a", "c", 3, 1, 2}
	got := mustMarshal(t, v)
	want := `["b","a","c",3,1,2]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBoolAndNull(t *testing.T) {
	if got := mustMarshal(t, true); got != "true" {
		t.Errorf("got %q, want true", got)
	}
	if got := mustMarshal(t, false); got != "false" {
		t.Errorf("got %q, want false", got)
	}
	if got := mustMarshal(t, nil); got != "null" {
		t.Errorf("got %q, want null", got)
	}
	var p *int
	if got := mustMarshal(t, p); got != "null" {
		t.Errorf("nil pointer: got %q, want null", got)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	got := mustMarshal(t, map[string]any{"a": 1})
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("output has trailing newline: %q", got)
	}
}

func TestNoInsignificantWhitespace(t *testing.T) {
	got := mustMarshal(t, map[string]any{"a": 1, "b": []any{1, 2}})
	for _, bad := range []string{": ", ", ", "  ", "\t", "\n"} {
		if strings.Contains(got, bad) {
			t.Errorf("output %q contains insignificant whitespace %q", got, bad)
		}
	}
}

func TestStruct(t *testing.T) {
	type inner struct {
		Z int `json:"z"`
		A int `json:"a"`
	}
	type outer struct {
		B     int    `json:"b"`
		A     int    `json:"a"`
		Inner inner  `json:"inner"`
		Skip  string `json:"-"`
		Empty string `json:"empty,omitempty"`
	}
	v := outer{B: 2, A: 1, Inner: inner{Z: 10, A: 20}, Skip: "ignored", Empty: ""}
	got := mustMarshal(t, v)
	want := `{"a":1,"b":2,"inner":{"a":20,"z":10}}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKeySortByteWise(t *testing.T) {
	// Sorting is byte-wise on the UTF-8 encoding rather than rune-wise.
	// 'Z' (0x5A) sorts before 'a' (0x61); 'ä' (UTF-8 0xC3 0xA4) sorts
	// after any ASCII letter.
	v := map[string]any{
		"a":  1,
		"Z":  2,
		"ä":  3,
		"aa": 4,
	}
	got := mustMarshal(t, v)
	want := "{\"Z\":2,\"a\":1,\"aa\":4,\"ä\":3}"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNestedSliceOfMaps(t *testing.T) {
	v := []any{
		map[string]any{"b": 1, "a": 2},
		map[string]any{"d": 3, "c": 4},
	}
	got := mustMarshal(t, v)
	want := `[{"a":2,"b":1},{"c":4,"d":3}]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
