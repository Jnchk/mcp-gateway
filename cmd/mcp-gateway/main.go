package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
	"github.com/Jnchk/mcp-gateway/internal/gateway"
	"github.com/Jnchk/mcp-gateway/internal/registry"
	"github.com/Jnchk/mcp-gateway/internal/status"
	"github.com/Jnchk/mcp-gateway/internal/upload"
)

const version = "0.1.0"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fs := flag.NewFlagSet("mcp-gateway", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", config.DefaultPath(), "path to config.json")
	stdio := fs.Bool("stdio", false, "serve MCP over stdin/stdout instead of HTTP")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("configuration", "err", err)
		os.Exit(1)
	}

	reg := registry.New(cfg, registry.Options{
		IdleTimeout:    cfg.IdleTimeout.Value(),
		ConnectTimeout: cfg.ConnectTimeout.Value(),
		CallTimeout:    cfg.CallTimeout.Value(),
		SchemaTTL:      cfg.SchemaTTL.Value(),
		Logger:         logger,
	})
	reg.Start()
	defer reg.Close()

	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-gateway", Version: version}, &mcp.ServerOptions{
		Instructions: instructions(cfg),
	})
	gateway.Register(srv, gateway.New(reg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *stdio {
		err = srv.Run(ctx, &mcp.StdioTransport{})
	} else {
		err = serveHTTP(ctx, cfg, reg, srv, logger)
	}
	if err != nil {
		logger.Error("server", "err", err)
		os.Exit(1)
	}
}

func serveHTTP(ctx context.Context, cfg *config.Config, reg *registry.Registry, srv *mcp.Server, logger *slog.Logger) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)

	mux := http.NewServeMux()
	mux.Handle(cfg.Path, withToken(cfg.Token, handler))
	mux.Handle("/status", status.Handler(reg, cfg.StatusToken))
	// POST /upload/<upstream>?filename=... keeps client-side staging on the
	// gateway instead of requiring a direct connection to the upstream.
	mux.Handle("/upload/", upload.Handler(reg, cfg.Token, cfg.MaxUpload))

	addrs, err := cfg.ListenAddrs()
	if err != nil {
		return err
	}
	var listeners []net.Listener
	for _, addr := range addrs {
		ln, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("listen %s: %w", addr, listenErr)
		}
		listeners = append(listeners, ln)
	}

	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()

	servers := make([]*http.Server, len(listeners))
	errCh := make(chan error, len(listeners))
	for i, ln := range listeners {
		hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		servers[i] = hs
		logger.Info("listening", "addr", ln.Addr().String(), "path", cfg.Path, "status", cfg.StatusToken != "")
		go func() { errCh <- hs.Serve(ln) }()
	}
	go func() {
		<-serveCtx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		for _, hs := range servers {
			_ = hs.Shutdown(shutdown)
		}
	}()

	err = <-errCh
	stopServing()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func instructions(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf(`Upstream MCP servers behind one endpoint.
- list_servers: state, tool count, latency and last error of every upstream.
- list_tools(server, q?, full?): compact index; full=true returns the JSON Schema needed to call a tool.
- call_tool(server, tool, arguments): arguments pass through unchanged.
Servers: %s.
Tool names, ids and text formats belong to the upstream; follow its result text.`, joinComma(names))
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func withToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if len(got) > 7 && got[:7] == "Bearer " {
			got = got[7:]
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
