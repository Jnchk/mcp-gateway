package registry

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
)

const clientVersion = "0.1.0"

func defaultDial(logger interface{ Info(string, ...any) }) Dialer {
	return func(ctx context.Context, name string, u config.Upstream) (Session, error) {
		var transport mcp.Transport
		switch u.Transport {
		case "http":
			transport = &mcp.StreamableClientTransport{
				Endpoint: u.URL,
				HTTPClient: &http.Client{
					Transport: headerRoundTripper{base: http.DefaultTransport, headers: u.Headers},
				},
				DisableStandaloneSSE: true, // no server-initiated messages needed
			}
		case "stdio":
			cmd := exec.Command(u.Command[0], u.Command[1:]...)
			cmd.Dir = u.CWD
			env := os.Environ()
			for k, v := range u.Env {
				env = append(env, k+"="+v)
			}
			cmd.Env = env
			cmd.Stderr = &prefixWriter{name: name, w: os.Stderr}
			transport = &mcp.CommandTransport{Command: cmd}
		}

		client := mcp.NewClient(&mcp.Implementation{Name: "mcp-gateway", Version: clientVersion}, nil)
		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			return nil, err
		}
		return session, nil
	}
}

// prefixWriter tags upstream stderr lines with the server name.
type prefixWriter struct {
	name string
	w    io.Writer

	mu   sync.Mutex
	line bool // mid-line, so no prefix needed
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	start := 0
	for i, c := range b {
		if p.line {
			p.line = false
		} else if _, err := io.WriteString(p.w, "["+p.name+"] "); err != nil {
			return 0, err
		}
		if c == '\n' {
			if _, err := p.w.Write(b[start : i+1]); err != nil {
				return 0, err
			}
			start = i + 1
		} else {
			p.line = true
		}
	}
	if start < len(b) {
		if _, err := p.w.Write(b[start:]); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// headerRoundTripper applies the configured headers to every upstream request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	req := r.Clone(r.Context())
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}
