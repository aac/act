package redaction

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLoadPatterns_TrimsSkipsBlanksAndDedupes(t *testing.T) {
	in := "\xef\xbb\xbf  " + redSecret + " \r\n" + // BOM, spaces, CRLF
		"\n" +
		"\t \t\r\n" + // whitespace-only: dropped
		redSecret + "\n" + // duplicate
		"\tshort-ish1\t\n" +
		"zz-same-len\n" +
		"aa-same-len" // no trailing newline
	got, err := LoadPatterns(strings.NewReader(in))
	if err != nil {
		t.Fatalf("LoadPatterns: %v", err)
	}
	// Apply order: longest first, ties by byte order.
	want := []string{redSecret, "aa-same-len", "zz-same-len", "short-ish1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadPatterns = %q, want %q", got, want)
	}
}

func TestLoadPatterns_RejectsShortPatternByLineNumber(t *testing.T) {
	const short = "abc1234" // 7 bytes
	in := "a-long-enough-pattern\n\n   " + short + "  \n"
	_, err := LoadPatterns(strings.NewReader(in))
	var pe *PatternError
	if !errors.As(err, &pe) {
		t.Fatalf("LoadPatterns err = %v, want *PatternError", err)
	}
	if pe.Line != 3 {
		t.Errorf("PatternError.Line = %d, want 3", pe.Line)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q does not name line 3", err)
	}
	if strings.Contains(err.Error(), short) {
		t.Errorf("error %q contains the pattern text", err)
	}
	// Exactly MinPatternLen bytes is accepted; one fewer is not.
	if _, err := LoadPatterns(strings.NewReader("12345678")); err != nil {
		t.Errorf("8-byte pattern: %v, want accepted", err)
	}
	if _, err := LoadPatterns(strings.NewReader("1234567")); err == nil {
		t.Errorf("7-byte pattern: accepted, want rejected")
	}
	// Bytes, not runes: four two-byte runes are 8 bytes.
	if _, err := LoadPatterns(strings.NewReader("éééé")); err != nil {
		t.Errorf("8-byte non-ASCII pattern: %v, want accepted", err)
	}
}

func TestLoadPatterns_RejectsPatternInsideReplacement(t *testing.T) {
	for _, p := range []string{"redacted", "[redacted", "redacted]", "[redacted]"} {
		_, err := LoadPatterns(strings.NewReader("a-long-enough-pattern\n" + p + "\n"))
		var pe *PatternError
		if !errors.As(err, &pe) || pe.Line != 2 {
			t.Errorf("pattern %q: err = %v, want *PatternError on line 2", p, err)
		}
	}
	// A longer pattern that merely contains the replacement is fine: it
	// cannot re-match once replaced.
	if _, err := LoadPatterns(strings.NewReader("x[redacted]x")); err != nil {
		t.Errorf("pattern containing the replacement: %v, want accepted", err)
	}
}

func TestLoadPatterns_RejectsEmptyAndNonUTF8(t *testing.T) {
	for _, in := range []string{"", "\n\n", "  \t \r\n"} {
		_, err := LoadPatterns(strings.NewReader(in))
		var pe *PatternError
		if !errors.As(err, &pe) || pe.Line != 0 {
			t.Errorf("input %q: err = %v, want *PatternError with Line 0", in, err)
		}
	}
	bad := "a-long-enough-pattern\nabc\xff\xfedefgh\n"
	_, err := LoadPatterns(strings.NewReader(bad))
	var pe *PatternError
	if !errors.As(err, &pe) || pe.Line != 2 {
		t.Fatalf("non-UTF-8 line: err = %v, want *PatternError on line 2", err)
	}
	if strings.Contains(err.Error(), "abc") {
		t.Errorf("error %q contains the line's text", err)
	}
}

func TestRawForms_IncludesCanonicalEscape(t *testing.T) {
	forms := rawForms([]string{`a "quoted" é\x`, redSecret})
	want := [][]byte{[]byte(`a "quoted" é\x`), []byte(`a \"quoted\" é\\x`), []byte(redSecret)}
	if len(forms) != len(want) {
		t.Fatalf("rawForms = %q, want %q", forms, want)
	}
	for i := range want {
		if !bytes.Equal(forms[i], want[i]) {
			t.Errorf("rawForms[%d] = %q, want %q", i, forms[i], want[i])
		}
	}
}

func TestReplaceString_RepeatsUntilStable(t *testing.T) {
	// Replacing the inner secret completes the longer pattern, which a
	// second round then replaces.
	patterns := ordered([]string{"SECRET-12", "ab[redacted]cd"})
	got, n := replaceString("xx abSECRET-12cd yy", patterns)
	if got != "xx [redacted] yy" || n != 2 {
		t.Fatalf("replaceString = %q, %d; want %q, 2", got, n, "xx [redacted] yy")
	}
}
