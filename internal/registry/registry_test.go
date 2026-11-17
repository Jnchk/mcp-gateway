package registry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
)

type fakeSession struct {
	tools  []*mcp.Tool
	call   func(string, any) (*mcp.CallToolResult, error)
	closed atomic.Bool
}

func (f *fakeSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: f.tools}, nil
}

func (f *fakeSession) CallTool(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	if f.call != nil {
		return f.call(p.Name, p.Arguments)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}

func (f *fakeSession) Close() error {
	f.closed.Store(true)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func baseConfig() *config.Config {
	return &config.Config{
		Servers: map[string]config.Upstream{
			"up": {Transport: "http", URL: "http://example.invalid/mcp"},
		},
	}
}

func TestConcurrentCallsShareOneConnection(t *testing.T) {
	var dials atomic.Int32
	release := make(chan struct{})
	dial := func(context.Context, string, config.Upstream) (Session, error) {
		dials.Add(1)
		<-release
		return &fakeSession{}, nil
	}
	reg := New(baseConfig(), Options{Dial: dial, ConnectTimeout: time.Second, CallTimeout: time.Second, SchemaTTL: time.Minute})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := reg.Call(context.Background(), "up", "x", nil); err != nil {
				t.Errorf("call: %v", err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1", got)
	}
}

func TestBackoffBlocksRetryUntilDeadline(t *testing.T) {
	clk := newClock()
	var dials atomic.Int32
	dial := func(context.Context, string, config.Upstream) (Session, error) {
		dials.Add(1)
		return nil, errors.New("connection refused")
	}
	reg := New(baseConfig(), Options{Dial: dial, Now: clk.now, ConnectTimeout: time.Second, CallTimeout: time.Second, SchemaTTL: time.Minute})

	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected a failure")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials during backoff = %d, want 1", got)
	}

	clk.advance(6 * time.Second) // past the first 5s backoff
	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected a failure")
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials after backoff = %d, want 2", got)
	}
}

func TestIdleSweepClosesSession(t *testing.T) {
	sess := &fakeSession{}
	reg := New(baseConfig(), Options{
		Dial:           func(context.Context, string, config.Upstream) (Session, error) { return sess, nil },
		IdleTimeout:    20 * time.Millisecond,
		SweepInterval:  5 * time.Millisecond,
		ConnectTimeout: time.Second,
		CallTimeout:    time.Second,
		SchemaTTL:      time.Minute,
	})
	reg.Start()
	defer reg.Close()

	if _, err := reg.Call(context.Background(), "up", "x", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !sess.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("session was never closed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state := reg.Snapshot()[0].State; state != "idle" {
		t.Fatalf("state = %q, want idle", state)
	}
}

func TestSnapshotStates(t *testing.T) {
	clk := newClock()
	fail := false
	dial := func(context.Context, string, config.Upstream) (Session, error) {
		if fail {
			return nil, errors.New("boom")
		}
		return &fakeSession{}, nil
	}
	reg := New(baseConfig(), Options{Dial: dial, Now: clk.now, ConnectTimeout: time.Second, CallTimeout: time.Second, SchemaTTL: time.Minute})

	if got := reg.Snapshot()[0].State; got != "unknown" {
		t.Fatalf("initial state = %q", got)
	}
	if _, err := reg.Call(context.Background(), "up", "x", nil); err != nil {
		t.Fatal(err)
	}
	if got := reg.Snapshot()[0].State; got != "up" {
		t.Fatalf("state = %q, want up", got)
	}

	fail = true
	reg.Close()
	reg = New(baseConfig(), Options{Dial: dial, Now: clk.now, ConnectTimeout: time.Second, CallTimeout: time.Second, SchemaTTL: time.Minute})
	if _, err := reg.Call(context.Background(), "up", "x", nil); err == nil {
		t.Fatal("expected failure")
	}
	snap := reg.Snapshot()[0]
	if snap.State != "down" || snap.LastError == "" || snap.RetryInSecs == 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
}

func TestAllowed(t *testing.T) {
	cfg := baseConfig()
	cfg.Servers["up"] = config.Upstream{Transport: "http", URL: "http://h", Allow: []string{"read", "list_*"}}
	cfg.Servers["denied"] = config.Upstream{Transport: "http", URL: "http://h", Deny: []string{"write"}}
	reg := New(cfg, Options{Dial: func(context.Context, string, config.Upstream) (Session, error) {
		return &fakeSession{}, nil
	}})

	cases := []struct {
		server, tool string
		want         bool
	}{
		{"up", "read", true},
		{"up", "write", false},
		{"denied", "read", true},
		{"denied", "write", false},
		{"missing", "read", false},
	}
	for _, tc := range cases {
		if got := reg.Allowed(tc.server, tc.tool); got != tc.want {
			t.Errorf("Allowed(%q,%q) = %v, want %v", tc.server, tc.tool, got, tc.want)
		}
	}
}

func TestToolsCachesCatalog(t *testing.T) {
	var listCalls atomic.Int32
	sess := &fakeSession{tools: []*mcp.Tool{{Name: "a"}}}
	sess.call = nil
	dial := func(context.Context, string, config.Upstream) (Session, error) {
		return &countingSession{fakeSession: sess, listCalls: &listCalls}, nil
	}
	reg := New(baseConfig(), Options{Dial: dial, ConnectTimeout: time.Second, CallTimeout: time.Second, SchemaTTL: time.Minute})

	for i := 0; i < 3; i++ {
		tools, err := reg.Tools(context.Background(), "up")
		if err != nil {
			t.Fatal(err)
		}
		if len(tools) != 1 || tools[0].Name != "a" {
			t.Fatalf("tools = %#v", tools)
		}
	}
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("ListTools calls = %d, want 1 (cached)", got)
	}
}

type countingSession struct {
	*fakeSession
	listCalls *atomic.Int32
}

func (c *countingSession) ListTools(ctx context.Context, p *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	c.listCalls.Add(1)
	return c.fakeSession.ListTools(ctx, p)
}
