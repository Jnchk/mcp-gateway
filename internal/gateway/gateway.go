// Package gateway exposes the three MCP tools that front the upstreams.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/registry"
)

// resultBudget caps a single upstream result so one runaway page cannot eat the
// model's context. Upstreams keep their own formatting below this size.
const resultBudget = 256 << 10

type Gateway struct {
	reg *registry.Registry
}

func New(reg *registry.Registry) *Gateway { return &Gateway{reg: reg} }

func Register(srv *mcp.Server, g *Gateway) {
	add(srv, &mcp.Tool{
		Name:        "list_servers",
		Description: "List upstream MCP servers with state, tool count, latency and last error.",
		Annotations: readOnly,
	}, g.listServers)

	add(srv, &mcp.Tool{
		Name:        "list_tools",
		Description: "List one upstream's tools: a compact index, or full JSON Schemas with full=true. Use before call_tool.",
		Annotations: readOnly,
	}, g.listTools)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "call_tool",
		Description: "Call a tool on one upstream server. Arguments are passed through unchanged; discover them with list_tools(server, full=true).",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true)},
	}, g.callTool)
}

func add[In any](srv *mcp.Server, t *mcp.Tool, h func(context.Context, In) (string, error)) {
	mcp.AddTool(srv, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		s, err := h(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
	})
}

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}

func boolPtr(b bool) *bool { return &b }

func (g *Gateway) listServers(_ context.Context, _ struct{}) (string, error) {
	snaps := g.reg.Snapshot()
	var b strings.Builder
	b.WriteString("# server | transport | state | tools | latency_ms | last_error\n")
	for _, s := range snaps {
		latency := "-"
		if s.LatencyMs != nil {
			latency = strconv.FormatInt(*s.LatencyMs, 10)
		}
		b.WriteString(row(s.Name, s.Transport, s.State, strconv.Itoa(s.Tools), latency, truncate(s.LastError, 160)))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

type listToolsIn struct {
	Server string `json:"server" jsonschema:"upstream server name"`
	Q      string `json:"q,omitempty" jsonschema:"substring filter on tool name or description"`
	Full   bool   `json:"full,omitempty" jsonschema:"return full JSON Schemas instead of the compact index"`
}

func (g *Gateway) listTools(ctx context.Context, in listToolsIn) (string, error) {
	if !g.reg.HasServer(in.Server) {
		return "", fmt.Errorf("unknown server %q (configured: %v)", in.Server, g.reg.Names())
	}

	tools, err := g.reg.Tools(ctx, in.Server)
	if err != nil {
		return "", err
	}

	shown := make([]*mcp.Tool, 0, len(tools))
	hidden := 0
	for _, t := range tools {
		if !g.reg.Allowed(in.Server, t.Name) {
			hidden++
			continue
		}
		if in.Q != "" && !containsFold(t.Name+" "+t.Description, in.Q) {
			continue
		}
		shown = append(shown, t)
	}

	if len(shown) == 0 {
		if in.Q != "" {
			return fmt.Sprintf("no tools on %q matched %q", in.Server, in.Q), nil
		}
		return fmt.Sprintf("server %q exposes no tools", in.Server), nil
	}

	var b strings.Builder
	if in.Full {
		for _, t := range shown {
			line, err := json.Marshal(struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				InputSchema any    `json:"inputSchema,omitempty"`
			}{t.Name, t.Description, t.InputSchema})
			if err != nil {
				return "", fmt.Errorf("encoding schema for %q: %w", t.Name, err)
			}
			b.Write(line)
			b.WriteByte('\n')
		}
	} else {
		b.WriteString("# tool | args | description\n")
		for _, t := range shown {
			b.WriteString(row(t.Name, argSummary(t), truncate(t.Description, 100)))
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "# %d of %d tools on %s", len(shown), len(tools), in.Server)
	if hidden > 0 {
		fmt.Fprintf(&b, " (%d hidden by allow/deny)", hidden)
	}
	b.WriteByte('\n')
	return b.String(), nil
}

type callToolIn struct {
	Server    string         `json:"server" jsonschema:"upstream server name"`
	Tool      string         `json:"tool" jsonschema:"tool name from list_tools"`
	Arguments map[string]any `json:"arguments,omitempty" jsonschema:"arguments object for that tool; omit when it takes none"`
}

func (g *Gateway) callTool(ctx context.Context, _ *mcp.CallToolRequest, in callToolIn) (*mcp.CallToolResult, any, error) {
	if !g.reg.HasServer(in.Server) {
		return nil, nil, fmt.Errorf("unknown server %q (configured: %v)", in.Server, g.reg.Names())
	}
	if in.Tool == "" {
		return nil, nil, fmt.Errorf("tool is required")
	}
	if !g.reg.Allowed(in.Server, in.Tool) {
		return nil, nil, fmt.Errorf("tool %q is not allowed on server %q by the gateway configuration", in.Tool, in.Server)
	}

	var args any
	if len(in.Arguments) > 0 {
		args = in.Arguments
	}
	res, err := g.reg.Call(ctx, in.Server, in.Tool, args)
	if err != nil {
		return nil, nil, err
	}
	return limitResult(tagResult(res, in.Server, in.Tool)), nil, nil
}

// tagResult marks which upstream produced an error so failures are attributable.
func tagResult(res *mcp.CallToolResult, server, tool string) *mcp.CallToolResult {
	if !res.IsError || len(res.Content) == 0 {
		return res
	}
	prefix := &mcp.TextContent{Text: fmt.Sprintf("[%s/%s] ", server, tool)}
	res.Content = append([]mcp.Content{prefix}, res.Content...)
	return res
}

func row(cells ...string) string {
	for i, c := range cells {
		c = strings.Join(strings.Fields(c), " ")
		if c == "" {
			c = "-"
		}
		cells[i] = c
	}
	return strings.Join(cells, " | ")
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func argSummary(t *mcp.Tool) string {
	schema := parseSchema(t.InputSchema)
	if len(schema.Properties) == 0 {
		return "-"
	}
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	shown := names
	const max = 8
	if len(shown) > max {
		shown = shown[:max]
	}
	parts := make([]string, 0, len(shown)+1)
	for _, name := range shown {
		if required[name] {
			parts = append(parts, name)
		} else {
			parts = append(parts, name+"?")
		}
	}
	if rest := len(names) - len(shown); rest > 0 {
		parts = append(parts, fmt.Sprintf("+%d", rest))
	}
	return strings.Join(parts, ", ")
}

func parseSchema(raw any) struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
} {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if raw != nil {
		if b, err := json.Marshal(raw); err == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	return s
}
