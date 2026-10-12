// act-4a538e — credentials in a remote URL must not reach act's output.
//
// A remote URL can carry userinfo (`https://user:token@host/…`). act never
// prints a remote URL on a normal write, but the text of a failed push or
// fetch is git's own stderr, and that text is returned as
// `push_deferred_reason`, printed in the not-pushed warning block, reported
// by `act doctor` and appended to `.act/.sync-log`. git masks the userinfo
// in its http(s) and ssh messages; it does not for `git://`, where the
// whole authority is echoed as the host it could not look up (measured:
// "fatal: unable to look up user:<secret>@127.0.0.1:1 (port 9418)"). MCP
// results land in agent transcripts, so one such message is a leak.
//
// The functions here are applied where act SURFACES a push/fetch/remote
// error or a remote URL — never to what is written to git config or passed
// to git, which must keep the real URL to work.
package gitops

import (
	"net/url"
	"regexp"
	"strings"
)

// redactedUserinfo replaces userinfo in surfaced text.
const redactedUserinfo = "***"

// minStandalonePassword is the shortest password RedactURLUserinfo also
// replaces where it stands alone (not followed by `@`). Shorter ones — the
// `x` of `https://<token>:x@host` — would mangle unrelated text, and in that
// shape the secret is the user part, which is caught with its `@`.
const minStandalonePassword = 6

// schemeUserinfoRE matches the userinfo of any `scheme://userinfo@` in a
// text, whether or not the URL is one act knows about.
var schemeUserinfoRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/@'"]+@`)

// RedactURLUserinfo returns text with URL credentials replaced by `***`:
//
//   - the userinfo of every `scheme://userinfo@…` in text, and
//   - for each of urls (the remote URLs the text may be about), that URL's
//     userinfo wherever it appears before an `@`, with or without a scheme,
//     and its password wherever it appears at all.
//
// The second rule is what covers git messages that echo a URL's authority
// without its scheme. A URL with no `scheme://` or no userinfo contributes
// nothing. A user name with no password is masked too: it is where a
// token goes in `https://<token>@host`.
func RedactURLUserinfo(text string, urls ...string) string {
	if text == "" {
		return text
	}
	for _, u := range urls {
		info := urlUserinfo(u)
		if info == "" {
			continue
		}
		forms := []string{info}
		if dec, err := url.PathUnescape(info); err == nil && dec != info {
			forms = append(forms, dec)
		}
		// Whole userinfo first, in every form, so a password replaced on
		// its own does not leave the user name behind.
		for _, f := range forms {
			text = strings.ReplaceAll(text, f+"@", redactedUserinfo+"@")
		}
		for _, f := range forms {
			if i := strings.IndexByte(f, ':'); i >= 0 && len(f)-i-1 >= minStandalonePassword {
				text = strings.ReplaceAll(text, f[i+1:], redactedUserinfo)
			}
		}
	}
	return schemeUserinfoRE.ReplaceAllString(text, "${1}"+redactedUserinfo+"@")
}

// urlUserinfo returns the userinfo of a `scheme://userinfo@host…` URL, or ""
// when u has no scheme or no userinfo. Parsed by hand rather than with
// net/url so a URL git accepts but net/url rejects (a non-numeric port)
// still yields its userinfo.
func urlUserinfo(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return ""
	}
	authority := u[i+3:]
	if j := strings.IndexAny(authority, "/?#"); j >= 0 {
		authority = authority[:j]
	}
	at := strings.LastIndexByte(authority, '@')
	if at <= 0 {
		return ""
	}
	return authority[:at]
}

// RedactRemoteCredentials is RedactURLUserinfo with urls set to every remote
// URL this repo's git config names: `remote.<name>.url` / `.pushurl`, and
// the rewrite targets of `url.<base>.insteadOf` / `.pushInsteadOf` (a common
// place for a token). It runs one `git config` and is meant for failure
// paths only.
func (g *GitOps) RedactRemoteCredentials(text string) string {
	if text == "" {
		return text
	}
	return RedactURLUserinfo(text, g.configuredRemoteURLs()...)
}

// RedactError returns err with RedactRemoteCredentials applied to its text.
// The returned error still unwraps to err, so errors.Is / errors.As keep
// working; err itself is returned when there is nothing to redact.
func (g *GitOps) RedactError(err error) error {
	if err == nil {
		return nil
	}
	raw := err.Error()
	clean := g.RedactRemoteCredentials(raw)
	if clean == raw {
		return err
	}
	return &redactedError{msg: clean, err: err}
}

// redactedError carries a redacted message over the error it replaces.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// configuredRemoteURLs lists the URLs described on RedactRemoteCredentials.
// Any failure (no such keys is exit 1) yields what could be read.
func (g *GitOps) configuredRemoteURLs() []string {
	out, _ := g.run("config", "--get-regexp",
		`^(remote\..*\.(url|pushurl)|url\..*\.(insteadof|pushinsteadof))$`)
	var urls []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, _ := strings.Cut(line, " ")
		if strings.HasPrefix(key, "url.") {
			// url.<base>.insteadof: the URL is the subsection.
			if j := strings.LastIndexByte(key, '.'); j > len("url.") {
				urls = append(urls, key[len("url."):j])
			}
			continue
		}
		urls = append(urls, value)
	}
	return urls
}
