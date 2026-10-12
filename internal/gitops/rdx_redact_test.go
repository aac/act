package gitops

import (
	"errors"
	"strings"
	"testing"
)

// TestRdxRedactURLUserinfo pins the two rules of RedactURLUserinfo. The
// message texts are the ones git printed for these URLs (act-4a538e).
func TestRdxRedactURLUserinfo(t *testing.T) {
	const secret = "s3cretTOKEN"
	for _, tc := range []struct {
		name, text, want string
		urls             []string
	}{
		{
			name: "scheme URL in text, no known URLs",
			text: "clone https://user:" + secret + "@127.0.0.1:1/x.git: exit status 128",
			want: "clone https://***@127.0.0.1:1/x.git: exit status 128",
		},
		{
			name: "user-only userinfo is masked too",
			text: "url git://" + secret + "@127.0.0.1:1/x.git",
			want: "url git://***@127.0.0.1:1/x.git",
		},
		{
			name: "authority echoed without its scheme",
			text: "fatal: unable to look up user:" + secret + "@127.0.0.1:1 (port 9418)",
			urls: []string{"git://user:" + secret + "@127.0.0.1:1/x.git"},
			want: "fatal: unable to look up ***@127.0.0.1:1 (port 9418)",
		},
		{
			name: "user-only authority echoed without its scheme",
			text: "fatal: unable to look up " + secret + "@127.0.0.1 (port 1)",
			urls: []string{"git://" + secret + "@127.0.0.1:1/x.git"},
			want: "fatal: unable to look up ***@127.0.0.1 (port 1)",
		},
		{
			name: "password standing alone",
			text: "remote: bad password " + secret + " for user",
			urls: []string{"https://user:" + secret + "@host/x.git"},
			want: "remote: bad password *** for user",
		},
		{
			name: "percent-encoded userinfo echoed decoded",
			text: "fatal: unable to look up us er:" + secret + "@host",
			urls: []string{"git://us%20er:" + secret + "@host/x.git"},
			want: "fatal: unable to look up ***@host",
		},
		{
			name: "short password is not replaced where it stands alone",
			text: "x marks the spot; tok:x@host",
			urls: []string{"https://tok:x@host/x.git"},
			want: "x marks the spot; ***@host",
		},
		{
			name: "no userinfo anywhere is left alone",
			text: "fatal: unable to access 'https://127.0.0.1:1/x.git/': mail admin@example.com",
			urls: []string{"https://127.0.0.1:1/x.git", "git@github.com:aac/act.git"},
			want: "fatal: unable to access 'https://127.0.0.1:1/x.git/': mail admin@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURLUserinfo(tc.text, tc.urls...)
			if got != tc.want {
				t.Errorf("RedactURLUserinfo(%q, %q)\n got %q\nwant %q", tc.text, tc.urls, got, tc.want)
			}
			if strings.Contains(got, secret) {
				t.Errorf("result still carries the secret: %q", got)
			}
		})
	}
}

// TestRdxRedactErrorKeepsTheChain: a redacted error still answers errors.Is
// for what it wraps, and an error with nothing to redact is returned as is.
func TestRdxRedactErrorKeepsTheChain(t *testing.T) {
	g := NewActGitOps(t.TempDir()) // no repo: no configured URLs to read
	base := errors.New("rdx base")
	leaky := errors.Join(base, errors.New("push to ssh://user:s3cretTOKEN@host/x.git failed"))
	red := g.RedactError(leaky)
	if strings.Contains(red.Error(), "s3cretTOKEN") {
		t.Errorf("RedactError left the credential in: %q", red.Error())
	}
	if !errors.Is(red, base) {
		t.Errorf("RedactError broke the error chain: errors.Is(red, base) = false")
	}
	if got := g.RedactError(base); got != base {
		t.Errorf("RedactError on a clean error returned %v, want the same error", got)
	}
	if g.RedactError(nil) != nil {
		t.Errorf("RedactError(nil) != nil")
	}
}
