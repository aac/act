package main

import (
	"testing"
	"time"
)

// TestParseSinceDuration covers the CLI-layer parsing for `act log
// --since`. The cli layer (RunLogOpts) takes a time.Duration directly;
// the string→duration step lives here and is what the "bad --since"
// acceptance criterion exercises.
func TestParseSinceDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"24h", 24 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"500ms", 500 * time.Millisecond, false},

		// Failure modes the help-line "(e.g. 24h, 7d, 30m)" implicitly
		// excludes.
		{"", 0, true},
		{"   ", 0, true},
		{"abc", 0, true},
		{"d", 0, true},
		{"-1h", 0, true},
		{"0", 0, true},
		// "d" + further unit is not accepted (we don't expand to a
		// full custom parser; users who want mixed units must convert).
		{"1d2h", 0, true},
	}
	for _, tc := range cases {
		got, err := parseSinceDuration(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSinceDuration(%q) = %v, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSinceDuration(%q) error = %v, want nil", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSinceDuration(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSplitCSVArg ensures the comma-splitter in main.go matches the
// behaviour the LogOptions.Types path depends on (trim, drop empties,
// nil for blank input).
func TestSplitCSVArg(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"create", []string{"create"}},
		{"create,close", []string{"create", "close"}},
		{" create , close ", []string{"create", "close"}},
		{"create,,close", []string{"create", "close"}},
	}
	for _, tc := range cases {
		got := splitCSVArg(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCSVArg(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSVArg(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
