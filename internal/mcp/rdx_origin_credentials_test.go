package mcp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// rdxSecret is a made-up credential embedded in the test origin URL. It is
// not a real token; the tests only ask whether act echoes it.
const rdxSecret = "s3cretTOKEN"

// rdxCaptureStderr runs fn with os.Stderr redirected and returns what was
// written to it. The MCP tools write the not-pushed warning block there.
func rdxCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-done
}

// TestDocClaim_Spec_RdxMCPRemoteCredentialsNeverReturned asserts the spec's
// "Credentials in a remote URL" paragraph on the MCP surface: with an origin
// whose URL carries userinfo, the result of act_finish, act_next,
// act_create and act_update — whose text lands in an agent's transcript —
// and the warning block on stderr never carry the credential (act-4a538e).
// `https://` is the form git already masks; `git://` is the one it echoes.
func TestDocClaim_Spec_RdxMCPRemoteCredentialsNeverReturned(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "true")
	for name, url := range map[string]string{
		"https": "https://user:" + rdxSecret + "@127.0.0.1:1/x.git",
		"git":   "git://user:" + rdxSecret + "@127.0.0.1:1/x.git",
	} {
		t.Run(name, func(t *testing.T) {
			root := makeRepo(t)
			created, code := cli.RunCreate(root, cli.CreateOptions{Title: "rdx finish target", Type: "task"})
			if code != 0 {
				t.Fatalf("seed create: code=%d out=%+v", code, created)
			}
			id := created.(cli.CreateResult).ID
			mustGitMCP(t, filepath.Join(root, ".act"), "remote", "add", "origin", url)

			call := func(tool string, args map[string]any) string {
				t.Helper()
				var text string
				stderr := rdxCaptureStderr(t, func() {
					_, text = callTool(t, root, tool, args)
				})
				t.Logf("%s: result=%s\nstderr=%s", tool, text, strings.TrimSpace(stderr))
				if strings.Contains(text, rdxSecret) {
					t.Errorf("%s result carries the origin URL's credential: %s", tool, text)
				}
				if strings.Contains(stderr, rdxSecret) {
					t.Errorf("%s stderr carries the origin URL's credential:\n%s", tool, stderr)
				}
				return text
			}

			text := call("act_finish", map[string]any{"id": id})
			// The path under test must have run: a deferral was reported.
			if !strings.Contains(text, "push_deferred_reason") {
				t.Errorf("act_finish reported no push_deferred_reason; the credentialed-origin push path was not exercised: %s", text)
			}
			call("act_create", map[string]any{"title": "rdx created over mcp"})
			call("act_next", map[string]any{})
			call("act_update", map[string]any{"id": id, "priority": 1})
		})
	}
}
