// Package mcp implements a minimal stdio JSON-RPC 2.0 server exposing the
// act CLI verbs as MCP tools. This is the 1:1 scaffold (act-380d): each
// CLI command in internal/cli has a matching tool here; the composed tools
// (act_next, act_finish, act_block) live in act-2f81.
//
// The wire protocol is the standard MCP subset over stdio: newline-delimited
// JSON-RPC requests on stdin, responses on stdout. Three methods are
// implemented:
//
//   - initialize  — handshake; advertises tool capabilities.
//   - tools/list  — returns the registered tool descriptors with input
//     schemas mirroring the CLI flag set.
//   - tools/call  — dispatches into the matching cli.RunX function and
//     returns the JSON body verbatim, or surfaces an error envelope.
//
// Tool errors are returned in the result envelope (`isError: true`) rather
// than as JSON-RPC errors, matching the MCP convention. Invalid methods or
// malformed requests return JSON-RPC error -32601 / -32600 respectively.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/aac/act/internal/cli"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/version"
)

// protocolVersion is the MCP wire version we advertise during initialize.
// The handshake is intentionally lax: we accept any client version and echo
// our own back. Clients that depend on a specific version should pin it
// out-of-band.
const protocolVersion = "2024-11-05"

// serverName / serverVersion are echoed in the initialize response so MCP
// clients can render an identifying label in their UIs.
const serverName = "act-mcp"

// serverVersion mirrors the stamped release version (version.Binary): "dev" for
// local builds, the real version when the release linker stamps
// internal/version.Binary in CI. var, not const, so the stamp flows through.
var serverVersion = version.Binary

// Server is a stdio MCP host. It owns the JSON-RPC framing, the tool
// registry, and the cli dispatch glue. One Server is single-threaded: Run
// reads, dispatches, and writes one request at a time. This matches the
// stdio transport's serial nature and keeps the cli's repo-state mutations
// race-free.
type Server struct {
	// resolveRoot lazily resolves the host repo root. It is invoked on the
	// first tool call (not at construction), so the initialize/tools-list
	// handshake answers in ANY cwd — see NewDeferredServer and act-119180.
	resolveRoot func() (string, error)

	// repoRoot / rootErr / rootResolved cache the resolveRoot result. The
	// server is single-threaded (Run dispatches one request at a time), so a
	// plain bool guard is sufficient — no mutex needed.
	repoRoot     string
	rootErr      error
	rootResolved bool

	readOnly bool
	in       io.Reader
	out      io.Writer
}

// NewServer constructs a Server with a pre-resolved repo root. repoRoot is
// used as the cwd-equivalent for every tool dispatch. When readOnly is true,
// write tools are refused with a read_only_violation regardless of any
// per-call read_only argument.
//
// Use this when the host repo root is already known (e.g. tests). For the
// stdio entrypoint, prefer NewDeferredServer so a missing host repo / .act/
// does not abort the initialize handshake.
func NewServer(repoRoot string, readOnly bool, in io.Reader, out io.Writer) *Server {
	s := NewDeferredServer(func() (string, error) { return repoRoot, nil }, readOnly, in, out)
	// Pre-resolved: populate the cache eagerly so callers that dispatch through
	// the per-tool callX helpers directly (tests) see repoRoot without first
	// routing through handleToolsCall's lazy hostRoot gate.
	s.repoRoot = repoRoot
	s.rootResolved = true
	return s
}

// NewDeferredServer constructs a Server whose host repo root is resolved
// lazily on the first tool call via resolveRoot, rather than eagerly at
// construction. This is what lets `act mcp` answer a JSON-RPC initialize (and
// tools/list) handshake in ANY cwd — including one with no host git repo or no
// .act/ — with the "no host repo" / "no act state" error deferred to the tool
// calls that actually need tracker state (act-119180). MCP clients such as
// Codex register the server (command ./bin/act, cwd .) in a bare context and
// would otherwise see the process exit before initialize ever completes.
func NewDeferredServer(resolveRoot func() (string, error), readOnly bool, in io.Reader, out io.Writer) *Server {
	return &Server{
		resolveRoot: resolveRoot,
		readOnly:    readOnly,
		in:          in,
		out:         out,
	}
}

// hostRoot lazily resolves and caches the host repo root. Resolution is
// deferred to tool-call time (act-119180) so initialize/tools-list succeed in
// any cwd; the resolver error (no host repo, etc.) is cached and surfaced to
// every tool call that needs tracker state.
func (s *Server) hostRoot() (string, error) {
	if !s.rootResolved {
		s.repoRoot, s.rootErr = s.resolveRoot()
		s.rootResolved = true
	}
	return s.repoRoot, s.rootErr
}

// effectiveRoot resolves the host repo root for one tool call. It prefers a
// workspace the CLIENT names out-of-band over the server's process cwd
// (act-ffc00d).
//
// Why this exists: a plugin host launches `act mcp` as a long-lived process
// with a cwd it chooses, which is NOT the user's project. Claude Code happens
// to launch it in the project dir (its `.mcp.json` sets no cwd), so cwd-based
// resolution works there. Codex launches it with cwd = the plugin's install
// dir (its `.codex-plugin/mcp.json` pins `cwd "."`, needed only to locate the
// relative `./bin/act`), and advertises no MCP `roots` capability — so cwd is
// the plugin cache and every repo-relative tool would operate there. Codex
// does, however, carry the real workspace on every `tools/call` in a
// proprietary `_meta` block; we read it and resolve the host repo from it.
//
// Precedence: a client-supplied workspace is authoritative (resolve the host
// repo from it, or surface that workspace's error — never silently fall back
// to cwd, which is the plugin dir and the bug). Only when no workspace hint is
// present do we fall back to the cwd-based resolver (the Claude / direct-CLI
// path).
func (s *Server) effectiveRoot(rawParams json.RawMessage) (string, error) {
	ws := workspacesFromMeta(rawParams)
	if len(ws) == 0 {
		return s.hostRoot()
	}
	var firstErr error
	for _, w := range ws {
		root, err := gitops.FindHostRepoRoot(w)
		if err == nil {
			return root, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", firstErr
}

// workspacesFromMeta extracts client-declared workspace roots from a
// tools/call params `_meta`. Today the only source is Codex's proprietary
// `x-codex-turn-metadata.workspaces` (a map keyed by absolute workspace path);
// the keys are returned sorted so multi-workspace selection is deterministic.
// Returns nil when the block is absent (Claude, direct CLI) — the caller then
// falls back to cwd-based resolution. Parse failures are treated as "absent"
// so a malformed or unexpected `_meta` never breaks a tool call.
func workspacesFromMeta(rawParams json.RawMessage) []string {
	if len(rawParams) == 0 {
		return nil
	}
	var p struct {
		Meta struct {
			Codex struct {
				Workspaces map[string]json.RawMessage `json:"workspaces"`
			} `json:"x-codex-turn-metadata"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return nil
	}
	if len(p.Meta.Codex.Workspaces) == 0 {
		return nil
	}
	keys := make([]string, 0, len(p.Meta.Codex.Workspaces))
	for k := range p.Meta.Codex.Workspaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonRPCRequest is the inbound shape on stdin. id is `any` so we round-trip
// numbers and strings unmodified per JSON-RPC 2.0.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is the success/error envelope we emit. Exactly one of
// Result/Error is set per the spec; the omitempty tags keep the wire form
// clean.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError mirrors the spec's error object. Data is optional and used
// for free-form diagnostics.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC error codes. We use a tight subset; everything beyond
// these codes belongs in the tool-result envelope.
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternal       = -32603
)

// toolDescriptor is one entry in the tools/list response. The InputSchema
// is a freeform JSON Schema object describing the tool's argument shape.
type toolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolResult is the tools/call response envelope. Content is a list of
// content parts; we use a single text part containing the JSON body of the
// underlying CLI command. IsError signals to MCP clients that the tool
// returned an error envelope rather than a successful result.
type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// toolContent is one content part. Only "text" parts are produced by the
// scaffold; structured content is reserved for the composed tools.
type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Run drives the read/dispatch/write loop. It terminates cleanly on EOF,
// on ctx.Done(), or when stdin returns a non-EOF error. Bad JSON is
// reported as a Parse Error and the loop continues; a malformed-but-parsed
// request returns Invalid Request.
func (s *Server) Run(ctx context.Context) error {
	r := bufio.NewReader(s.in)
	enc := json.NewEncoder(s.out)
	// MCP transport is plain JSON-RPC over stdio; the response body is never
	// embedded in HTML, so encoding/json's default escape (act-e26e) of '<',
	// '>', and '&' to their six-byte backslash-u00xx forms is pure noise —
	// it turns tracker text like `act create --blocks <id>` into a wire
	// string littered with backslash-u sequences. Disable it so
	// user-supplied bytes round-trip unchanged and FTS search hits on the
	// literal characters.
	enc.SetEscapeHTML(false)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				if len(line) == 0 {
					return nil
				}
				// fall through and process trailing partial line
			} else {
				return err
			}
		}
		line = trimLine(line)
		if len(line) == 0 {
			if err == io.EOF {
				return nil
			}
			continue
		}
		var req jsonRPCRequest
		if jerr := json.Unmarshal(line, &req); jerr != nil {
			s.writeError(enc, nil, errParse, "parse error", jerr.Error())
			if err == io.EOF {
				return nil
			}
			continue
		}
		s.dispatch(ctx, enc, req)
		if err == io.EOF {
			return nil
		}
	}
}

// trimLine drops trailing CR/LF; matches the behaviour of bufio.Scanner
// without the 64KiB token cap.
func trimLine(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// dispatch routes one parsed request to the correct handler. Notifications
// (id absent) are silently ignored except for handshake errors.
func (s *Server) dispatch(ctx context.Context, enc *json.Encoder, req jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(enc, req)
	case "initialized", "notifications/initialized":
		// Notification — no response.
	case "tools/list":
		s.handleToolsList(enc, req)
	case "tools/call":
		s.handleToolsCall(ctx, enc, req)
	case "ping":
		s.writeResult(enc, req.ID, map[string]any{})
	default:
		s.writeError(enc, req.ID, errMethodNotFound, "method not found", req.Method)
	}
}

// handleInitialize emits the canonical handshake response. We always say
// we support tools; resources, prompts, sampling are unimplemented and
// omitted from capabilities so clients don't try to call them.
func (s *Server) handleInitialize(enc *json.Encoder, req jsonRPCRequest) {
	res := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
	}
	s.writeResult(enc, req.ID, res)
}

// handleToolsList returns the static tool registry. The list shape and
// schemas are stable; clients are expected to cache them per-session.
func (s *Server) handleToolsList(enc *json.Encoder, req jsonRPCRequest) {
	tools := s.tools()
	s.writeResult(enc, req.ID, map[string]any{"tools": tools})
}

// handleToolsCall dispatches to the matching tool implementation. The
// params shape is `{name: string, arguments: object}`; missing arguments
// default to an empty object so tools without inputs (e.g. act_doctor)
// work without ceremony.
func (s *Server) handleToolsCall(ctx context.Context, enc *json.Encoder, req jsonRPCRequest) {
	_ = ctx
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(enc, req.ID, errInvalidParams, "invalid params", err.Error())
		return
	}
	if p.Name == "" {
		s.writeError(enc, req.ID, errInvalidParams, "missing tool name", nil)
		return
	}
	if isWriteTool(p.Name) && s.readOnly {
		s.writeToolError(enc, req.ID, "method_not_allowed",
			fmt.Sprintf("server is read-only; tool %q not permitted", p.Name))
		return
	}
	// Resolve the host repo root for this call (act-119180, act-ffc00d).
	// Deferred from startup to here so initialize/tools-list answer in any cwd;
	// and resolved per-call so a client-supplied workspace (Codex's `_meta`)
	// overrides the server's process cwd — which under the Codex plugin launch
	// model is the plugin install dir, not the user's project. Every current
	// tool needs tracker state, so a resolution failure surfaces as a no_repo
	// tool-error envelope rather than aborting the server.
	root, err := s.effectiveRoot(req.Params)
	if err != nil {
		s.writeToolError(enc, req.ID, "no_repo", fmt.Sprintf("act mcp: %v", err))
		return
	}
	s.repoRoot = root
	s.rootResolved = true
	s.rootErr = nil
	args := p.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}
	res, isErr := s.invoke(p.Name, args)
	body, mErr := marshalNoHTMLEscape(res)
	if mErr != nil {
		s.writeError(enc, req.ID, errInternal, "result marshal", mErr.Error())
		return
	}
	tr := toolResult{
		Content: []toolContent{{Type: "text", Text: string(body)}},
		IsError: isErr,
	}
	s.writeResult(enc, req.ID, tr)
}

// invoke is the central tool dispatcher. It returns the JSON-shaped result
// (an `any` ready for marshaling) and a flag indicating whether the call
// resulted in an error envelope (non-zero exit). Unknown tool names return
// an error envelope so the caller's result framing remains consistent with
// regular tool errors.
func (s *Server) invoke(name string, args json.RawMessage) (any, bool) {
	switch name {
	case "act_init":
		return s.callInit(args)
	case "act_create":
		return s.callCreate(args)
	case "act_list":
		return s.callList(args)
	case "act_show":
		return s.callShow(args)
	case "act_update":
		return s.callUpdate(args)
	case "act_close":
		return s.callClose(args)
	case "act_dep_add":
		return s.callDepAdd(args)
	case "act_ready":
		return s.callReady(args)
	case "act_search":
		return s.callSearch(args)
	case "act_log":
		return s.callLog(args)
	case "act_doctor":
		return s.callDoctor(args)
	case "act_version":
		return s.callVersion(args)
	case "act_next":
		return s.callNext(args)
	case "act_finish":
		return s.callFinish(args)
	case "act_block":
		return s.callBlock(args)
	case "act_file_blocker":
		return s.callFileBlocker(args)
	default:
		return errEnvelope("unknown_tool", fmt.Sprintf("unknown tool %q", name)), true
	}
}

// errEnvelope is the canonical {error, message} shape returned to MCP
// clients on any tool failure that doesn't already produce one. Matches
// the spec's error taxonomy.
func errEnvelope(kind, msg string) map[string]any {
	return map[string]any{"error": kind, "message": msg}
}

// writeResult emits a JSON-RPC success response. Notifications (nil id)
// produce no output, matching the spec.
func (s *Server) writeResult(enc *json.Encoder, id json.RawMessage, result any) {
	if id == nil {
		return
	}
	_ = enc.Encode(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

// writeError emits a JSON-RPC error response. Like writeResult, it skips
// notifications.
func (s *Server) writeError(enc *json.Encoder, id json.RawMessage, code int, msg string, data any) {
	if id == nil && code != errParse {
		return
	}
	_ = enc.Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: msg, Data: data},
	})
}

// writeToolError emits a tool-result envelope with isError=true so the
// client surfaces it as a tool failure rather than a transport error. Used
// for read-only enforcement and the like.
func (s *Server) writeToolError(enc *json.Encoder, id json.RawMessage, kind, msg string) {
	body, _ := marshalNoHTMLEscape(errEnvelope(kind, msg))
	tr := toolResult{
		Content: []toolContent{{Type: "text", Text: string(body)}},
		IsError: true,
	}
	s.writeResult(enc, id, tr)
}

// marshalNoHTMLEscape is json.Marshal without the default HTML-safe escaping
// of '<', '>', and '&' (act-e26e). MCP tool-result bodies are nested inside a
// JSON-RPC envelope as a plain string; they are never spliced into HTML, so
// the default escape turns user-supplied tracker text into the backslash-u
// form for those three characters. The buffer's trailing newline
// (json.Encoder always appends one) is trimmed so callers get the same byte
// shape json.Marshal returned previously.
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// isWriteTool returns true for the tools that mutate repo state. Any of
// these are blocked when the server was started with --read-only.
func isWriteTool(name string) bool {
	switch name {
	case "act_init", "act_create", "act_update", "act_close", "act_dep_add",
		"act_next", "act_finish", "act_block", "act_file_blocker":
		return true
	}
	return false
}

// tools returns the registered tool descriptors in a deterministic order.
// Schemas mirror the cli flag sets (kebab-case → snake_case), minus the
// per-call plumbing params trimmed in act-ca659d.
//
// What was dropped from the ADVERTISED schemas and why (the schema text is
// re-read on every turn of every session that wires this server, so bytes
// here are a recurring cost, not a one-off):
//
//   - read_only — was on all 16 tools; its own description said the
//     server-level --read-only flag takes precedence, so it was a per-call
//     advisory nothing enforced. Enforcement is unchanged: handleToolsCall
//     refuses write tools when the server runs --read-only, and act_finish
//     still honours a per-call read_only if a client sends one.
//   - no_commit / isolated — auto-commit and git-touching are what make an
//     act write durable and shareable; an agent driving the tracker over MCP
//     has no reason to opt out, and a silently uncommitted op is a stranded
//     one. Still decoded by the callX glue when passed.
//
// Nothing was renamed, retyped, or made (non-)required — dropping a schema
// property does not remove it from the wire, because schemaObject leaves
// additionalProperties unconstrained. Cross-tool explanatory prose (the
// dep-add direction worked example, accept-vs-accept_add contrasts) moved to
// skills/act/SKILL.md, which an agent reads once instead of every turn.
func (s *Server) tools() []toolDescriptor {
	return []toolDescriptor{
		{
			Name:        "act_init",
			Description: "Initialize an act repository at the server's repo root.",
			InputSchema: schemaObject(map[string]any{
				"force": schemaBool("Reinitialize even if .act/ already exists."),
			}, nil),
		},
		{
			Name:        "act_create",
			Description: "Create an issue. blocked_by/blocks attach dep edges atomically in the same commit — use them instead of a follow-up act_dep_add.",
			InputSchema: schemaObject(map[string]any{
				"title":       schemaString("Issue title (required, ≤256 bytes)."),
				"priority":    schemaInteger("Priority 0-3 (default 1)."),
				"type":        schemaEnum([]string{"task", "bug", "epic", "chore"}, "Issue type."),
				"parent":      schemaString("Parent id (hierarchy only, NOT a dep edge)."),
				"blocked_by":  schemaArrayOfString("Ids the NEW issue is blocked by (it stays out of ready until each closes)."),
				"blocks":      schemaArrayOfString("Existing ids the NEW issue blocks (each stays out of ready until it closes)."),
				"description": schemaString("Free-text body."),
				"accept":      schemaArrayOfString("Acceptance criteria, in order."),
				"push":        schemaBool("Push after commit."),
			}, []string{"title"}),
		},
		{
			Name:        "act_list",
			Description: "List issues. By DEFAULT this is the working set (open, in_progress, blocked) and closed issues are excluded; pass status=\"closed\" or all=true to reach them.",
			InputSchema: schemaObject(map[string]any{
				"status":   schemaString("Comma-separated status filter. Omit for the working set."),
				"all":      schemaBool("Include closed issues (sorted last). Mutually exclusive with status."),
				"assignee": schemaString("Exact-match assignee filter."),
				"type":     schemaString("Issue type filter (task|bug|epic|chore)."),
				"limit":    schemaInteger("Maximum issues to return (default 200)."),
				"sort":     schemaString("Comma-separated sort keys; prefix '-' for desc."),
			}, nil),
		},
		{
			Name:        "act_show",
			Description: "Show one issue's rendered state. `blocked_by` lists the issues that block THIS issue; `blocks` lists the issues THIS issue blocks. Authoritative for dep direction.",
			InputSchema: schemaObject(map[string]any{
				"id":          schemaString("Issue id or prefix."),
				"include_ops": schemaBool("Include the HLC-sorted op stream."),
			}, []string{"id"}),
		},
		{
			Name:        "act_update",
			Description: "Escape hatch: update an issue's fields, accept criteria, or claim. Prefer act_next for the claim flow.",
			InputSchema: schemaObject(map[string]any{
				"id":                 schemaString("Issue id or prefix."),
				"status":             schemaString("New status (open|in_progress|blocked|closed)."),
				"priority":           schemaInteger("New priority (0-3)."),
				"assignee":           schemaString("New assignee (empty string clears)."),
				"description":        schemaString("New description (REPLACES the body; use description_append to add)."),
				"description_append": schemaString("Append this text to the existing description instead of replacing it. Mutually exclusive with description."),
				"accept":             schemaArrayOfString("Replace the acceptance criteria with exactly this list (the set REPLACES any prior criteria, it does not append); [] clears."),
				"accept_add":         schemaArrayOfString("Append these criteria to the existing acceptance list."),
				"accept_rm":          schemaArrayOfInteger("Remove acceptance criteria by zero-based index; out-of-range is a no-op."),
				"dep_rm":             schemaArrayOfString("Dep ids to remove."),
				"ext_rm":             schemaArrayOfString("External-tracker refs to clear."),
				"claim":              schemaBool("Atomic claim mode."),
				"unclaim":            schemaBool("Release a claim: in_progress back to open, assignee cleared."),
				"wait":               schemaBool("Wait for claim to free."),
				"wait_timeout":       schemaString("Wait timeout (Go duration string)."),
				"push":               schemaBool("Push after commit."),
				"verify":             schemaBool("Run integrity check after write."),
			}, []string{"id"}),
		},
		{
			Name:        "act_close",
			Description: "Escape hatch: close an issue. Prefer act_finish for the recommended workflow.",
			InputSchema: schemaObject(map[string]any{
				"id":     schemaString("Issue id or prefix."),
				"reason": schemaString("Optional close reason (≤500 bytes)."),
				"push":   schemaBool("Push after commit."),
			}, []string{"id"}),
		},
		{
			Name:        "act_dep_add",
			Description: "Escape hatch: add a dependency edge. For edge_type=blocks the CHILD is blocked BY the PARENT. Trust the response's plain-English `summary` over the raw child/parent fields. Prefer act_block or act_create's blocked_by/blocks, which name the blocker directly.",
			InputSchema: schemaObject(map[string]any{
				"child":     schemaString("Dependent id (for blocks: the issue that becomes blocked)."),
				"parent":    schemaString("Blocker id (for blocks: the issue that must close first). Omit when using `external`."),
				"edge_type": schemaEnum([]string{"blocks", "relates", "supersedes"}, "Edge type (default 'blocks')."),
				"external":  schemaArrayOfString("External-tracker refs to attach to `child` as blocking deps (e.g. \"linear:ENG-123\"). When set, parent/edge_type are ignored."),
				"push":      schemaBool("Push after commit."),
			}, []string{"child"}),
		},
		{
			Name:        "act_ready",
			Description: "Escape hatch: list the ready set: open issues with no unclosed blocking deps. Prefer act_next which combines ready + claim + show.",
			InputSchema: schemaObject(map[string]any{
				"under": schemaString("Restrict to descendants of this issue id/prefix."),
				"limit": schemaInteger("Maximum issues to return (default 50)."),
			}, nil),
		},
		{
			Name:        "act_search",
			Description: "Full-text search across issues.",
			InputSchema: schemaObject(map[string]any{
				"query":  schemaString("Search query (required)."),
				"in":     schemaEnum([]string{"title", "desc", "all"}, "FTS5 column scope (default 'all')."),
				"status": schemaString("Comma-separated status filter."),
				"limit":  schemaInteger("Maximum matches (default 50)."),
			}, []string{"query"}),
		},
		{
			Name:        "act_log",
			Description: "Show the HLC-sorted op log for one issue.",
			InputSchema: schemaObject(map[string]any{
				"id": schemaString("Issue id or prefix."),
			}, []string{"id"}),
		},
		{
			Name:        "act_doctor",
			Description: "Run repository integrity checks.",
			InputSchema: schemaObject(map[string]any{
				"check": schemaString("Run a single named check (empty runs all)."),
				"fix":   schemaBool("Auto-remediate where safe."),
			}, nil),
		},
		{
			Name:        "act_version",
			Description: "Report the act binary version and optionally the repo's max op_version.",
			InputSchema: schemaObject(map[string]any{
				"check_repo": schemaBool("Walk .act/ops/ and report max writer_version."),
			}, nil),
		},
		{
			Name:        "act_next",
			Description: "Recommended: pick the next ready issue, claim it, and return its rendered state (ready + claim + show, with bounded retry on claim loss). Returns `commit_marker` — embed it verbatim as a trailer in the BODY of every work-commit for the issue.",
			InputSchema: schemaObject(map[string]any{
				"under": schemaString("Optional id prefix; restrict to descendants."),
			}, nil),
		},
		{
			Name:        "act_finish",
			Description: "Recommended: close an issue and return its `Act-Id:` commit-marker trailer. Wraps act_close.",
			InputSchema: schemaObject(map[string]any{
				"id":     schemaString("Issue id or prefix (required)."),
				"reason": schemaString("Optional close reason."),
			}, []string{"id"}),
		},
		{
			Name:        "act_block",
			Description: "Recommended: mark an issue blocked AND record the blocks-edge in a single commit. Use instead of act_update + act_dep_add.",
			InputSchema: schemaObject(map[string]any{
				"id":         schemaString("Issue to mark blocked (required)."),
				"blocked_by": schemaString("Issue that blocks (required)."),
				"reason":     schemaString("Optional reason."),
			}, []string{"id", "blocked_by"}),
		},
		{
			Name:        "act_file_blocker",
			Description: "Recommended: file a new issue already blocked by one or more existing issues, in a single atomic commit. Replaces act_create + act_dep_add.",
			InputSchema: schemaObject(map[string]any{
				"title":       schemaString("Issue title (required, ≤256 bytes)."),
				"blocked_by":  schemaArrayOfString("Ids the new issue is blocked by (required, ≥1)."),
				"description": schemaString("Optional free-text body."),
				"accept":      schemaArrayOfString("Optional acceptance criteria, in order."),
				"type":        schemaString("Optional issue type (task|bug|epic|chore; default task)."),
				"priority":    schemaInteger("Optional priority 0–3 (default 2)."),
				"parent":      schemaString("Optional parent id (hierarchy, not a dep edge)."),
			}, []string{"title", "blocked_by"}),
		},
	}
}

// schemaObject is the boilerplate JSON-Schema wrapper used by every tool's
// input definition. additionalProperties is deliberately unconstrained: the
// wire still ACCEPTS the plumbing fields the schemas no longer advertise
// (read_only, no_commit, isolated — see the tools() comment), so a client
// holding a cached older schema keeps working.
func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func schemaString(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func schemaInteger(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func schemaBool(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func schemaArrayOfString(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}

func schemaArrayOfInteger(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "integer"},
		"description": desc,
	}
}

func schemaEnum(values []string, desc string) map[string]any {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return map[string]any{
		"type":        "string",
		"enum":        vs,
		"description": desc,
	}
}

// ----- per-tool dispatch glue -------------------------------------------------
//
// Each callX decodes its arguments object into the matching cli options
// struct and forwards to RunX. The output is returned as-is; non-zero exit
// codes flip the isError flag on the tool result envelope.

func (s *Server) callInit(raw json.RawMessage) (any, bool) {
	var args struct {
		Force bool `json:"force"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	// MCP-driven init under Phase 1 always bootstraps the nested .act/ repo
	// with its initial commit; there's no --no-commit toggle anymore.
	out, code := cli.RunInit(s.repoRoot, args.Force, "", "", nil)
	return out, code != 0
}

func (s *Server) callCreate(raw json.RawMessage) (any, bool) {
	var args struct {
		Title       string   `json:"title"`
		Priority    *int     `json:"priority"`
		Type        string   `json:"type"`
		Parent      string   `json:"parent"`
		BlockedBy   []string `json:"blocked_by"`
		Blocks      []string `json:"blocks"`
		Description string   `json:"description"`
		Accept      []string `json:"accept"`
		NoCommit    bool     `json:"no_commit"`
		Push        bool     `json:"push"`
		Isolated    bool     `json:"isolated"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunCreate(s.repoRoot, cli.CreateOptions{
		Title:       args.Title,
		Priority:    args.Priority,
		Type:        args.Type,
		Parent:      args.Parent,
		BlockedBy:   args.BlockedBy,
		Blocks:      args.Blocks,
		Description: args.Description,
		Accept:      args.Accept,
		AsJSON:      true,
		NoCommit:    args.NoCommit,
		Push:        args.Push,
		Isolated:    args.Isolated,
	})
	return out, code != 0
}

func (s *Server) callList(raw json.RawMessage) (any, bool) {
	var args struct {
		Status   string `json:"status"`
		All      bool   `json:"all"`
		Assignee string `json:"assignee"`
		Type     string `json:"type"`
		Limit    int    `json:"limit"`
		Sort     string `json:"sort"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	if args.Limit == 0 {
		args.Limit = 200
	}
	out, code := cli.RunList(s.repoRoot, cli.ListOptions{
		Status:   args.Status,
		All:      args.All,
		Assignee: args.Assignee,
		Type:     args.Type,
		Limit:    args.Limit,
		Sort:     args.Sort,
		AsJSON:   true,
	})
	return out, code != 0
}

func (s *Server) callShow(raw json.RawMessage) (any, bool) {
	var args struct {
		ID         string `json:"id"`
		IncludeOps bool   `json:"include_ops"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunShow(s.repoRoot, cli.ShowOptions{
		ID:         args.ID,
		AsJSON:     true,
		IncludeOps: args.IncludeOps,
	})
	if code == 0 {
		// Match cmd/act/main.go's JSON rendering: surface the rendered map.
		if r, ok := out.(cli.ShowResult); ok {
			return r.ShowJSON(), false
		}
	}
	return out, code != 0
}

func (s *Server) callUpdate(raw json.RawMessage) (any, bool) {
	var args struct {
		ID                string    `json:"id"`
		Status            *string   `json:"status"`
		Priority          *int      `json:"priority"`
		Assignee          *string   `json:"assignee"`
		Description       *string   `json:"description"`
		DescriptionAppend *string   `json:"description_append"`
		Accept            *[]string `json:"accept"`
		AcceptAdd         []string  `json:"accept_add"`
		AcceptRm          []int     `json:"accept_rm"`
		DepRm             []string  `json:"dep_rm"`
		ExtRm             []string  `json:"ext_rm"`
		Claim             bool      `json:"claim"`
		Unclaim           bool      `json:"unclaim"`
		Wait              bool      `json:"wait"`
		WaitTimeout       string    `json:"wait_timeout"`
		NoCommit          bool      `json:"no_commit"`
		Push              bool      `json:"push"`
		Isolated          bool      `json:"isolated"`
		Verify            bool      `json:"verify"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	var wait time.Duration
	if args.WaitTimeout != "" {
		d, perr := time.ParseDuration(args.WaitTimeout)
		if perr != nil {
			return errEnvelope("bad_args", "wait_timeout: "+perr.Error()), true
		}
		wait = d
	}
	// accept is pointer-typed so a present-but-empty array ("clear all
	// criteria") is distinguishable from an absent key ("don't touch
	// acceptance"). AcceptSet mirrors the CLI's flag-presence semantics.
	var accept []string
	acceptSet := args.Accept != nil
	if acceptSet {
		accept = *args.Accept
	}
	out, code := cli.RunUpdate(s.repoRoot, cli.UpdateOptions{
		ID:          args.ID,
		Status:      args.Status,
		Priority:    args.Priority,
		Assignee:    args.Assignee,
		Description: args.Description,
		// RunUpdate rejects description + description_append together, so
		// the conflict guard is shared with the CLI rather than duplicated.
		DescriptionAppend: args.DescriptionAppend,
		Accept:            accept,
		AcceptSet:         acceptSet,
		AcceptAdd:         args.AcceptAdd,
		AcceptRm:          args.AcceptRm,
		DepRm:             args.DepRm,
		ExtRm:             args.ExtRm,
		Claim:             args.Claim,
		Unclaim:           args.Unclaim,
		Wait:              args.Wait,
		WaitTimeout:       wait,
		Push:              args.Push,
		NoCommit:          args.NoCommit,
		Isolated:          args.Isolated,
		AsJSON:            true,
		Verify:            args.Verify,
	})
	return out, code != 0
}

func (s *Server) callClose(raw json.RawMessage) (any, bool) {
	var args struct {
		ID       string `json:"id"`
		Reason   string `json:"reason"`
		NoCommit bool   `json:"no_commit"`
		Push     bool   `json:"push"`
		Isolated bool   `json:"isolated"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunClose(s.repoRoot, cli.CloseOptions{
		ID:       args.ID,
		Reason:   args.Reason,
		AsJSON:   true,
		NoCommit: args.NoCommit,
		Push:     args.Push,
		Isolated: args.Isolated,
	})
	return out, code != 0
}

func (s *Server) callDepAdd(raw json.RawMessage) (any, bool) {
	var args struct {
		Child    string   `json:"child"`
		Parent   string   `json:"parent"`
		EdgeType string   `json:"edge_type"`
		External []string `json:"external"`
		NoCommit bool     `json:"no_commit"`
		Push     bool     `json:"push"`
		Isolated bool     `json:"isolated"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	// External-blocker form (act-ce1427): attach opaque refs to `child`;
	// parent/edge_type are ignored. Mirrors `act dep add <id> --external`.
	if len(args.External) > 0 {
		out, code := cli.RunDepAddExternal(s.repoRoot, args.Child, args.External, cli.DepAddOptions{
			AsJSON:   true,
			NoCommit: args.NoCommit,
			Push:     args.Push,
			Isolated: args.Isolated,
		})
		return out, code != 0
	}
	out, code := cli.RunDepAdd(s.repoRoot, cli.DepAddOptions{
		Child:    args.Child,
		Parent:   args.Parent,
		EdgeType: args.EdgeType,
		AsJSON:   true,
		NoCommit: args.NoCommit,
		Push:     args.Push,
		Isolated: args.Isolated,
	})
	return out, code != 0
}

func (s *Server) callReady(raw json.RawMessage) (any, bool) {
	var args struct {
		Under string `json:"under"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunReady(s.repoRoot, cli.ReadyOptions{
		Under:  args.Under,
		Limit:  args.Limit,
		AsJSON: true,
	})
	return out, code != 0
}

func (s *Server) callSearch(raw json.RawMessage) (any, bool) {
	var args struct {
		Query  string `json:"query"`
		In     string `json:"in"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	if strings.TrimSpace(args.Query) == "" {
		return errEnvelope("bad_args", "query is required"), true
	}
	in := args.In
	if in == "" {
		in = "all"
	}
	out, code := cli.RunSearch(s.repoRoot, args.Query, cli.SearchOptions{
		In:     in,
		Status: args.Status,
		Limit:  args.Limit,
		AsJSON: true,
	})
	return out, code != 0
}

func (s *Server) callLog(raw json.RawMessage) (any, bool) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunLog(s.repoRoot, args.ID, true)
	return out, code != 0
}

func (s *Server) callDoctor(raw json.RawMessage) (any, bool) {
	var args struct {
		Check string `json:"check"`
		Fix   bool   `json:"fix"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunDoctor(s.repoRoot, cli.DoctorOptions{
		Check:  args.Check,
		Fix:    args.Fix,
		AsJSON: true,
	})
	return out, code != 0
}

func (s *Server) callVersion(raw json.RawMessage) (any, bool) {
	var args struct {
		CheckRepo bool `json:"check_repo"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunVersion(args.CheckRepo, s.repoRoot)
	return out, code != 0
}
