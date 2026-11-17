package status

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
	"github.com/Jnchk/mcp-gateway/internal/registry"
)

type fakeSession struct{}

func (fakeSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}
func (fakeSession) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}
func (fakeSession) Close() error { return nil }

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	cfg := &config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h"},
	}}
	reg := registry.New(cfg, registry.Options{
		Dial: func(context.Context, string, config.Upstream) (registry.Session, error) {
			return fakeSession{}, nil
		},
		ConnectTimeout: time.Second,
		CallTimeout:    time.Second,
	})
	t.Cleanup(reg.Close)
	return reg
}

func TestDisabledWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(testRegistry(t), "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRequiresToken(t *testing.T) {
	h := Handler(testRegistry(t), "s3cret")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no header = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
}

func TestDocumentShape(t *testing.T) {
	reg := testRegistry(t)
	if _, err := reg.Call(t.Context(), "red", "x", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	Handler(reg, "s3cret").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var doc struct {
		GeneratedAt time.Time `json:"generated_at"`
		Servers     []struct {
			Name      string  `json:"name"`
			State     string  `json:"state"`
			Tools     int     `json:"tools"`
			LatencyMs *int64  `json:"latency_ms"`
			LastOK    *string `json:"last_ok"`
			LastError *string `json:"last_error"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedAt.IsZero() || len(doc.Servers) != 1 {
		t.Fatalf("doc = %#v", doc)
	}
	s := doc.Servers[0]
	if s.Name != "red" || s.State != "up" || s.LastOK == nil {
		t.Fatalf("server = %#v", s)
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatal("token leaked into the document")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(testRegistry(t), "s3cret").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}
