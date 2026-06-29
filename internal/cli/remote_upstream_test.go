package cli

// Tests for `act remote add-upstream` (Phase 2 ticket 1b).
//
// Each acceptance criterion maps to one or more focused tests below:
//
//   - Private URL: success → config + initial push lands on bare-remote
//     fixture (TestRemoteAddUpstream_PrivateURL_Succeeds).
//   - Public URL without override: exit 2, envelope upstream_public,
//     stderr literal (TestRemoteAddUpstream_PublicURL_RefusesWithStderr).
//   - Public URL with --force-public: success, push lands
//     (TestRemoteAddUpstream_PublicURL_WithForcePublic_Succeeds).
//   - Pattern matching: table-driven over the curated seed patterns
//     (TestRemoteAddUpstream_PatternMatching).
//
// The fixture wires the orchestrator's `.act/.git` to a real
// BareRemote (testfixtures.NewBareRemote) so the initial push goes to
// a hermetic local-filesystem bare repo. We do NOT use a "public" URL
// per se for the private-URL path — testfixtures BareRemote URLs are
// local paths that don't match any curated pattern, so they take the
// private path naturally.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/testfixtures"
)

// addUpstreamFixture builds the standard `add-upstream` setup: a host
// repo with `act init` and `act remote enable`, then seeds the
// nested `.act/.git` with one commit so the initial push has a ref
// to send. Returns (hostRoot).
func addUpstreamFixture(t *testing.T) string {
	t.Helper()
	host := newRemoteFixture(t)
	if _, code := RunRemote(RemoteOptions{Verb: "enable", SourceCWD: host}); code != 0 {
		t.Fatalf("remote enable: code=%d", code)
	}
	// seedActGitForSync lives in remote_sync_test.go and gives us a
	// nested .act/.git with identity + at least one commit on main.
	seedActGitForSync(t, host)
	return host
}

// TestRemoteAddUpstream_PrivateURL_Succeeds covers AC #1.
// Adding a private (non-pattern-matching) URL writes the config and
// the initial push lands on the upstream bare-repo fixture.
func TestRemoteAddUpstream_PrivateURL_Succeeds(t *testing.T) {
	host := addUpstreamFixture(t)
	upstream := testfixtures.NewBareRemote(t)
	// Delete the seeded main ref so our initial push lands as a
	// fast-forward (matches the production "fresh GitHub mirror" shape).
	mustExecSync(t, "git", "--git-dir="+upstream.Path, "update-ref", "-d", "refs/heads/main")

	out, code := RunRemoteAddUpstream(RemoteAddUpstreamOptions{
		URL:       upstream.URL,
		SourceCWD: host,
	})
	if code != 0 {
		t.Fatalf("add-upstream (private): code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteAddUpstreamResult)
	if !ok {
		t.Fatalf("add-upstream (private): unexpected output type %T", out)
	}
	if res.URL != upstream.URL {
		t.Errorf("result URL=%q, want %q", res.URL, upstream.URL)
	}
	if res.ForcePublic {
		t.Errorf("result ForcePublic=true on private URL")
	}

	// Config: remote.origin-upstream.url MUST equal the supplied URL.
	configPath := filepath.Join(host, ".act", ".git", "config")
	val, getCode := gitConfigGet(t, configPath, "remote.origin-upstream.url")
	if getCode != 0 {
		t.Fatalf("git config --get remote.origin-upstream.url: exit=%d", getCode)
	}
	if val != upstream.URL {
		t.Errorf("remote.origin-upstream.url=%q, want %q", val, upstream.URL)
	}

	// Push landed: upstream's main ref MUST now equal the
	// orchestrator's local main ref.
	gitDir := filepath.Join(host, ".act", ".git")
	localRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+gitDir, "rev-parse", "refs/heads/main"))
	upstreamRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+upstream.Path, "rev-parse", "refs/heads/main"))
	if upstreamRef != localRef {
		t.Errorf("upstream main ref %s != local main %s after add-upstream", upstreamRef, localRef)
	}
}

// TestRemoteAddUpstream_PublicURL_RefusesWithStderr covers AC #2.
// A pattern-matching URL without --force-public exits 2 with the
// envelope and the canonical stderr literal.
func TestRemoteAddUpstream_PublicURL_RefusesWithStderr(t *testing.T) {
	host := addUpstreamFixture(t)
	// Use a curated seed pattern's literal shape.
	publicURL := "https://github.com/aac/public-thing"

	out, code := RunRemoteAddUpstream(RemoteAddUpstreamOptions{
		URL:       publicURL,
		SourceCWD: host,
	})
	if code != 2 {
		t.Errorf("add-upstream (public): code=%d, want 2", code)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("add-upstream (public): unexpected output type %T", out)
	}
	if m["error"] != ErrUpstreamPublic {
		t.Errorf("add-upstream (public): error=%v, want %s", m["error"], ErrUpstreamPublic)
	}
	const wantMsg = "refusing public upstream; pass --force-public to override"
	if msg, _ := m["message"].(string); msg != wantMsg {
		t.Errorf("add-upstream (public): message=%q, want %q", msg, wantMsg)
	}

	// State-preservation: refusal MUST NOT have written the config key.
	configPath := filepath.Join(host, ".act", ".git", "config")
	val, _ := config.GetGitConfig(configPath, "remote.origin-upstream.url")
	if val != "" {
		t.Errorf("refusal leaked config write: remote.origin-upstream.url=%q (want empty)", val)
	}
}

// TestRemoteAddUpstream_PublicURL_WithForcePublic_Succeeds covers AC #3.
// `--force-public` lets the same public-pattern URL through and the
// push lands.
func TestRemoteAddUpstream_PublicURL_WithForcePublic_Succeeds(t *testing.T) {
	host := addUpstreamFixture(t)
	// For the success path we need a URL that BOTH classifies as
	// public AND points at something we can actually push to. We
	// satisfy that by:
	//
	//   1. Inserting a synthetic host pattern (`acttest.local`) into
	//      PublicHostPatterns for the duration of this test.
	//   2. Building a synthetic URL on that host that classifies as
	//      public.
	//   3. Installing a per-orchestrator `url.<bare>.insteadOf`
	//      rewrite so `git push` redirects from the synthetic URL to
	//      the local BareRemote fixture path.
	upstream := testfixtures.NewBareRemote(t)
	mustExecSync(t, "git", "--git-dir="+upstream.Path, "update-ref", "-d", "refs/heads/main")

	const synthHost = "acttest.local"
	pubURL := "https://" + synthHost + "/anything/repo"

	// Splice a host-only pattern matching the synthetic host into
	// PublicHostPatterns, then restore on cleanup.
	orig := append([]string(nil), config.PublicHostPatterns...)
	config.PublicHostPatterns = append(config.PublicHostPatterns, synthHost)
	t.Cleanup(func() {
		config.PublicHostPatterns = orig
	})

	// Sanity check: the URL is now classified as public.
	if !config.IsPublicURL(pubURL) {
		t.Fatalf("test setup error: %q not classified as public", pubURL)
	}

	// Install a per-clone git `url.<bare>.insteadOf "https://acttest.local"`
	// rewrite so `git push` against the synthetic URL is redirected to
	// the local bare repo path.
	configPath := filepath.Join(host, ".act", ".git", "config")
	mustExecSync(t, "git", "config", "-f", configPath,
		"url."+upstream.URL+".insteadOf", pubURL)

	out, code := RunRemoteAddUpstream(RemoteAddUpstreamOptions{
		URL:         pubURL,
		ForcePublic: true,
		SourceCWD:   host,
	})
	if code != 0 {
		t.Fatalf("add-upstream (public + force): code=%d out=%v", code, out)
	}
	res, ok := out.(RemoteAddUpstreamResult)
	if !ok {
		t.Fatalf("add-upstream (public + force): unexpected output type %T", out)
	}
	if !res.ForcePublic {
		t.Errorf("result ForcePublic=false; want true")
	}

	// Push landed on the bare-remote fixture (via the insteadOf
	// rewrite): upstream main ref MUST equal local main ref.
	gitDir := filepath.Join(host, ".act", ".git")
	localRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+gitDir, "rev-parse", "refs/heads/main"))
	upstreamRef := strings.TrimSpace(mustExecSync(t, "git", "--git-dir="+upstream.Path, "rev-parse", "refs/heads/main"))
	if upstreamRef != localRef {
		t.Errorf("upstream main ref %s != local main %s after add-upstream --force-public", upstreamRef, localRef)
	}
}

// TestRemoteAddUpstream_MissingURL covers the bad-input path.
func TestRemoteAddUpstream_MissingURL(t *testing.T) {
	host := addUpstreamFixture(t)
	out, code := RunRemoteAddUpstream(RemoteAddUpstreamOptions{
		SourceCWD: host,
	})
	if code != 2 {
		t.Errorf("add-upstream (no URL): code=%d, want 2", code)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if m["error"] != ErrBadFlag {
		t.Errorf("add-upstream (no URL): error=%v, want %s", m["error"], ErrBadFlag)
	}
}

// TestRemoteAddUpstream_PatternMatching is a table-driven assertion
// across the seed patterns. Drives config.IsPublicURL directly — the
// boundary the curated pattern list claims to enforce. Each row pairs
// a URL with the expected classification.
func TestRemoteAddUpstream_PatternMatching(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		public bool
	}{
		{
			name:   "github_aac_public_match",
			url:    "https://github.com/aac/public-thing",
			public: true,
		},
		{
			name:   "github_aac_public_match_with_suffix",
			url:    "https://github.com/aac/public-other-repo",
			public: true,
		},
		{
			name:   "github_aac_private_no_match",
			url:    "https://github.com/aac/private-thing",
			public: false,
		},
		{
			name:   "github_other_org_no_match",
			url:    "https://github.com/other/public-thing",
			public: false,
		},
		{
			name:   "public_example_subdomain_match",
			url:    "https://foo.public.example.com/anything",
			public: true,
		},
		{
			name:   "private_example_no_match",
			url:    "https://private.example.com/anything",
			public: false,
		},
		{
			name:   "case_insensitive_host",
			url:    "https://GITHUB.COM/aac/public-thing",
			public: true,
		},
		{
			name:   "ssh_form_match",
			url:    "git@github.com:aac/public-thing.git",
			public: true,
		},
		{
			name:   "ssh_form_no_match",
			url:    "git@github.com:aac/private-thing.git",
			public: false,
		},
		{
			name:   "local_filesystem_path",
			url:    "/tmp/some/bare.git",
			public: false,
		},
		{
			name:   "empty_string",
			url:    "",
			public: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := config.IsPublicURL(tc.url)
			if got != tc.public {
				t.Errorf("IsPublicURL(%q) = %v, want %v", tc.url, got, tc.public)
			}
		})
	}
}
