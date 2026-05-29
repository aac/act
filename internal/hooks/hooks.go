// Package hooks implements the runtime contract for `.act/hooks/*` scripts
// described by spec §"Errors, hooks, migration..." §2.
//
// Discovery: only three filenames are recognized — `create`, `close`,
// `claim`. They execute pre-commit-op (between op-write/stage and
// op-commit) so a non-zero exit cleanly aborts the op. Future phases
// (`pre-commit-op`, `post-fold`, `post-compact`) are reserved and MUST
// NOT be loaded by this package.
//
// Naming history (act-8277): an earlier version of this package used
// `post-<op>` filenames internally while every doc + every concrete
// hook script in the act repo used the bare op-type name. The
// mismatch silently no-op'd every hook in the act repo. The bare
// names now match what AGENTS.md, the act-skill, and `act help
// workflow` document.
//
// Caller invariant: hooks NEVER run during `act fold` (read-only),
// replay/recovery, `act import`, or fresh `git clone`. The invariant is
// "hooks fire exactly once per logical op, on the writer that originated
// it". This package does not enforce that invariant — it is the caller's
// responsibility to skip Run on those code paths.
package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"
)

// Phase enumerates the lifecycle moments at which hooks fire. Only one
// phase exists in v0.1.
type Phase string

// PhasePreCommitOp is the only phase recognized in v0.1. All three hook
// files (`create`, `close`, `claim`) actually run at this phase — between
// `git add` of the op file and `git commit`.
const PhasePreCommitOp Phase = "pre-commit-op"

// recognized maps op_type → hook filename. The filename mirrors the
// op-type 1:1 (the same name AGENTS.md, the act-skill, and `act help
// workflow` all use). Other op-types skip hook execution entirely
// (caller does not invoke ResolveHook for them, but ResolveHook itself
// returns ("", false) for unknown op-types as a belt-and-braces guard).
//
// Note (act-8277): the original v0.1 map used `post-<op>` filenames
// but every doc + every existing hook file in this repo uses the bare
// op-type name. The mismatch meant ResolveHook silently returned
// ("", false) on every close, the `.act/hooks/close` gate never fired,
// and gofmt/vet/test drift reached CI without being caught locally.
// The post- prefix is now dropped to match what's documented.
var recognized = map[string]string{
	"create": "create",
	"close":  "close",
	"claim":  "claim",
}

// HookContext carries the per-invocation environment for a hook. OpJSON
// is the canonical-JSON serialization of the op envelope; it is piped to
// the hook's stdin verbatim, with no trailing newline. Closing stdin
// signals EOF to the script.
//
// Phase 1 of the coordination-plane design (docs/coordination-plane-design.md
// delta item 4) pins the hook execution contract: hooks always run with
// cwd=HostRepoRoot and the environment variable $ACT_STATE_PATH set to the
// nested .act/ directory. This means a hook script can shell out to project
// commands (gofmt, go test, npm test, etc.) without first having to climb
// out of the .act/ subtree to find the project root — and can locate the
// act state path explicitly when it needs to inspect op files. HostRepoRoot
// and ActStatePath are exposed to the hook as:
//
//   - cwd = HostRepoRoot
//   - env $ACT_STATE_PATH = ActStatePath
//
// Both are absolute paths. When HostRepoRoot is empty Run falls back to
// the calling process's cwd (the pre-Phase-1 behavior) so existing tests
// that don't set the field keep working.
type HookContext struct {
	OpID         string // sha256 of canonical JSON; exposed as $ACT_OP_ID
	OpType       string // create|close|claim; exposed as $ACT_OP_TYPE
	IssueID      string // exposed as $ACT_ISSUE_ID
	Phase        Phase  // exposed as $ACT_HOOK_PHASE
	OpJSON       []byte // payload for stdin; not modified
	HostRepoRoot string // absolute path to host repo; cwd for the hook
	ActStatePath string // absolute path to nested .act/; exposed as $ACT_STATE_PATH
}

// HookFailedError is returned by Run when the hook process exits non-zero
// or is killed by the timeout machinery. Code is the OS exit code (1 if
// the process was signalled). StderrTail holds the last 4096 bytes of
// captured stderr after UTF-8-trim. Truncated is true when the total
// stderr written by the hook exceeded the 64KB cap. Cause is "exit" for
// non-zero exit and "timeout" for SIGTERM/SIGKILL paths.
type HookFailedError struct {
	Code       int
	StderrTail string
	Truncated  bool
	Cause      string // "exit" | "timeout"
}

// Error implements error.
func (e *HookFailedError) Error() string {
	if e.Cause == "timeout" {
		return fmt.Sprintf("hook timed out (code=%d, truncated=%t)", e.Code, e.Truncated)
	}
	return fmt.Sprintf("hook exited %d (truncated=%t)", e.Code, e.Truncated)
}

// stderrCap is the total bytes captured from the hook's stderr before
// further bytes are dropped and Truncated is set.
const stderrCap = 64 * 1024

// stderrTailMax is the size of the tail surfaced in HookFailedError.
const stderrTailMax = 4096

// graceWindow is the time given to a hook to exit cleanly after SIGTERM
// before SIGKILL is sent.
const graceWindow = 1 * time.Second

// ResolveHook returns the absolute path of the hook executable matching
// opType and a boolean indicating whether it should be invoked. It
// returns ("", false) when:
//
//   - opType is not one of create|close|claim,
//   - the file does not exist,
//   - the file exists but is not executable by the current user.
//
// Reserved future names (`pre-commit-op`, `post-fold`, `post-compact`)
// are not in the recognized map, so they are never resolved. ResolveHook
// does not surface stat errors other than non-existence; permission
// errors and similar return ("", false) by design (the spec mandates
// "skipped silently").
func ResolveHook(hooksDir, opType string) (string, bool) {
	name, ok := recognized[opType]
	if !ok {
		return "", false
	}
	path := filepath.Join(hooksDir, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return "", false
	}
	// Skip silently if not executable. We check the user-execute bit;
	// platforms without POSIX mode bits (Windows) will see 0 here, so
	// callers on those platforms should treat the hooks dir as opt-in
	// via separate platform code (out of scope for v0.1).
	if info.Mode().Perm()&0o100 == 0 {
		return "", false
	}
	return path, true
}

// Run executes executablePath with ctx.OpJSON on stdin and the four
// `ACT_*` environment variables set. It enforces a wall-clock timeout: on
// expiry, the process receives SIGTERM, then SIGKILL after graceWindow.
//
// On exit-zero, Run returns nil. On non-zero exit or kill, Run returns a
// *HookFailedError. The op file remains on disk — clean-up (unstage,
// delete) is the caller's responsibility (see internal/cli.WriteOpAndAutoCommit).
//
// Stderr is captured up to stderrCap bytes; bytes beyond the cap are
// counted (so Truncated can be set) but discarded. Stdout is captured
// with the same cap and discarded on success per spec §2 step 6; the
// stdout buffer is intentionally unused on the success path.
func Run(ctx HookContext, executablePath string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// We do NOT use exec.CommandContext because its cancellation path
	// uses Process.Kill (SIGKILL) directly, skipping the SIGTERM grace
	// window mandated by the spec. Instead, we manage the timeout with
	// a goroutine that sends SIGTERM then SIGKILL.
	cmd := exec.Command(executablePath)
	env := append(os.Environ(),
		"ACT_OP_ID="+ctx.OpID,
		"ACT_OP_TYPE="+ctx.OpType,
		"ACT_ISSUE_ID="+ctx.IssueID,
		"ACT_HOOK_PHASE="+string(ctx.Phase),
	)
	if ctx.ActStatePath != "" {
		env = append(env, "ACT_STATE_PATH="+ctx.ActStatePath)
	}
	cmd.Env = env
	// Phase 1 contract: hooks run with cwd=HostRepoRoot. When the caller
	// hasn't supplied one (legacy callers, internal tests that don't care)
	// we fall back to the process cwd, which preserves pre-Phase-1
	// behavior.
	if ctx.HostRepoRoot != "" {
		cmd.Dir = ctx.HostRepoRoot
	}
	// Place the hook in its own process group so the timeout watchdog
	// can reliably kill the entire subtree (e.g. shell + sleep child).
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("hooks: stdin pipe: %w", err)
	}

	stderrBuf := &cappedBuffer{cap: stderrCap}
	stdoutBuf := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderrBuf
	cmd.Stdout = stdoutBuf

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("hooks: start: %w", err)
	}

	// Pipe op JSON to stdin in a goroutine so a hook that ignores stdin
	// cannot deadlock us. Errors writing to stdin (e.g. "broken pipe"
	// because the hook closed stdin early) are not fatal; the hook's
	// exit code is the source of truth.
	writeErrCh := make(chan error, 1)
	go func() {
		var werr error
		if len(ctx.OpJSON) > 0 {
			_, werr = stdin.Write(ctx.OpJSON)
		}
		cerr := stdin.Close()
		if werr == nil {
			werr = cerr
		}
		writeErrCh <- werr
	}()

	// Manage timeout. We use a cancel-on-done pattern so the watchdog
	// goroutine exits promptly when the hook finishes on its own. The
	// wasTimeout flag is set before any signal is delivered so the main
	// goroutine can classify the exit deterministically once Wait
	// returns.
	timeoutCtx, cancel := context.WithCancel(context.Background())
	wd := &watchdog{}
	go func() {
		select {
		case <-timeoutCtx.Done():
			return
		case <-time.After(timeout):
		}
		wd.markTimeout()
		// Best-effort SIGTERM to the whole group. Ignore errors: the
		// process may have already exited between the timer and our
		// Signal call.
		killGroup(cmd, sigterm())
		select {
		case <-timeoutCtx.Done():
			return
		case <-time.After(graceWindow):
		}
		killGroup(cmd, os.Kill)
	}()

	waitErr := cmd.Wait()
	cancel()
	<-writeErrCh

	wasTimeout := wd.timedOut()

	tailBytes, truncatedTail := stderrTail(stderrBuf.Bytes())
	overflow := stderrBuf.Truncated() || stdoutBuf.Truncated()
	_ = truncatedTail // tail truncation by 4KB is implicit; "Truncated" reports the 64KB overflow per spec §5.D.3

	if waitErr == nil && !wasTimeout {
		return nil
	}

	herr := &HookFailedError{
		StderrTail: string(tailBytes),
		Truncated:  overflow,
	}
	if wasTimeout {
		herr.Cause = "timeout"
		// Killed processes have no clean exit code; we report 1 to
		// match the spec's "treated as failure" wording without
		// overloading 0.
		herr.Code = 1
		return herr
	}

	herr.Cause = "exit"
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		herr.Code = exitErr.ExitCode()
		if herr.Code < 0 {
			herr.Code = 1
		}
	} else {
		// Non-ExitError (e.g. exec failure post-Start) — treat as
		// generic failure with code 1.
		herr.Code = 1
	}
	return herr
}

// cappedBuffer is an io.Writer that retains up to cap bytes. Writes
// beyond cap are silently dropped, but the truncated flag is latched so
// callers can surface the fact to the user.
type cappedBuffer struct {
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.cap - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// Bytes returns the captured bytes; callers must not mutate.
func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

// Truncated reports whether any write was dropped due to the cap.
func (c *cappedBuffer) Truncated() bool { return c.truncated }

// stderrTail returns the last stderrTailMax bytes of b after trimming any
// invalid trailing UTF-8 bytes. The boolean is true when the input was
// longer than stderrTailMax and was sliced.
func stderrTail(b []byte) ([]byte, bool) {
	sliced := false
	if len(b) > stderrTailMax {
		b = b[len(b)-stderrTailMax:]
		sliced = true
	}
	// Walk back from end to find a position where the suffix begins on
	// a UTF-8 sequence start byte. utf8.Valid handles the full slice;
	// if not valid, we trim trailing bytes that are continuation bytes
	// or partial leaders.
	if utf8.Valid(b) {
		return b, sliced
	}
	// Drop trailing bytes until we find a valid suffix boundary.
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r == utf8.RuneError && size == 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b, sliced
}

// io.Writer compile-time assertion.
var _ io.Writer = (*cappedBuffer)(nil)

// watchdog records whether the timeout watchdog has decided to kill the
// hook. The flag is consulted by the main goroutine after cmd.Wait
// returns to classify the failure deterministically.
type watchdog struct {
	mu      sync.Mutex
	timeout bool
}

func (w *watchdog) markTimeout() {
	w.mu.Lock()
	w.timeout = true
	w.mu.Unlock()
}

func (w *watchdog) timedOut() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.timeout
}
