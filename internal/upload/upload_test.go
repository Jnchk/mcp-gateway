package upload

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jnchk/mcp-gateway/internal/config"
	"github.com/Jnchk/mcp-gateway/internal/registry"
)

type recorded struct {
	method, path, query, auth, contentType string
	body                                   string
	calls                                  int
}

func fakeUpstream(t *testing.T, status int, response string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.calls++
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.Query().Get("filename")
		rec.auth = r.Header.Get("Authorization")
		rec.contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		rec.body = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestUploadForwardsToUpstream(t *testing.T) {
	srv, rec := fakeUpstream(t, http.StatusCreated, `{"id":"tok123","filename":"report.pdf"}`)
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {
			Transport: "http", URL: "http://unused/mcp",
			UploadURL: srv.URL + "/upload",
			Headers:   map[string]string{"Authorization": "Bearer upstream-token"},
		},
	}}, registry.Options{ConnectTimeout: time.Second, CallTimeout: time.Second})
	t.Cleanup(reg.Close)

	h := Handler(reg, "gw-token", 1<<20)
	req := httptest.NewRequest(http.MethodPost, "/upload/red?filename=report.pdf", strings.NewReader("pdf-bytes"))
	req.Header.Set("Authorization", "Bearer gw-token")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "tok123") {
		t.Fatalf("body = %s", rec2.Body.String())
	}
	if rec.method != http.MethodPost || rec.path != "/upload" || rec.query != "report.pdf" {
		t.Fatalf("upstream request = %+v", rec)
	}
	if rec.auth != "Bearer upstream-token" || rec.contentType != "application/octet-stream" || rec.body != "pdf-bytes" {
		t.Fatalf("upstream headers/body = %+v", rec)
	}
}

func TestUploadRequiresGatewayToken(t *testing.T) {
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://unused/mcp"},
	}}, registry.Options{})
	defer reg.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload/red?filename=x", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	Handler(reg, "gw-token", 1<<20).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUploadRejectsUnknownAndStdio(t *testing.T) {
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"local": {Transport: "stdio", Command: []string{"/bin/true"}},
	}}, registry.Options{})
	defer reg.Close()

	h := Handler(reg, "", 1<<20)
	for _, path := range []string{"/upload/nope?filename=x", "/upload/local?filename=x", "/upload/?filename=x"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("x"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
	}
}

func TestUploadRequiresFilename(t *testing.T) {
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h/mcp", UploadURL: "http://h/upload"},
	}}, registry.Options{})
	defer reg.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload/red", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	Handler(reg, "", 1<<20).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUploadEnforcesSizeCap(t *testing.T) {
	srv, rec := fakeUpstream(t, http.StatusCreated, `{}`)
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h/mcp", UploadURL: srv.URL + "/upload"},
	}}, registry.Options{})
	defer reg.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload/red?filename=x", strings.NewReader("0123456789"))
	res := httptest.NewRecorder()
	Handler(reg, "", 4).ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", res.Code)
	}
	if rec.calls != 0 {
		t.Fatalf("upstream was called %d times", rec.calls)
	}
}

func TestUploadPassesUpstreamErrorsThrough(t *testing.T) {
	srv, _ := fakeUpstream(t, http.StatusUnprocessableEntity, `{"errors":["File is too big"]}`)
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h/mcp", UploadURL: srv.URL + "/upload"},
	}}, registry.Options{})
	defer reg.Close()

	req := httptest.NewRequest(http.MethodPost, "/upload/red?filename=x", strings.NewReader("abc"))
	res := httptest.NewRecorder()
	Handler(reg, "", 1<<20).ServeHTTP(res, req)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "File is too big") {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
}

func TestUploadIsPostOnly(t *testing.T) {
	reg := registry.New(&config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h/mcp"},
	}}, registry.Options{})
	defer reg.Close()

	req := httptest.NewRequest(http.MethodGet, "/upload/red?filename=x", nil)
	res := httptest.NewRecorder()
	Handler(reg, "", 1<<20).ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", res.Code)
	}
}
