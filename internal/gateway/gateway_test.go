package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
	"github.com/Jnchk/mcp-gateway/internal/registry"
)

type fakeSession struct {
	tools []*mcp.Tool
	call  func(string, any) (*mcp.CallToolResult, error)
}

func (f *fakeSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: f.tools}, nil
}

func (f *fakeSession) CallTool(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return f.call(p.Name, p.Arguments)
}

func (f *fakeSession) Close() error { return nil }

func fixtureTools() []*mcp.Tool {
	return []*mcp.Tool{
		{
			Name:        "search_issues",
			Description: "Find issues. Lots of extra prose that should be truncated from the index.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project": map[string]any{"type": "string"},
					"limit":   map[string]any{"type": "integer"},
				},
				"required": []string{"project"},
			},
		},
		{Name: "get_issue", Description: "Read one issue."},
		{Name: "create_issue", Description: "Create an issue."},
	}
}

func testRegistry(t *testing.T, call func(string, any) (*mcp.CallToolResult, error), upstream config.Upstream) *registry.Registry {
	t.Helper()
	cfg := &config.Config{Servers: map[string]config.Upstream{"red": upstream}}
	reg := registry.New(cfg, registry.Options{
		Dial: func(context.Context, string, config.Upstream) (registry.Session, error) {
			return &fakeSession{tools: fixtureTools(), call: call}, nil
		},
		ConnectTimeout: time.Second,
		CallTimeout:    time.Second,
		SchemaTTL:      time.Minute,
	})
	t.Cleanup(reg.Close)
	return reg
}

func newClient(t *testing.T, reg *registry.Registry) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "gw", Version: "test"}, nil)
	Register(srv, New(reg))

	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "c"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("%s: no content", name)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func TestListToolsCompact(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	got, isErr := call(t, cs, "list_tools", map[string]any{"server": "red"})
	if isErr {
		t.Fatalf("list_tools: %s", got)
	}
	for _, want := range []string{
		"# tool | args | description",
		"search_issues | limit?, project |",
		"# 3 of 3 tools on red",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestListToolsFullAndFilter(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	got, isErr := call(t, cs, "list_tools", map[string]any{"server": "red", "q": "search", "full": true})
	if isErr {
		t.Fatalf("list_tools: %s", got)
	}
	if !strings.Contains(got, `"inputSchema"`) || !strings.Contains(got, `"name":"search_issues"`) {
		t.Fatalf("full schema missing:\n%s", got)
	}
	if strings.Contains(got, "get_issue") {
		t.Fatalf("q filter did not apply:\n%s", got)
	}
}

func TestCallToolPassesArgsThrough(t *testing.T) {
	var gotArgs any
	reg := testRegistry(t, func(_ string, args any) (*mcp.CallToolResult, error) {
		gotArgs = args
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found 2"}}}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	text, isErr := call(t, cs, "call_tool", map[string]any{
		"server": "red", "tool": "search_issues",
		"arguments": map[string]any{"project": "web", "limit": 3},
	})
	if isErr || text != "found 2" {
		t.Fatalf("call_tool = %q (err=%v)", text, isErr)
	}
	args, ok := gotArgs.(map[string]any)
	if !ok || args["project"] != "web" || args["limit"] != float64(3) {
		t.Fatalf("upstream args = %#v", gotArgs)
	}
}

func TestCallToolTagsUpstreamErrors(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
		}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	text, isErr := call(t, cs, "call_tool", map[string]any{"server": "red", "tool": "get_issue"})
	if !isErr {
		t.Fatalf("expected IsError, got %q", text)
	}
	if !strings.HasPrefix(text, "[red/get_issue] ") || !strings.Contains(text, "boom") {
		t.Fatalf("missing attribution: %q", text)
	}
}

func TestDenyBlocksBeforeConnecting(t *testing.T) {
	dials := 0
	cfg := &config.Config{Servers: map[string]config.Upstream{
		"red": {Transport: "http", URL: "http://h", Deny: []string{"create_issue"}},
	}}
	reg := registry.New(cfg, registry.Options{
		Dial: func(context.Context, string, config.Upstream) (registry.Session, error) {
			dials++
			return &fakeSession{tools: fixtureTools()}, nil
		},
	})
	cs := newClient(t, reg)

	text, isErr := call(t, cs, "call_tool", map[string]any{"server": "red", "tool": "create_issue"})
	if !isErr || !strings.Contains(text, "not allowed") {
		t.Fatalf("deny = %q (err=%v)", text, isErr)
	}
	if dials != 0 {
		t.Fatalf("denied call opened %d connections", dials)
	}

	// The index hides denied tools too.
	got, _ := call(t, cs, "list_tools", map[string]any{"server": "red"})
	if strings.Contains(got, "create_issue") || !strings.Contains(got, "hidden by allow/deny") {
		t.Fatalf("denied tool still advertised:\n%s", got)
	}
}

func TestUnknownServerAndTool(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	text, isErr := call(t, cs, "list_tools", map[string]any{"server": "nope"})
	if !isErr || !strings.Contains(text, `unknown server "nope"`) {
		t.Fatalf("unknown server = %q", text)
	}
	text, isErr = call(t, cs, "call_tool", map[string]any{"server": "red", "tool": ""})
	if !isErr || !strings.Contains(text, "tool is required") {
		t.Fatalf("empty tool = %q", text)
	}
}

func TestListServersReflectsState(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	got, _ := call(t, cs, "list_servers", nil)
	if !strings.Contains(got, "red | http | unknown | 0") {
		t.Fatalf("initial status:\n%s", got)
	}
	if _, isErr := call(t, cs, "call_tool", map[string]any{"server": "red", "tool": "get_issue"}); isErr {
		t.Fatal("call failed")
	}
	got, _ = call(t, cs, "list_servers", nil)
	if !strings.Contains(got, "red | http | up |") {
		t.Fatalf("post-call status:\n%s", got)
	}
}

func TestResultTruncation(t *testing.T) {
	big := strings.Repeat("字", 300<<10/3)
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: big}}}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	text, isErr := call(t, cs, "call_tool", map[string]any{"server": "red", "tool": "get_issue"})
	if isErr {
		t.Fatalf("call: %s", text)
	}
	if !strings.Contains(text, "truncated by gateway") {
		t.Fatalf("missing truncation marker (len=%d)", len(text))
	}
	if len(text) > resultBudget+256 {
		t.Fatalf("result not bounded: %d bytes", len(text))
	}
}

func TestCatalogBudget(t *testing.T) {
	reg := testRegistry(t, func(string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	}, config.Upstream{Transport: "http", URL: "http://h"})
	cs := newClient(t, reg)

	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > 1536 {
		t.Fatalf("tools/list is %d bytes, budget is 1536; descriptions are getting expensive", len(b))
	}
}
