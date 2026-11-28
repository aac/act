package redaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aac/act/internal/canonicaljson"
)

// Replacement is the only text a redaction ever writes over a matched
// string. It is a constant, not a caller's choice: a chosen replacement
// would make redaction an edit command again, and two checkouts redacting
// the same op with the same patterns must produce the same bytes
// (docs/redaction-design.md §3, §6.3).
const Replacement = "[redacted]"

// MinPatternLen is the shortest pattern LoadPatterns accepts, in bytes.
// Below it a pattern collides by chance with act's own random hex (a 4-digit
// pattern appears in roughly one nonce in 2,300), which the residual scan
// cannot tell from a real occurrence.
const MinPatternLen = 8

// maxApplyRounds bounds the replace-until-stable loop in replaceString. A
// pattern can contain Replacement as a substring, so one replacement can
// complete a longer pattern; a string still matching after this many rounds
// is left as it is and the residual check reports it.
const maxApplyRounds = 8

// PatternError is a rejected patterns file. It names the 1-based line and a
// fixed reason, never the line's text: the line may be the secret.
type PatternError struct {
	// Line is the 1-based line number, or 0 when the error is about the
	// file as a whole (no patterns at all).
	Line int
	// Reason is fixed text describing the rule the line broke.
	Reason string
}

func (e *PatternError) Error() string {
	if e.Line == 0 {
		return "patterns file " + e.Reason
	}
	return fmt.Sprintf("patterns file line %d: %s", e.Line, e.Reason)
}

// LoadPatterns reads a patterns file (docs/redaction-design.md §5.3): the
// whole input as UTF-8, split on "\n", one trailing "\r" stripped, leading
// and trailing spaces and tabs trimmed, empty lines dropped, duplicates
// removed. Each remaining line is one literal, case-sensitive pattern.
//
// Trimming is deliberate: an invisible trailing space would turn a real
// redaction into a silent zero-match, and a secret's trimmed core is still
// the secret. For the same reason a leading UTF-8 byte-order mark is
// dropped.
//
// A *PatternError is returned for: no patterns at all; a pattern shorter
// than MinPatternLen bytes; a pattern that occurs inside Replacement (it
// would re-match forever); a line that is not valid UTF-8 (it could never
// match a decoded JSON string).
//
// The result is in apply order: longest pattern first, ties by byte order.
func LoadPatterns(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("redaction: read patterns: %w", err)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	seen := map[string]bool{}
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		line = strings.TrimSuffix(line, "\r")
		line = strings.Trim(line, " \t")
		if line == "" {
			continue
		}
		if !utf8.ValidString(line) {
			return nil, &PatternError{Line: n, Reason: "is not valid UTF-8"}
		}
		if len(line) < MinPatternLen {
			return nil, &PatternError{Line: n, Reason: fmt.Sprintf("pattern is shorter than %d bytes; give a shorter secret together with the words written next to it", MinPatternLen)}
		}
		if strings.Contains(Replacement, line) {
			return nil, &PatternError{Line: n, Reason: "pattern occurs inside the replacement text " + Replacement}
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return nil, &PatternError{Reason: "holds no patterns"}
	}
	return ordered(out), nil
}

// ordered returns a copy of patterns in apply order: longest first, ties by
// byte order. The order is fixed so two checkouts given the same file
// produce byte-identical re-issued ops.
func ordered(patterns []string) []string {
	out := append([]string(nil), patterns...)
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// rawForms returns every byte form a pattern can take in a file
// (docs/redaction-design.md §5.3): its bytes, and its bytes as canonicaljson
// escapes them inside a JSON string, when that differs.
func rawForms(patterns []string) [][]byte {
	var forms [][]byte
	for _, p := range patterns {
		forms = append(forms, []byte(p))
		enc, err := canonicaljson.Marshal(p)
		if err != nil || len(enc) < 2 {
			continue
		}
		esc := enc[1 : len(enc)-1] // strip the surrounding quotes
		if !bytes.Equal(esc, []byte(p)) {
			forms = append(forms, esc)
		}
	}
	return forms
}

// containsAny reports whether b holds any of forms.
func containsAny(b []byte, forms [][]byte) bool {
	for _, f := range forms {
		if bytes.Contains(b, f) {
			return true
		}
	}
	return false
}

// holds reports whether the file name, with bytes b, must be treated as
// holding a pattern. It fails closed; any one of these makes it true:
//
//  1. a raw form of a pattern in the bytes;
//  2. a pattern inside any string token, keys and values alike, of b read
//     as a stream of JSON values. The walk goes token by token and builds
//     no maps, so the earlier value of a duplicate key is still read;
//  3. the file claims to be JSON (claimsJSON) and the walk cannot finish
//     it cleanly: a decode error, trailing bytes after the last value, or
//     input that ends inside an object or array. Such a file is residual
//     whatever it holds. act writes these files as canonical JSON, so one
//     that does not decode has been touched by something else, and no
//     partial reading of it is trusted. It is reported by path, never by
//     content.
//
// Checks 2 and 3 make the answer independent of how a file escapes the
// text (\u escapes, an escaped '/', escapes canonicaljson would not emit).
// A file that does not claim to be JSON (a hook script, the SQLite index)
// gets checks 1 and 2 only: JSON escaping means nothing there.
func holds(name string, b []byte, patterns []string, forms [][]byte) bool {
	if containsAny(b, forms) {
		return true
	}
	found, clean := tokensHold(b, patterns)
	if found {
		return true
	}
	return !clean && claimsJSON(name, b)
}

// claimsJSON reports whether a file presents itself as JSON: its name ends
// in .json, or its first non-whitespace byte opens a JSON object, array or
// string. An empty or all-whitespace file does not.
func claimsJSON(name string, b []byte) bool {
	if strings.HasSuffix(strings.ToLower(name), ".json") {
		return true
	}
	t := bytes.TrimLeft(b, " \t\r\n")
	return len(t) > 0 && (t[0] == '{' || t[0] == '[' || t[0] == '"')
}

// tokensHold walks b as a stream of JSON values with json.Decoder.Token and
// reports whether any string token holds a pattern. clean is true only
// when the walk reached the end of b with every object and array closed;
// a decode error, or input that ends inside a value, is not clean.
func tokensHold(b []byte, patterns []string) (found, clean bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	depth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false, depth == 0
		}
		if err != nil {
			return false, false
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if stringHolds(v, patterns) {
				return true, true
			}
		}
	}
}

func stringHolds(s string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// replaceString replaces every pattern in s with Replacement, longest
// pattern first, repeating until nothing matches or maxApplyRounds rounds
// have run. patterns must already be in apply order. It returns the new
// string and the number of occurrences replaced.
func replaceString(s string, patterns []string) (string, int) {
	total := 0
	for round := 0; round < maxApplyRounds; round++ {
		changed := false
		for _, p := range patterns {
			if c := strings.Count(s, p); c > 0 {
				s = strings.ReplaceAll(s, p, Replacement)
				total += c
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return s, total
}
