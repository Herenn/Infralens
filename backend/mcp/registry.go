package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"

	"github.com/Herenn/Infralens/backend/service"
	"github.com/Herenn/Infralens/backend/storage"
)

// Deps are the backend services tools read from. Fields other than Topology
// are optional; a tool whose dependency is nil is simply not registered (see
// BuiltinTools), so a feature that is not wired up never shows the model a
// tool that can only fail.
type Deps struct {
	// Topology is the service layer every built-in tool reads through.
	Topology *service.TopologyService

	// Inspections, when set, adds the agent's process fingerprint (process
	// name, listen ports, detected DB/HTTP server) to get_service. It is the
	// store's ServiceRepository in production.
	Inspections InspectionReader
}

// InspectionReader reads a service's deep-inspection record.
type InspectionReader interface {
	GetInspection(ctx context.Context, serviceID string) (*storage.ServiceInspection, error)
}

// Env is what a tool handler receives: the dependencies plus shared helpers
// (topology snapshots, service resolution) defined in resolve.go.
type Env struct {
	Deps
}

// Tool is one MCP tool. Everything an LLM sees about the tool - name,
// description, input schema - lives here next to its implementation.
type Tool struct {
	// Name is the stable identifier the model calls (snake_case).
	Name string
	// Title is a short human-readable label for client UIs.
	Title string
	// Description tells the model when and how to use the tool. Keep it
	// factual; it is part of every prompt that lists the tools.
	Description string
	// InputSchema is a JSON Schema object (see the obj/str/integer helpers).
	InputSchema Schema
	// Run executes the tool with the raw arguments object. Use Typed to get
	// decoded, validated arguments instead of raw JSON.
	Run func(ctx context.Context, env *Env, args json.RawMessage) (any, error)
}

// Schema is a JSON Schema fragment.
type Schema = map[string]any

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (t *Tool) validate() error {
	if !toolNamePattern.MatchString(t.Name) {
		return errors.New("name must be snake_case, 1-64 characters")
	}
	if strings.TrimSpace(t.Description) == "" {
		return errors.New("description is required")
	}
	if t.InputSchema["type"] != "object" {
		return errors.New(`input schema must have "type": "object"`)
	}
	if t.Run == nil {
		return errors.New("Run is required")
	}
	if _, err := json.Marshal(t.InputSchema); err != nil {
		return fmt.Errorf("input schema does not encode: %w", err)
	}
	return nil
}

type toolDefinition struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema Schema          `json:"inputSchema"`
	Annotations toolAnnotations `json:"annotations"`
}

type toolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

func (t *Tool) definition() toolDefinition {
	return toolDefinition{
		Name:        t.Name,
		Title:       t.Title,
		Description: t.Description,
		InputSchema: t.InputSchema,
		// Every InfraLens tool is a read of local state: no side effects, the
		// same call answers the same way until the topology changes, and
		// nothing reaches outside this server.
		Annotations: toolAnnotations{
			Title:          t.Title,
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}
}

// Typed adapts a handler taking decoded arguments. Arguments are decoded
// strictly: an unknown argument name is reported back to the model as a tool
// error listing the valid ones, so a misspelt parameter is corrected rather
// than silently ignored.
func Typed[In any](fn func(ctx context.Context, env *Env, in In) (any, error)) func(context.Context, *Env, json.RawMessage) (any, error) {
	valid := argNames(reflect.TypeOf((*In)(nil)).Elem())
	return func(ctx context.Context, env *Env, raw json.RawMessage) (any, error) {
		var in In
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				te := invalidArgs("invalid arguments: %s; valid arguments are: %s",
					strings.TrimPrefix(err.Error(), "json: "), strings.Join(valid, ", "))
				te.Details = map[string]any{"valid_arguments": valid}
				return nil, te
			}
		}
		return fn(ctx, env, in)
	}
}

// argNames lists the JSON argument names of an argument struct, sorted.
func argNames(t reflect.Type) []string {
	names := []string{}
	if t.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Schema helpers - small on purpose; they cover what the tools here need.
// ---------------------------------------------------------------------------

// obj is an object schema that rejects unknown properties.
func obj(required []string, props map[string]Schema) Schema {
	s := Schema{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) Schema { return Schema{"type": "string", "description": desc} }

func integer(desc string, min, max, def int) Schema {
	return Schema{"type": "integer", "description": desc, "minimum": min, "maximum": max, "default": def}
}

func enum(desc, def string, values ...string) Schema {
	return Schema{"type": "string", "description": desc, "enum": values, "default": def}
}

// ---------------------------------------------------------------------------
// Tool errors
// ---------------------------------------------------------------------------

// ToolError is a failure the model can act on: a bad argument, an ambiguous
// service name, history being disabled. It is returned as a tool result with
// isError=true (not a JSON-RPC error) so clients pass it to the model.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries structured context, e.g. disambiguation candidates.
	Details map[string]any `json:"details,omitempty"`
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

func invalidArgs(format string, args ...any) *ToolError {
	return &ToolError{Code: "invalid_arguments", Message: fmt.Sprintf(format, args...)}
}

// errHistoryDisabled maps service.ErrHistoryDisabled for the model.
var errHistoryDisabled = &ToolError{
	Code:    "history_disabled",
	Message: "topology history is disabled on this InfraLens server (HISTORY_ENABLED=false), so only the current topology can be queried",
}

// asToolError converts known service errors to model-facing ones.
func asToolError(err error) (*ToolError, bool) {
	var te *ToolError
	switch {
	case errors.As(err, &te):
		return te, true
	case errors.Is(err, service.ErrHistoryDisabled):
		return errHistoryDisabled, true
	case errors.Is(err, service.ErrDiffRangeTooLarge):
		return &ToolError{Code: "range_too_large", Message: "the requested time span is longer than this server's history retention allows; narrow the range"}, true
	case errors.Is(err, context.DeadlineExceeded):
		return &ToolError{Code: "timeout", Message: "the query took too long; try a narrower request (smaller depth or limit, or a specific service)"}, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// tools/call
// ---------------------------------------------------------------------------

// maxResultBytes caps one tool result's JSON. Tools page and cap their own
// lists well below this; it is the backstop that keeps a pathological graph
// from flooding the model's context (Claude Code, for one, warns above ~10k
// tokens of tool output).
const maxResultBytes = 48 << 10

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (h *Handler) callTool(ctx context.Context, rawParams json.RawMessage) (map[string]any, *protoError) {
	var p callParams
	if err := json.Unmarshal(rawParams, &p); err != nil || p.Name == "" {
		return nil, newProtoError(http.StatusOK, codeInvalidParams, "Invalid params: tools/call requires a tool name")
	}
	tool, ok := h.toolsByName[p.Name]
	if !ok {
		return nil, newProtoError(http.StatusOK, codeInvalidParams, "Unknown tool: %s", p.Name)
	}
	if args := bytes.TrimSpace(p.Arguments); len(args) > 0 && args[0] != '{' && !bytes.Equal(args, []byte("null")) {
		return nil, newProtoError(http.StatusOK, codeInvalidParams, "Invalid params: arguments must be an object")
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, busyWait)
	select {
	case h.sem <- struct{}{}:
		cancelWait()
	case <-waitCtx.Done():
		cancelWait()
		return toolErrorResult(&ToolError{Code: "busy", Message: "the InfraLens server is handling too many tool calls; retry shortly"}), nil
	}
	defer func() { <-h.sem }()

	callCtx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()

	start := time.Now()
	out, err := h.runTool(callCtx, tool, p.Arguments)
	entry := log.WithFields(log.Fields{"tool": tool.Name, "duration": time.Since(start).Round(time.Millisecond)})

	if err != nil {
		if te, ok := asToolError(err); ok {
			entry.WithField("code", te.Code).Debug("mcp tool call returned a tool error")
			return toolErrorResult(te), nil
		}
		entry.WithError(err).Warn("mcp tool call failed")
		return toolErrorResult(&ToolError{Code: "internal_error", Message: "InfraLens failed to answer this query; see the server log"}), nil
	}

	data, err := json.Marshal(out)
	if err != nil {
		entry.WithError(err).Error("mcp tool result does not encode")
		return toolErrorResult(&ToolError{Code: "internal_error", Message: "InfraLens produced an unencodable result"}), nil
	}
	data = escapeInvisible(data)
	if len(data) > maxResultBytes {
		entry.WithField("bytes", len(data)).Info("mcp tool result over size cap")
		return toolErrorResult(&ToolError{
			Code:    "result_too_large",
			Message: fmt.Sprintf("the result is %d bytes, over the %d-byte cap; lower limit/depth/max_nodes or scope the query to one service", len(data), maxResultBytes),
		}), nil
	}
	entry.Debug("mcp tool call")

	// The same JSON goes in both fields: structuredContent for clients that
	// use it, a text block for those that only forward text (the spec's
	// backwards-compatibility recommendation). Observed data is only ever
	// emitted inside this JSON, never interpolated into prose.
	return map[string]any{
		"content":           []textContent{{Type: "text", Text: string(data)}},
		"structuredContent": json.RawMessage(data),
		"isError":           false,
	}, nil
}

// runTool runs a tool, converting a panic into an error so one bad tool
// cannot take the endpoint down with it.
func (h *Handler) runTool(ctx context.Context, tool *Tool, args json.RawMessage) (out any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic in tool %s: %v", tool.Name, rec)
		}
	}()
	return tool.Run(ctx, h.env, args)
}

// escapeInvisible rewrites every invisible or control character in encoded
// JSON as a \u escape. encoding/json already escapes C0 controls and
// U+2028/2029 but leaves DEL, C1 controls and format characters (bidi
// overrides, zero-width joiners, tag characters) raw. Escaping them keeps the
// output lossless - it decodes to the same strings, so IDs still round-trip -
// while ensuring no such character reaches a terminal or a model's context
// unescaped. Outside strings JSON is pure ASCII, so this only touches string
// contents.
func escapeInvisible(data []byte) []byte {
	needs := false
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if invisible(r) {
			needs = true
			break
		}
		i += size
	}
	if !needs {
		return data
	}
	var b bytes.Buffer
	b.Grow(len(data) + 16)
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if invisible(r) {
			if r > 0xFFFF {
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, r1, r2)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		} else {
			b.Write(data[i : i+size])
		}
		i += size
	}
	return b.Bytes()
}

func invisible(r rune) bool {
	return r == 0x7f || (r >= 0x80 && unicode.IsControl(r)) || unicode.Is(unicode.Cf, r)
}

func toolErrorResult(te *ToolError) map[string]any {
	payload := map[string]any{"error": te}
	data, _ := json.Marshal(payload)
	data = escapeInvisible(data)
	return map[string]any{
		"content":           []textContent{{Type: "text", Text: string(data)}},
		"structuredContent": json.RawMessage(data),
		"isError":           true,
	}
}
