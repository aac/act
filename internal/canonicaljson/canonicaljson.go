// Package canonicaljson implements RFC 8785-style canonical JSON used for op
// hashing and signatures.
//
// The output is minified canonical JSON:
//   - UTF-8, no BOM, no trailing newline.
//   - Object keys sorted lexicographically by their UTF-8 byte
//     representation.
//   - No insignificant whitespace: no spaces after ':' or ',', no newlines or
//     indentation.
//   - Numbers render as bare integer digits; non-integer floats are rejected
//     with ErrFloatNotAllowed.
//   - Strings use the standard JSON short escapes (\", \\, \b, \f, \n, \r,
//     \t) and \u00xx for the remaining C0/C1 control characters; non-ASCII
//     printable code points are emitted verbatim as UTF-8.
//   - Arrays preserve element ordering; empty arrays and objects render as
//     "[]" / "{}".
//   - Booleans and null render in lowercase.
package canonicaljson

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

// ErrFloatNotAllowed is returned when a non-integer float value is
// encountered. Floats with integer values (and which are not NaN or Inf) are
// permitted and emitted as integers.
var ErrFloatNotAllowed = errors.New("canonicaljson: floats not allowed")

// Marshal returns the canonical JSON encoding of v.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encode(&buf, reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encode(buf *bytes.Buffer, v reflect.Value) error {
	if !v.IsValid() {
		buf.WriteString("null")
		return nil
	}

	// Unwrap interfaces and pointers.
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		buf.WriteString(strconv.FormatInt(v.Int(), 10))
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		buf.WriteString(strconv.FormatUint(v.Uint(), 10))
		return nil

	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrFloatNotAllowed
		}
		if math.Trunc(f) != f {
			return ErrFloatNotAllowed
		}
		// Reject magnitudes that exceed int64 range; we cannot represent
		// them as a canonical integer literal exactly.
		if f > math.MaxInt64 || f < math.MinInt64 {
			return ErrFloatNotAllowed
		}
		buf.WriteString(strconv.FormatInt(int64(f), 10))
		return nil

	case reflect.String:
		encodeString(buf, v.String())
		return nil

	case reflect.Slice, reflect.Array:
		// []byte rendered as a string per encoding/json convention is not
		// applicable here; canonical op JSON does not use raw byte slices.
		// Treat all slices/arrays as JSON arrays.
		if v.Kind() == reflect.Slice && v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		n := v.Len()
		if n == 0 {
			buf.WriteString("[]")
			return nil
		}
		buf.WriteByte('[')
		for i := 0; i < n; i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, v.Index(i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	case reflect.Map:
		if v.IsNil() {
			buf.WriteString("null")
			return nil
		}
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("canonicaljson: map key type %s is not string", v.Type().Key())
		}
		keys := v.MapKeys()
		if len(keys) == 0 {
			buf.WriteString("{}")
			return nil
		}
		strKeys := make([]string, len(keys))
		for i, k := range keys {
			strKeys[i] = k.String()
		}
		// Byte-wise lexicographic sort (Go string < operator compares
		// bytes, which is byte-wise on the UTF-8 encoding).
		sort.Strings(strKeys)
		buf.WriteByte('{')
		for i, k := range strKeys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			if err := encode(buf, v.MapIndex(reflect.ValueOf(k))); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case reflect.Struct:
		return encodeStruct(buf, v)

	default:
		return fmt.Errorf("canonicaljson: unsupported kind %s", v.Kind())
	}
}

// encodeStruct emits a struct's exported fields as a JSON object with keys
// sorted lexicographically by their final JSON name. Field naming follows a
// minimal subset of encoding/json's rules: a `json:"name"` tag overrides the
// field name; `json:"-"` skips the field; `omitempty` is honored.
func encodeStruct(buf *bytes.Buffer, v reflect.Value) error {
	t := v.Type()
	type fieldEntry struct {
		name      string
		idx       int
		omitempty bool
	}
	var fields []fieldEntry
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		omitempty := false
		if tag, ok := f.Tag.Lookup("json"); ok {
			if tag == "-" {
				continue
			}
			parts := splitTag(tag)
			if parts[0] != "" {
				name = parts[0]
			}
			for _, p := range parts[1:] {
				if p == "omitempty" {
					omitempty = true
				}
			}
		}
		fields = append(fields, fieldEntry{name: name, idx: i, omitempty: omitempty})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })

	if len(fields) == 0 {
		buf.WriteString("{}")
		return nil
	}

	wrote := false
	buf.WriteByte('{')
	for _, fe := range fields {
		fv := v.Field(fe.idx)
		if fe.omitempty && isEmptyValue(fv) {
			continue
		}
		if wrote {
			buf.WriteByte(',')
		}
		encodeString(buf, fe.name)
		buf.WriteByte(':')
		if err := encode(buf, fv); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote {
		// All fields were omitted by omitempty.
		buf.Truncate(buf.Len() - 1) // drop the '{' we wrote
		buf.WriteString("{}")
		return nil
	}
	buf.WriteByte('}')
	return nil
}

func splitTag(tag string) []string {
	var out []string
	start := 0
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			out = append(out, tag[start:i])
			start = i + 1
		}
	}
	out = append(out, tag[start:])
	return out
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

const hexdigits = "0123456789abcdef"

// encodeString writes s as a canonical JSON string literal.
func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		// Handle ASCII fast path with explicit escape rules.
		c := s[i]
		if c < 0x80 {
			switch c {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				if c < 0x20 || c == 0x7f {
					buf.WriteString(`\u00`)
					buf.WriteByte(hexdigits[c>>4])
					buf.WriteByte(hexdigits[c&0xf])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		// Multi-byte UTF-8: decode to detect C1 control codes (U+0080..U+009F)
		// which must be escaped, but otherwise emit the raw UTF-8 bytes.
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8: emit the byte as a \u00xx escape so the output
			// stays valid JSON. This shouldn't occur on well-formed input.
			buf.WriteString(`\u00`)
			buf.WriteByte(hexdigits[c>>4])
			buf.WriteByte(hexdigits[c&0xf])
			i++
			continue
		}
		if r >= 0x80 && r <= 0x9f {
			buf.WriteString(`\u00`)
			buf.WriteByte(hexdigits[byte(r)>>4])
			buf.WriteByte(hexdigits[byte(r)&0xf])
		} else {
			buf.WriteString(s[i : i+size])
		}
		i += size
	}
	buf.WriteByte('"')
}
