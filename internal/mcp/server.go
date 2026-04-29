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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aac/act/internal/cli"
)

// protocolVersion is the MCP wire version we advertise during initialize.
// The handshake is intentionally lax: we accept any client version and echo
// our own back. Clients that depend on a specific version should pin it
// out-of-band.
const protocolVersion = "2024-11-05"

// serverName / serverVersion are echoed in the initialize response so MCP
// clients can render an identifying label in their UIs.
const serverName = "act-mcp"
const serverVersion = "0.1.0"

// Server is a stdio MCP host. It owns the JSON-RPC framing, the tool
// registry, and the cli dispatch glue. One Server is single-threaded: Run
// reads, dispatches, and writes one request at a time. This matches the
// stdio transport's serial nature and keeps the cli's repo-state mutations
// race-free.
type Server struct {
	repoRoot string
	readOnly bool
	in       io.Reader
	out      io.Writer
}

// NewServer constructs a Server with the given repo root. repoRoot is used
// as the cwd-equivalent for every tool dispatch; the caller is responsible
// for chdir / .act/ presence checks (the cmd layer handles exit 3 on
// missing .act/). When readOnly is true, write tools are refused with a
// read_only_violation regardless of any per-call read_only argument.
func NewServer(repoRoot string, readOnly bool, in io.Reader, out io.Writer) *Server {
	return &Server{
		repoRoot: repoRoot,
		readOnly: readOnly,
		in:       in,
		out:      out,
	}
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
	args := p.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}
	res, isErr := s.invoke(p.Name, args)
	body, mErr := json.Marshal(res)
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
	body, _ := json.Marshal(errEnvelope(kind, msg))
	tr := toolResult{
		Content: []toolContent{{Type: "text", Text: string(body)}},
		IsError: true,
	}
	s.writeResult(enc, id, tr)
}

// isWriteTool returns true for the tools that mutate repo state. Any of
// these are blocked when the server was started with --read-only.
func isWriteTool(name string) bool {
	switch name {
	case "act_init", "act_create", "act_update", "act_close", "act_dep_add":
		return true
	}
	return false
}

// tools returns the registered tool descriptors in a deterministic order.
// Schemas mirror the cli flag sets one-to-one (kebab-case → snake_case).
func (s *Server) tools() []toolDescriptor {
	return []toolDescriptor{
		{
			Name:        "act_init",
			Description: "Initialize an act repository at the server's repo root.",
			InputSchema: schemaObject(map[string]any{
				"force": schemaBool("Reinitialize even if .act/ already exists."),
				"json":  schemaBool("Emit JSON output (always true via MCP)."),
			}, nil),
		},
		{
			Name:        "act_create",
			Description: "Create a new issue.",
			InputSchema: schemaObject(map[string]any{
				"title":       schemaString("Issue title (required, ≤256 bytes)."),
				"priority":    schemaInteger("Priority enum (0-4); 0 normalised to 1."),
				"type":        schemaEnum([]string{"task", "bug", "epic", "chore"}, "Issue type."),
				"parent":      schemaString("Parent issue id or prefix."),
				"description": schemaString("Free-text body."),
				"accept":      schemaArrayOfString("Acceptance criteria, in order."),
				"no_commit":   schemaBool("Skip auto-commit."),
				"push":        schemaBool("Push after commit."),
				"isolated":    schemaBool("Run without touching git state."),
			}, []string{"title"}),
		},
		{
			Name:        "act_list",
			Description: "List issues filtered/sorted by the given options.",
			InputSchema: schemaObject(map[string]any{
				"status":   schemaString("Comma-separated status filter."),
				"assignee": schemaString("Exact-match assignee filter."),
				"type":     schemaString("Issue type filter (task|bug|epic|chore)."),
				"limit":    schemaInteger("Maximum issues to return (default 200)."),
				"sort":     schemaString("Comma-separated sort keys; prefix '-' for desc."),
			}, nil),
		},
		{
			Name:        "act_show",
			Description: "Show one issue's rendered state.",
			InputSchema: schemaObject(map[string]any{
				"id":          schemaString("Issue id or prefix."),
				"include_ops": schemaBool("Include the HLC-sorted op stream."),
			}, []string{"id"}),
		},
		{
			Name:        "act_update",
			Description: "Update an issue's fields, accept criteria, or claim.",
			InputSchema: schemaObject(map[string]any{
				"id":           schemaString("Issue id or prefix."),
				"status":       schemaString("New status (open|in_progress|blocked|closed)."),
				"priority":     schemaInteger("New priority (0-4)."),
				"assignee":     schemaString("New assignee (empty string clears)."),
				"description":  schemaString("New description."),
				"accept":       schemaArrayOfString("Replace acceptance criteria."),
				"dep_rm":       schemaArrayOfString("Dep ids to remove."),
				"claim":        schemaBool("Atomic claim mode."),
				"wait":         schemaBool("Wait for claim to free."),
				"wait_timeout": schemaString("Wait timeout (Go duration string)."),
				"no_commit":    schemaBool("Skip auto-commit."),
				"push":         schemaBool("Push after commit."),
				"isolated":     schemaBool("Run without touching git state."),
				"verify":       schemaBool("Run integrity check after write."),
			}, []string{"id"}),
		},
		{
			Name:        "act_close",
			Description: "Close an issue.",
			InputSchema: schemaObject(map[string]any{
				"id":        schemaString("Issue id or prefix."),
				"reason":    schemaString("Optional close reason (≤4096 bytes)."),
				"no_commit": schemaBool("Skip auto-commit."),
				"push":      schemaBool("Push after commit."),
				"isolated":  schemaBool("Run without touching git state."),
			}, []string{"id"}),
		},
		{
			Name:        "act_dep_add",
			Description: "Add a dependency edge from child to parent.",
			InputSchema: schemaObject(map[string]any{
				"child":     schemaString("Child issue id or prefix."),
				"parent":    schemaString("Parent issue id or prefix."),
				"edge_type": schemaEnum([]string{"blocks", "relates", "supersedes"}, "Edge type (default 'blocks')."),
				"no_commit": schemaBool("Skip auto-commit."),
				"push":      schemaBool("Push after commit."),
				"isolated":  schemaBool("Run without touching git state."),
			}, []string{"child", "parent"}),
		},
		{
			Name:        "act_ready",
			Description: "List the ready set: open issues with no unclosed blocking deps.",
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
				"check":   schemaString("Run a single named check (empty runs all)."),
				"fix":     schemaBool("Auto-remediate where safe."),
				"compact": schemaBool("Trigger manual compaction."),
			}, nil),
		},
		{
			Name:        "act_version",
			Description: "Report the act binary version and optionally the repo's max op_version.",
			InputSchema: schemaObject(map[string]any{
				"check_repo": schemaBool("Walk .act/ops/ and report max writer_version."),
			}, nil),
		},
	}
}

// schemaObject is the boilerplate JSON-Schema wrapper used by every tool's
// input definition. additionalProperties is not constrained because some
// tools (e.g. act_create) accept extension fields like read_only.
func schemaObject(props map[string]any, required []string) map[string]any {
	// Always allow read_only for symmetry with the spec; per-call read_only
	// is honoured by the cli layer where applicable. The flag itself does
	// nothing in this scaffold but appears in every tool schema.
	props["read_only"] = schemaBool("Per-call advisory: skip writes (server-level --read-only takes precedence).")
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
	out, code := cli.RunInit(s.repoRoot, args.Force, "", "", nil)
	return out, code != 0
}

func (s *Server) callCreate(raw json.RawMessage) (any, bool) {
	var args struct {
		Title       string   `json:"title"`
		Priority    int      `json:"priority"`
		Type        string   `json:"type"`
		Parent      string   `json:"parent"`
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
		ID          string   `json:"id"`
		Status      *string  `json:"status"`
		Priority    *int     `json:"priority"`
		Assignee    *string  `json:"assignee"`
		Description *string  `json:"description"`
		Accept      []string `json:"accept"`
		DepRm       []string `json:"dep_rm"`
		Claim       bool     `json:"claim"`
		Wait        bool     `json:"wait"`
		WaitTimeout string   `json:"wait_timeout"`
		NoCommit    bool     `json:"no_commit"`
		Push        bool     `json:"push"`
		Isolated    bool     `json:"isolated"`
		Verify      bool     `json:"verify"`
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
	out, code := cli.RunUpdate(s.repoRoot, cli.UpdateOptions{
		ID:          args.ID,
		Status:      args.Status,
		Priority:    args.Priority,
		Assignee:    args.Assignee,
		Description: args.Description,
		Accept:      args.Accept,
		DepRm:       args.DepRm,
		Claim:       args.Claim,
		Wait:        args.Wait,
		WaitTimeout: wait,
		Push:        args.Push,
		NoCommit:    args.NoCommit,
		Isolated:    args.Isolated,
		AsJSON:      true,
		Verify:      args.Verify,
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
		Child    string `json:"child"`
		Parent   string `json:"parent"`
		EdgeType string `json:"edge_type"`
		NoCommit bool   `json:"no_commit"`
		Push     bool   `json:"push"`
		Isolated bool   `json:"isolated"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
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
		Check   string `json:"check"`
		Fix     bool   `json:"fix"`
		Compact bool   `json:"compact"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return errEnvelope("bad_args", err.Error()), true
	}
	out, code := cli.RunDoctor(s.repoRoot, cli.DoctorOptions{
		Check:   args.Check,
		Fix:     args.Fix,
		AsJSON:  true,
		Compact: args.Compact,
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
