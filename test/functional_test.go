//go:build functional

// Functional test: builds the gateway, points it at a live Redmine MCP server and
// drives it over the real Streamable HTTP transport.
//
//	scripts/functional.sh [env-file]
package functional

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct {
	base  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func buildGateway(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mcp-gateway")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mcp-gateway")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func writeConfig(t *testing.T, addr, upstreamURL, upstreamToken string) string {
	t.Helper()
	cfg := map[string]any{
		"listen":       []string{addr},
		"path":         "/mcp",
		"token":        "mcp-token",
		"status_token": "status-token",
		"idle_timeout": "60s",
		"servers": map[string]any{
			"redmine": map[string]any{
				"transport": "http",
				"url":       upstreamURL,
				"headers":   map[string]string{"Authorization": "Bearer " + upstreamToken},
				"deny":      []string{"create_issue", "update_issue"},
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startGateway(t *testing.T, bin, cfg string) {
	t.Helper()
	var log bytes.Buffer
	cmd := exec.Command(bin, "--config", cfg)
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("gateway log:\n%s", log.String())
		}
	})
}

func waitReady(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(base + "/status") // 401 means it is up
		if err == nil {
			res.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("gateway at %s never became ready", base)
}

func statusDoc(t *testing.T, base, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var doc map[string]any
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func TestAgainstRedmine(t *testing.T) {
	upstream := os.Getenv("REDMINE_MCP_URL")
	token := os.Getenv("REDMINE_MCP_TOKEN")
	if upstream == "" || token == "" {
		t.Skip("set REDMINE_MCP_URL and REDMINE_MCP_TOKEN, or run scripts/functional.sh")
	}

	bin := buildGateway(t)
	addr := freePort(t)
	cfg := writeConfig(t, addr, upstream, token)
	startGateway(t, bin, cfg)

	base := "http://" + addr
	waitReady(t, base)

	// Status starts unknown: no upstream has been contacted yet.
	doc := statusDoc(t, base, "status-token")
	servers := doc["servers"].([]any)
	first := servers[0].(map[string]any)
	if first["name"] != "redmine" || first["state"] != "unknown" {
		t.Fatalf("initial status = %#v", first)
	}

	// Unauthenticated status is rejected.
	res, err := http.Get(base + "/status")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d", res.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	httpClient := &http.Client{Transport: bearer{base: http.DefaultTransport, token: "mcp-token"}}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             base + "/mcp",
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "functional", Version: "0"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	// The MCP endpoint also requires its own token.
	anon := &http.Client{}
	if _, err := mcp.NewClient(&mcp.Implementation{Name: "anon", Version: "0"}, nil).Connect(
		ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: anon, DisableStandaloneSSE: true}, nil,
	); err == nil {
		t.Fatal("MCP endpoint accepted a request without a token")
	}

	tools, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("gateway exposes %d tools, want 3", len(tools.Tools))
	}

	got, isErr := callText(t, cs, "list_tools", map[string]any{"server": "redmine", "q": "search"})
	if isErr || !strings.Contains(got, "search_issues") {
		t.Fatalf("list_tools = %q (err=%v)", got, isErr)
	}

	got, isErr = callText(t, cs, "list_tools", map[string]any{"server": "redmine", "q": "search", "full": true})
	if isErr || !strings.Contains(got, `"inputSchema"`) {
		t.Fatalf("full schema = %q (err=%v)", got, isErr)
	}

	// Live data through the upstream.
	got, isErr = callText(t, cs, "call_tool", map[string]any{
		"server": "redmine", "tool": "search_issues",
		"arguments": map[string]any{"status": "all", "limit": 3},
	})
	if isErr {
		t.Fatalf("search_issues: %s", got)
	}
	if !strings.Contains(got, "# id | status") || !strings.Contains(got, "# returned") {
		t.Fatalf("search_issues output:\n%s", got)
	}

	got, isErr = callText(t, cs, "call_tool", map[string]any{
		"server": "redmine", "tool": "get_issue", "arguments": map[string]any{"id": 17062},
	})
	if isErr || !strings.Contains(got, "#17062") {
		t.Fatalf("get_issue = %q (err=%v)", got, isErr)
	}

	// Deny is enforced before the upstream is contacted.
	got, isErr = callText(t, cs, "call_tool", map[string]any{
		"server": "redmine", "tool": "create_issue", "arguments": map[string]any{"project": "x", "subject": "y"},
	})
	if !isErr || !strings.Contains(got, "not allowed") {
		t.Fatalf("deny = %q (err=%v)", got, isErr)
	}

	// After the calls the status document shows the upstream as up.
	doc = statusDoc(t, base, "status-token")
	first = doc["servers"].([]any)[0].(map[string]any)
	if first["state"] != "up" {
		t.Fatalf("status after calls = %#v", first)
	}

	got, isErr = callText(t, cs, "list_servers", nil)
	if isErr || !strings.Contains(got, "redmine | http | up") {
		t.Fatalf("list_servers = %q (err=%v)", got, isErr)
	}
	fmt.Fprintln(os.Stderr, "functional: ok, upstream catalog and live calls verified")
}
