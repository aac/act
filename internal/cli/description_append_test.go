package cli

import (
	"strings"
	"testing"
)

// appendDescription is the separator policy behind `act update
// --description-append` (act-a79d66). The behaviour tests live at the
// subprocess boundary in cmd/act; this covers the whitespace edge cases
// cheaply, since they are what make successive appends read uniformly.
func TestAppendDescription(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		addition string
		want     string
	}{
		{
			name:     "empty existing yields the addition alone",
			existing: "",
			addition: "note",
			want:     "note",
		},
		{
			name:     "whitespace-only existing yields the addition alone",
			existing: "  \n\n ",
			addition: "note",
			want:     "note",
		},
		{
			name:     "one blank line between body and addition",
			existing: "body",
			addition: "note",
			want:     "body\n\nnote",
		},
		{
			// A body that already ends in a newline must not produce a
			// wider gap than one that doesn't: the separator is uniform
			// regardless of how the previous author terminated their text.
			name:     "trailing newline does not widen the gap",
			existing: "body\n",
			addition: "note",
			want:     "body\n\nnote",
		},
		{
			name:     "multiple trailing newlines collapse to one blank line",
			existing: "body\n\n\n",
			addition: "note",
			want:     "body\n\nnote",
		},
		{
			// Interior blank lines are the author's paragraph structure
			// and must survive untouched; only the tail is normalised.
			name:     "interior blank lines are preserved",
			existing: "para one\n\npara two",
			addition: "note",
			want:     "para one\n\npara two\n\nnote",
		},
		{
			name:     "multi-line addition is inserted verbatim",
			existing: "body",
			addition: "line one\nline two",
			want:     "body\n\nline one\nline two",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appendDescription(tc.existing, tc.addition); got != tc.want {
				t.Errorf("appendDescription(%q, %q) = %q, want %q",
					tc.existing, tc.addition, got, tc.want)
			}
		})
	}
}

// TestFormatListTruncationNotice covers the notice's two states (act-b50d81).
// The empty-string-when-not-truncated case is what keeps a normal listing's
// stderr clean; without it every call site would need its own guard.
func TestFormatListTruncationNotice(t *testing.T) {
	if got := FormatListTruncationNotice(ListResult{Count: 5, Total: 5, Truncated: false}); got != "" {
		t.Errorf("uncapped listing produced a notice: %q", got)
	}

	got := FormatListTruncationNotice(ListResult{Count: 200, Total: 268, Truncated: true})
	for _, want := range []string{"WARNING", "showing 200 of 268", "68 hidden", "--limit 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q; got: %s", want, got)
		}
	}
}
