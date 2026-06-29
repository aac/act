// Package config — curated list of "public" host patterns for the
// `act remote add-upstream` safety check (Phase 2 ticket 1b).
//
// `act remote add-upstream <url>` configures the orchestrator's
// `origin-upstream` to point at a GitHub/GitLab/whatever mirror that
// receives the orchestration plane's op-log. By default the command
// refuses an upstream whose URL matches one of the curated public-host
// patterns below, on the theory that an agent run will rarely WANT to
// push op-log churn to a publicly-readable repository. The user can
// override per-invocation with `--force-public`.
//
// This is a safety hint, not a security boundary. The list catches the
// most-obvious public-org URLs an agent might paste by mistake; an
// adversary deliberately routing op-log to a public bucket has a hundred
// other ways to do it. The curated approach (rather than a regex
// dragnet) keeps false positives low — every entry should be visibly
// "obviously public on this exact host."
//
// Pattern syntax: `filepath.Match`-style globs against the host portion
// of the URL (case-insensitive). The leading "github.com/..." entries
// pattern-match against the host PLUS the first path component, so
// `github.com/aac/public-thing` matches but `github.com/aac/private-thing`
// does not. We do that by joining host and path with `/` before the
// match — see IsPublicURL.
//
// Adding to the list: append a new pattern below and document the
// concrete URL shape it catches. Keep the list small and the patterns
// narrow; broad globs like `*.com` are explicitly out of scope.
package config

import (
	"net/url"
	"path/filepath"
	"strings"
)

// PublicHostPatterns is the curated list of host+path globs that
// `act remote add-upstream` refuses by default. Each entry is matched
// (case-insensitively) against `<host>/<first-path-component>` of the
// parsed upstream URL.
//
// Each entry is one of:
//
//   - A bare hostname pattern matched against the host alone
//     (e.g. `*.public.example.com`). The first-path-component is
//     ignored for this form.
//   - A `host/prefix-*` pattern matched against `<host>/<first-path>`
//     (e.g. `github.com/aac/public-*` catches the aac org's deliberately
//     public repos).
//
// Order is alphabetical by pattern for readability; the match loop is
// order-independent.
var PublicHostPatterns = []string{
	// Seed entries from the ticket.
	"*.public.example.com",
	"github.com/aac/public-*",
}

// IsPublicURL reports whether rawURL matches any pattern in
// PublicHostPatterns. The match is case-insensitive on the host portion
// (URLs are case-insensitive in host but case-sensitive in path; the
// curated patterns target host+well-known-prefix where the prefix
// convention is lowercased by the host's UI, so we lowercase the path
// too — over-inclusive on truly case-sensitive paths is the safer
// failure mode for a safety hint).
//
// Returns false if rawURL fails to parse — an unparseable URL surfaces
// later as a `git push` error, not here.
//
// Match strategy:
//
//   - Host-only patterns (no '/' in the pattern) match against the
//     parsed host alone. `*.public.example.com` catches every
//     subdomain.
//   - host+path patterns (contains '/') match against
//     `<host>/<path-component-1>/<path-component-2>` of the parsed URL.
//     `filepath.Match`'s `*` glob does not span '/' boundaries, so
//     `github.com/aac/public-*` matches the repo-name segment without
//     leaking across paths.
//   - SSH-form URLs (`git@host:org/repo`) are reparsed into host+path
//     before matching, so the curated globs catch the SSH idiom an
//     agent might paste alongside HTTPS.
func IsPublicURL(rawURL string) bool {
	var host, path string
	// SSH-style URLs (`git@github.com:org/repo.git`) fail url.Parse
	// with "first path segment in URL cannot contain colon" — we
	// detect them up front via the SCP-like splitter and only fall
	// through to url.Parse for proper URLs.
	if h, p := splitSCPLikeURL(rawURL); h != "" {
		host = strings.ToLower(h)
		path = strings.ToLower(p)
		// Strip a trailing `.git` for matching parity with HTTPS
		// forms — the curated globs target org/repo-prefix, not
		// `.git` suffixes the SSH form conventionally includes.
		path = strings.TrimSuffix(path, ".git")
	} else {
		u, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		host = strings.ToLower(u.Host)
		path = strings.ToLower(strings.TrimPrefix(u.Path, "/"))
	}
	if host == "" {
		return false
	}
	// Build the host+path string we match against. We include up to
	// two path components (`host/org/repo`) which is enough for every
	// pattern shape the curated list will ever hold. `filepath.Match`
	// glob `*` cannot span `/` so longer paths still match the
	// repo-segment pattern.
	joined := host
	if path != "" {
		joined = host + "/" + path
	}
	for _, pattern := range PublicHostPatterns {
		p := strings.ToLower(pattern)
		// Host-only patterns (no '/') match against host alone.
		if !strings.Contains(p, "/") {
			if ok, _ := filepath.Match(p, host); ok {
				return true
			}
			continue
		}
		// host+path patterns match against the joined host+path.
		// filepath.Match's `*` does not span '/' so the match
		// terminates at the path-component boundary the pattern
		// implies.
		if ok, _ := filepath.Match(p, joined); ok {
			return true
		}
		// Also try matching against just the first two components
		// (host/org/repo) without trailing path. This catches the
		// case where the URL has trailing path segments but the
		// pattern terminates at the repo level: pattern
		// `github.com/aac/public-*` against URL path
		// `aac/public-thing/issues/1` should still match.
		if trimmed := truncateToComponents(joined, 3); trimmed != joined {
			if ok, _ := filepath.Match(p, trimmed); ok {
				return true
			}
		}
	}
	return false
}

// truncateToComponents returns s truncated to the first n path
// components (where the host counts as the first component). Returns
// s unchanged if it has ≤ n components.
func truncateToComponents(s string, n int) string {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			count++
			if count == n {
				return s[:i]
			}
		}
	}
	return s
}

// splitSCPLikeURL splits an SCP-style URL (`git@github.com:org/repo.git`)
// into host+path. Returns "", "" if rawURL is not in SCP-like form.
//
// SCP-like form: `[user@]host:path` where path does not begin with a
// slash (distinguishes from a full URL host like `example.com:8080/...`).
func splitSCPLikeURL(rawURL string) (host, path string) {
	// Strip optional `user@` prefix.
	s := rawURL
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	// Must contain `:` separating host from path, and path must NOT
	// start with `/` (else it's a real URL with port).
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", ""
	}
	host = s[:i]
	path = s[i+1:]
	if strings.HasPrefix(path, "/") {
		// Looks like `host:/path` — that's a URL idiom, not SCP.
		return "", ""
	}
	// A host portion with no `.` and no port is likely a local
	// scp target like `mydir:somefile`; require at least one `.` in
	// the host portion to count as a git remote.
	if !strings.Contains(host, ".") {
		return "", ""
	}
	return host, path
}
