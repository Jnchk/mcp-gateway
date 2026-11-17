package registry

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
)

func TestPrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{name: "red", w: &buf}

	w.Write([]byte("first line\nsecond"))
	w.Write([]byte(" line\n"))
	if got := buf.String(); got != "[red] first line\n[red] second line\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestDefaultDialReportsMissingBinary(t *testing.T) {
	dial := defaultDial(nil)
	_, err := dial(context.Background(), "x", config.Upstream{
		Transport: "stdio",
		Command:   []string{"/nonexistent/definitely-not-here"},
	})
	if err == nil {
		t.Fatal("expected an error for a missing command")
	}
}

func TestNamesAndHasServer(t *testing.T) {
	reg := New(baseConfig(), Options{})
	if got := reg.Names(); len(got) != 1 || got[0] != "up" {
		t.Fatalf("names = %v", got)
	}
	if !reg.HasServer("up") || reg.HasServer("nope") {
		t.Fatal("HasServer is wrong")
	}
}

func TestTransportErrorDropsSessionAndRetriesAfterBackoff(t *testing.T) {
	clk := newClock()
	var dials atomicInt
	fail := true
	sess := func() *fakeSession {
		return &fakeSession{call: func(string, any) (*mcp.CallToolResult, error) {
			if fail {
				return nil, errors.New("pipe closed")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		}}
	}
	reg := New(baseConfig(), Options{
		Dial: func(context.Context, string, config.Upstream) (Session, error) {
			dials.Inc()
			return sess(), nil
		},
		Now:            clk.now,
		ConnectTimeout: time.Second,
		CallTimeout:    time.Second,
		SchemaTTL:      time.Minute,
	})

	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected a transport error")
	}
	if got := reg.Snapshot()[0].State; got != "down" {
		t.Fatalf("state = %q, want down", got)
	}

	// Backoff still gates the retry.
	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected the backoff error")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials during backoff = %d", got)
	}

	fail = false
	clk.advance(6 * time.Second)
	if _, err := reg.Call(context.Background(), "up", "x", nil); err != nil {
		t.Fatal(err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials after backoff = %d, want 2", got)
	}
	if got := reg.Snapshot()[0].State; got != "up" {
		t.Fatalf("state = %q, want up", got)
	}
}

func TestToolsPagination(t *testing.T) {
	paged := &pagedSession{}
	reg := New(baseConfig(), Options{
		Dial:           func(context.Context, string, config.Upstream) (Session, error) { return paged, nil },
		ConnectTimeout: time.Second,
		CallTimeout:    time.Second,
		SchemaTTL:      time.Minute,
	})
	tools, err := reg.Tools(context.Background(), "up")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "one" || tools[1].Name != "two" {
		t.Fatalf("tools = %#v", tools)
	}
	if paged.calls != 2 {
		t.Fatalf("pages fetched = %d, want 2", paged.calls)
	}
}

type atomicInt struct {
	mu sync.Mutex
	n  int
}

func (a *atomicInt) Inc()      { a.mu.Lock(); a.n++; a.mu.Unlock() }
func (a *atomicInt) Load() int { a.mu.Lock(); defer a.mu.Unlock(); return a.n }

type pagedSession struct{ calls int }

func (p *pagedSession) ListTools(_ context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	p.calls++
	if params.Cursor == "" {
		return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "one"}}, NextCursor: "next"}, nil
	}
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "two"}}}, nil
}

func (p *pagedSession) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func (p *pagedSession) Close() error { return nil }

func TestHeaderRoundTripper(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	client := &http.Client{Transport: headerRoundTripper{
		base:    http.DefaultTransport,
		headers: map[string]string{"Authorization": "Bearer t"},
	}}
	res, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got != "Bearer t" {
		t.Fatalf("header = %q", got)
	}
}
