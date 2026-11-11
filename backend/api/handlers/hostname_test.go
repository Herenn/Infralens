package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func postEvents(t *testing.T, ts *testServer, events ...map[string]interface{}) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"node_name": "node-1",
		"timestamp": time.Now().Format(time.RFC3339),
		"events":    events,
	})
	req := httptest.NewRequest("POST", "/api/v1/events", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("POST /events: %d %s", rr.Code, rr.Body.String())
	}
}

func outboundEvent(src, comm, dst string, port int, hostname string) map[string]interface{} {
	ev := map[string]interface{}{
		"pid": 1, "comm": comm, "src_addr": src, "dst_addr": dst, "dst_port": port, "direction": 0,
	}
	if hostname != "" {
		ev["dst_hostname"] = hostname
	}
	return ev
}

type apiService struct {
	ID        string   `json:"id"`
	PodIP     string   `json:"pod_ip"`
	Hostname  string   `json:"hostname"`
	Hostnames []string `json:"hostnames"`
}

type apiConnection struct {
	ID        string   `json:"id"`
	Hostnames []string `json:"hostnames"`
}

type apiTopology struct {
	Services    []apiService    `json:"services"`
	Connections []apiConnection `json:"connections"`
}

func getJSON(t *testing.T, ts *testServer, path string, out interface{}) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
}

func getText(t *testing.T, ts *testServer, path string) string {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func findService(t *testing.T, topo apiTopology, id string) apiService {
	t.Helper()
	for _, s := range topo.Services {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("service %s missing", id)
	return apiService{}
}

func TestHostnameEndToEnd(t *testing.T) {
	ts := newTestServerWithHistory(t)
	postEvents(t, ts,
		outboundEvent("10.0.2.13", "payment-worker", "52.1.2.3", 443, "api.stripe.com"),
		// An old agent: no dst_hostname at all.
		outboundEvent("10.0.2.12", "orders", "10.0.3.10", 5432, ""),
	)

	var topo apiTopology
	getJSON(t, ts, "/api/v1/topology", &topo)
	stripe := findService(t, topo, "52.1.2.3:443")
	if stripe.Hostname != "api.stripe.com" || len(stripe.Hostnames) != 1 || stripe.PodIP != "52.1.2.3" {
		t.Errorf("stripe service = %+v", stripe)
	}
	if db := findService(t, topo, "10.0.3.10:5432"); db.Hostname != "" || db.Hostnames != nil {
		t.Errorf("nameless service got %+v", db)
	}
	var edgeOK bool
	for _, c := range topo.Connections {
		if c.ID == "10.0.2.13/payment-worker->52.1.2.3:443:443" {
			edgeOK = len(c.Hostnames) == 1 && c.Hostnames[0] == "api.stripe.com"
		}
	}
	if !edgeOK {
		t.Errorf("connection hostnames missing: %+v", topo.Connections)
	}

	// Single-service read (also what WebSocket deltas use).
	var svc apiService
	getJSON(t, ts, "/api/v1/services/"+url.PathEscape("52.1.2.3:443"), &svc)
	if svc.Hostname != "api.stripe.com" {
		t.Errorf("GET /services/{id} hostname = %q", svc.Hostname)
	}
	var list []apiService
	getJSON(t, ts, "/api/v1/services", &list)
	var listed bool
	for _, s := range list {
		if s.ID == "52.1.2.3:443" {
			listed = s.Hostname == "api.stripe.com"
		}
	}
	if !listed {
		t.Error("GET /services lacks hostname")
	}

	// History reconstruction. RFC 3339 here has whole seconds, so ask for a
	// moment just after the (sub-second) observation rather than truncating
	// to before it. Local time, like the existing ?at= tests: see the note on
	// SQLite time comparison in the report.
	var past apiTopology
	getJSON(t, ts, "/api/v1/topology?at="+url.QueryEscape(time.Now().Add(2*time.Second).Format(time.RFC3339)), &past)
	if s := findService(t, past, "52.1.2.3:443"); s.Hostname != "api.stripe.com" {
		t.Errorf("historical hostname = %q", s.Hostname)
	}

	// Exports.
	mermaid := getText(t, ts, "/api/v1/topology/export?format=mermaid")
	if !strings.Contains(mermaid, `["api.stripe.com [52.1.2.3] (HTTPS)"]`) {
		t.Errorf("mermaid export lacks hostname label:\n%s", mermaid)
	}
	dot := getText(t, ts, "/api/v1/topology/export?format=dot")
	if !strings.Contains(dot, `label="api.stripe.com [52.1.2.3] (HTTPS)"`) {
		t.Errorf("dot export lacks hostname label:\n%s", dot)
	}
	// Nameless nodes keep their old labels.
	if !strings.Contains(dot, `label="10.0.3.10 (PostgreSQL)"`) {
		t.Errorf("nameless node label changed:\n%s", dot)
	}
}

// A shared (CDN) address reached under different names by different callers:
// the node shows the name with a "+N", and the edge whose name differs from
// the node's says which name it used.
func TestHostnameExportSharedAddress(t *testing.T) {
	ts := newTestServer(t)
	postEvents(t, ts,
		outboundEvent("10.0.1.1", "a", "104.16.0.1", 443, "cdn.example.com"),
		outboundEvent("10.0.1.2", "b", "104.16.0.1", 443, "cdn.example.com"),
		outboundEvent("10.0.1.3", "c", "104.16.0.1", 443, "assets.example.org"),
	)
	dot := getText(t, ts, "/api/v1/topology/export?format=dot")
	if !strings.Contains(dot, `label="cdn.example.com +1 [104.16.0.1] (HTTPS)"`) {
		t.Errorf("node label:\n%s", dot)
	}
	if !strings.Contains(dot, `"10.0.1.3/c" -> "104.16.0.1:443" [label=":443 assets.example.org"]`) {
		t.Errorf("edge label for the differing caller:\n%s", dot)
	}
	if !strings.Contains(dot, `"10.0.1.1/a" -> "104.16.0.1:443" [label=":443"]`) {
		t.Errorf("edge label for a caller matching the node:\n%s", dot)
	}
}

// Hostile names from an agent are rejected before storage, so nothing has to
// escape them downstream.
func TestHostnameHostileInputRejected(t *testing.T) {
	ts := newTestServer(t)
	postEvents(t, ts, outboundEvent("10.0.1.1", "a", "52.1.2.3", 443, `x"]; click svc_a "http://evil`))
	var topo apiTopology
	getJSON(t, ts, "/api/v1/topology", &topo)
	if s := findService(t, topo, "52.1.2.3:443"); s.Hostname != "" {
		t.Errorf("hostile hostname stored: %q", s.Hostname)
	}
}
