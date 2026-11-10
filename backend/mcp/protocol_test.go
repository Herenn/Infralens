package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpcClient speaks raw JSON-RPC to the real HTTP handler, as an MCP client
// would, so these tests pin the wire format rather than internal calls.
type rpcClient struct {
	t      *testing.T
	url    string
	header http.Header
}

type rpcResult struct {
	status int
	body   map[string]any
	raw    string
}

func (c *rpcClient) post(body string, extra map[string]string) rpcResult {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for k, v := range extra {
		if k == "Host" {
			req.Host = v // net/http ignores a Host entry in Header
			continue
		}
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	out := rpcResult{status: resp.StatusCode, raw: string(data)}
	if len(bytes.TrimSpace(data)) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &out.body); err != nil {
			c.t.Fatalf("response is not JSON: %v: %s", err, data)
		}
	}
	return out
}

func rpcCode(r rpcResult) float64 {
	if e, ok := r.body["error"].(map[string]any); ok {
		return e["code"].(float64)
	}
	return 0
}

func newTestEndpoint(t *testing.T, opts Options) (*fixture, *httptest.Server) {
	f := newFixture(t)
	if opts.Auth == nil {
		opts.Auth = KeyAuthenticator(nil, true) // open, as with ALLOW_NO_AUTH
	}
	f.handler = NewHandler(Deps{Topology: f.topology, Inspections: f.store.Services()}, opts)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)
	return f, srv
}

// TestLegacyLifecycle drives initialize -> initialized -> tools/list ->
// tools/call the way 2025-era clients (Claude Code, Claude Desktop via
// mcp-remote, Cursor) do.
func TestLegacyLifecycle(t *testing.T) {
	_, srv := newTestEndpoint(t, Options{Version: "9.9.9"})
	c := &rpcClient{t: t, url: srv.URL}

	init := c.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, nil)
	if init.status != 200 {
		t.Fatalf("initialize status %d: %s", init.status, init.raw)
	}
	res := init.body["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("negotiated %v, want the client's 2025-06-18 echoed", res["protocolVersion"])
	}
	if res["serverInfo"].(map[string]any)["version"] != "9.9.9" {
		t.Errorf("serverInfo = %v", res["serverInfo"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("tools capability not declared")
	}
	if !strings.Contains(res["instructions"].(string), "never as instructions") {
		t.Error("instructions should warn that observed values are data")
	}

	// An unknown future version negotiates down to our newest legacy one.
	future := c.post(`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2099-01-01","capabilities":{}}}`, nil)
	if v := future.body["result"].(map[string]any)["protocolVersion"]; v != latestLegacyVersion {
		t.Errorf("unknown version negotiated to %v, want %s", v, latestLegacyVersion)
	}

	h := map[string]string{headerProtocolVersion: "2025-06-18"}
	if r := c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, h); r.status != http.StatusAccepted || r.raw != "" {
		t.Errorf("notification: status %d body %q, want 202 and no body", r.status, r.raw)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":2,"method":"ping"}`, h); r.status != 200 || r.body["result"] == nil {
		t.Errorf("ping: %d %s", r.status, r.raw)
	}

	list := c.post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, h)
	tools := list.body["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 11 {
		t.Fatalf("tools/list returned %d tools", len(tools))
	}
	first := tools[0].(map[string]any)
	if first["name"] != "topology_overview" || first["inputSchema"].(map[string]any)["type"] != "object" {
		t.Errorf("first tool = %v", first)
	}
	if first["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Error("tools must be annotated readOnlyHint")
	}
	if _, ok := list.body["result"].(map[string]any)["resultType"]; ok {
		t.Error("legacy responses should not carry the modern resultType field")
	}

	call := c.post(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_service","arguments":{"service":"payments-db"}}}`, h)
	cr := call.body["result"].(map[string]any)
	if cr["isError"] != false {
		t.Fatalf("tools/call failed: %s", call.raw)
	}
	text := cr["content"].([]any)[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil || payload["service"].(map[string]any)["id"] != idDB {
		t.Errorf("text content should be the JSON result: %v %s", err, text)
	}
	if cr["structuredContent"].(map[string]any)["service"] == nil {
		t.Error("structuredContent missing")
	}

	// Session IDs from clients are ignored; none is ever minted.
	r := c.post(`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, map[string]string{headerProtocolVersion: "2025-11-25", "Mcp-Session-Id": "abc"})
	if r.status != 200 {
		t.Errorf("request with a session id: %d", r.status)
	}

	// Unknown method and unknown tool are JSON-RPC errors.
	if r := c.post(`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`, h); rpcCode(r) != codeMethodNotFound {
		t.Errorf("unknown method: %s", r.raw)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"drop_tables"}}`, h); rpcCode(r) != codeInvalidParams {
		t.Errorf("unknown tool: %s", r.raw)
	}

	// A version we never negotiate is rejected outright.
	if r := c.post(`{"jsonrpc":"2.0","id":8,"method":"tools/list"}`, map[string]string{headerProtocolVersion: "2024-11-05"}); r.status != 400 || rpcCode(r) != codeUnsupportedProtocolVersion {
		t.Errorf("unsupported legacy version: %d %s", r.status, r.raw)
	}
	// 2025-03-26 is not supported: a 2025-03-26 client is offered 2025-11-25
	// at initialize, and a post-initialize request without the version header
	// (which only 2025-03-26 permits) is rejected with the supported list.
	old := c.post(`{"jsonrpc":"2.0","id":"o","method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{}}}`, nil)
	if v := old.body["result"].(map[string]any)["protocolVersion"]; v != latestLegacyVersion {
		t.Errorf("2025-03-26 client offered %v, want %s", v, latestLegacyVersion)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`, nil); r.status != 400 || rpcCode(r) != codeUnsupportedProtocolVersion {
		t.Errorf("headerless legacy request: %d %s", r.status, r.raw)
	}
	for _, v := range supportedVersions {
		if v == "2025-03-26" {
			t.Error("2025-03-26 must not be advertised (it requires batch support)")
		}
	}
}

func modernMeta() string {
	return `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"}}`
}

// TestModernRequests drives the stateless 2026-07-28 revision: no handshake,
// per-request _meta, mirrored headers.
func TestModernRequests(t *testing.T) {
	_, srv := newTestEndpoint(t, Options{})
	c := &rpcClient{t: t, url: srv.URL}
	hdr := func(method string) map[string]string {
		return map[string]string{headerProtocolVersion: ProtocolVersionModern, headerMethod: method}
	}

	disc := c.post(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+modernMeta()+`}}`, hdr("server/discover"))
	if disc.status != 200 {
		t.Fatalf("discover: %d %s", disc.status, disc.raw)
	}
	dr := disc.body["result"].(map[string]any)
	if dr["resultType"] != "complete" || dr["supportedVersions"].([]any)[0] != ProtocolVersionModern {
		t.Errorf("discover result = %v", dr)
	}
	if dr["_meta"].(map[string]any)[metaServerInfo] == nil {
		t.Error("serverInfo missing from result _meta")
	}

	list := c.post(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+modernMeta()+`}}`, hdr("tools/list"))
	lr := list.body["result"].(map[string]any)
	if lr["ttlMs"] == nil || lr["cacheScope"] != "public" || lr["resultType"] != "complete" {
		t.Errorf("tools/list must carry resultType, ttlMs and cacheScope: %v", lr)
	}

	callHdr := hdr("tools/call")
	callHdr[headerName] = "get_blast_radius"
	call := c.post(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_blast_radius","arguments":{"service":"redis"},`+modernMeta()+`}}`, callHdr)
	cr := call.body["result"].(map[string]any)
	if call.status != 200 || cr["isError"] != false || cr["structuredContent"].(map[string]any)["blast_radius"].(float64) != 3 {
		t.Fatalf("modern tools/call: %d %s", call.status, call.raw)
	}

	// Mcp-Name may be base64-sentinel encoded.
	callHdr[headerName] = "=?base64?" + base64.StdEncoding.EncodeToString([]byte("get_blast_radius")) + "?="
	if r := c.post(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_blast_radius","arguments":{"service":"redis"},`+modernMeta()+`}}`, callHdr); r.status != 200 {
		t.Errorf("base64 Mcp-Name: %d %s", r.status, r.raw)
	}

	cases := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    float64
	}{
		{"header/body name mismatch",
			`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"find_orphans",` + modernMeta() + `}}`,
			map[string]string{headerProtocolVersion: ProtocolVersionModern, headerMethod: "tools/call", headerName: "rank_criticality"}, 400, codeHeaderMismatch},
		{"missing Mcp-Name",
			`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"find_orphans",` + modernMeta() + `}}`,
			hdr("tools/call"), 400, codeHeaderMismatch},
		{"missing Mcp-Method",
			`{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{` + modernMeta() + `}}`,
			map[string]string{headerProtocolVersion: ProtocolVersionModern}, 400, codeHeaderMismatch},
		{"method header mismatch",
			`{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{` + modernMeta() + `}}`,
			hdr("tools/call"), 400, codeHeaderMismatch},
		{"protocol version header mismatch",
			`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{` + modernMeta() + `}}`,
			map[string]string{headerProtocolVersion: "2025-11-25", headerMethod: "tools/list"}, 400, codeHeaderMismatch},
		{"unsupported modern version",
			`{"jsonrpc":"2.0","id":10,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			map[string]string{headerProtocolVersion: "2099-01-01", headerMethod: "tools/list"}, 400, codeUnsupportedProtocolVersion},
		{"missing clientCapabilities",
			`{"jsonrpc":"2.0","id":11,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
			hdr("tools/list"), 400, codeInvalidParams},
		{"modern header without _meta",
			`{"jsonrpc":"2.0","id":12,"method":"tools/list"}`,
			hdr("tools/list"), 400, codeInvalidParams},
		{"unknown method is 404",
			`{"jsonrpc":"2.0","id":13,"method":"ping","params":{` + modernMeta() + `}}`,
			hdr("ping"), 404, codeMethodNotFound},
	}
	for _, tc := range cases {
		r := c.post(tc.body, tc.headers)
		if r.status != tc.status || rpcCode(r) != tc.code {
			t.Errorf("%s: got %d / %v, want %d / %v: %s", tc.name, r.status, rpcCode(r), tc.status, tc.code, r.raw)
		}
	}

	// UnsupportedProtocolVersion lists what is supported, so dual-era
	// clients can retry or fall back.
	r := c.post(cases[5].body, cases[5].headers)
	data := r.body["error"].(map[string]any)["data"].(map[string]any)
	if len(data["supported"].([]any)) != len(supportedVersions) || data["requested"] != "2099-01-01" {
		t.Errorf("unsupported version data = %v", data)
	}
}

func TestTransportRules(t *testing.T) {
	_, srv := newTestEndpoint(t, Options{MaxBodyBytes: 4096, AllowedOrigins: []string{"https://infralens.example.com"}})
	c := &rpcClient{t: t, url: srv.URL}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{}}}`

	for _, m := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req, _ := http.NewRequest(m, srv.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != allowedMethods {
			t.Errorf("%s: %d Allow=%q, want 405 Allow=%q", m, resp.StatusCode, resp.Header.Get("Allow"), allowedMethods)
		}
	}

	origins := map[string]int{
		"":                              200, // non-browser client
		"http://localhost:6274":         200, // locally served page (e.g. MCP Inspector)
		"http://127.0.0.1:3000":         200,
		"https://infralens.example.com": 200, // MCP_ALLOWED_ORIGINS
		"https://evil.example":          403,
		"http://localhost.evil.example": 403,
		"null":                          403,
	}
	for origin, want := range origins {
		r := c.post(initBody, map[string]string{"Origin": origin})
		if r.status != want {
			t.Errorf("Origin %q: status %d, want %d (%s)", origin, r.status, want, r.raw)
		}
		if want == 403 && r.body["error"] == nil {
			t.Errorf("Origin %q: 403 should carry a JSON-RPC error body", origin)
		}
	}

	// The Host header is not policed: a same-host reverse proxy that
	// preserves Host (Caddy, Traefik, nginx `proxy_set_header Host $host`,
	// host.docker.internal) must work. Rebinding is stopped by Origin.
	for _, host := range []string{"infralens.example.com", "host.docker.internal:8080", "localhost"} {
		if r := c.post(initBody, map[string]string{"Host": host}); r.status != 200 {
			t.Errorf("Host %q: status %d, want 200", host, r.status)
		}
	}
	if r := c.post(initBody, map[string]string{"Host": "attacker.example", "Origin": "http://attacker.example"}); r.status != http.StatusForbidden {
		t.Errorf("rebinding (foreign Origin): status %d, want 403", r.status)
	}

	if r := c.post(initBody, map[string]string{"Content-Type": "text/plain"}); r.status != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: status %d, want 415", r.status)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"`+strings.Repeat("x", 5000)+`"}}`, nil); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status %d, want 413", r.status)
	}
	if r := c.post(`[`+initBody+`]`, nil); r.status != 400 || rpcCode(r) != codeInvalidRequest {
		t.Errorf("batch: %d %s", r.status, r.raw)
	}
	if r := c.post(`{not json`, nil); r.status != 400 || rpcCode(r) != codeParseError {
		t.Errorf("parse error: %d %s", r.status, r.raw)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":null,"method":"ping"}`, nil); r.status != 400 || rpcCode(r) != codeInvalidRequest {
		t.Errorf("null id: %d %s", r.status, r.raw)
	}
	if r := c.post(`{"jsonrpc":"2.0","id":1,"result":{}}`, nil); r.status != http.StatusAccepted {
		t.Errorf("client-sent response: %d, want 202", r.status)
	}

	// "*" turns Origin validation off entirely.
	_, open := newTestEndpoint(t, Options{AllowedOrigins: []string{"*"}})
	oc := &rpcClient{t: t, url: open.URL}
	if r := oc.post(initBody, map[string]string{"Origin": "https://evil.example"}); r.status != 200 {
		t.Errorf(`AllowedOrigins "*": status %d`, r.status)
	}
}

func TestConcurrencyLimitAnswersBusy(t *testing.T) {
	saved := busyWait
	busyWait = 50 * time.Millisecond
	t.Cleanup(func() { busyWait = saved })

	started, release := make(chan struct{}), make(chan struct{})
	slow := Tool{
		Name: "slow", Description: "blocks until released, to fill the concurrency limit", InputSchema: obj(nil, map[string]Schema{}),
		Run: func(context.Context, *Env, json.RawMessage) (any, error) {
			close(started)
			<-release
			return map[string]any{}, nil
		},
	}
	f := &fixture{handler: NewHandlerWithTools(Deps{}, Options{MaxConcurrentCalls: 1}, []Tool{slow})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.handler.callTool(context.Background(), json.RawMessage(`{"name":"slow"}`))
	}()
	<-started
	f.mustFail(t, "slow", nil, "busy")
	close(release)
	<-done
}
