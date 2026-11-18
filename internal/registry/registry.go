// Package registry owns upstream MCP connections: lazy connect, single flight,
// idle close and failure backoff.
package registry

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Jnchk/mcp-gateway/internal/config"
)

// Session is the slice of *mcp.ClientSession the registry needs.
type Session interface {
	ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

// Dialer opens one upstream session. Tests replace it.
type Dialer func(ctx context.Context, name string, u config.Upstream) (Session, error)

type Options struct {
	IdleTimeout    time.Duration
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
	SchemaTTL      time.Duration
	SweepInterval  time.Duration
	Dial           Dialer
	Logger         *slog.Logger
	Now            func() time.Time
}

type Registry struct {
	opts    Options
	servers map[string]*entry
	names   []string

	stop chan struct{}
	wg   sync.WaitGroup
}

type entry struct {
	name string
	cfg  config.Upstream

	dialMu sync.Mutex // serialises connect attempts
	mu     sync.Mutex // guards everything below

	sess     Session
	inflight int
	lastUsed time.Time

	lastOK    time.Time
	lastErr   error
	lastErrAt time.Time
	latency   time.Duration

	backoff      time.Duration
	backoffUntil time.Time

	tools   []*mcp.Tool
	toolsAt time.Time
}

func New(cfg *config.Config, opts Options) *Registry {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.SweepInterval <= 0 {
		opts.SweepInterval = 5 * time.Second
	}
	if opts.Dial == nil {
		opts.Dial = defaultDial(opts.Logger)
	}

	r := &Registry{opts: opts, servers: map[string]*entry{}}
	for name, u := range cfg.Servers {
		r.servers[name] = &entry{name: name, cfg: u}
		r.names = append(r.names, name)
	}
	sort.Strings(r.names)
	return r
}

func (r *Registry) Names() []string { return append([]string(nil), r.names...) }

func (r *Registry) HasServer(name string) bool {
	_, ok := r.servers[name]
	return ok
}

// Upstream returns a copy of the configured upstream, for routes that speak
// HTTP to it directly (the upload staging passthrough).
func (r *Registry) Upstream(name string) (config.Upstream, bool) {
	e, ok := r.servers[name]
	if !ok {
		return config.Upstream{}, false
	}
	return e.cfg, true
}

// Allowed reports whether the allow/deny lists permit a tool on this upstream.
func (r *Registry) Allowed(name, tool string) bool {
	e, ok := r.servers[name]
	if !ok {
		return false
	}
	matches := func(patterns []string) bool {
		for _, p := range patterns {
			if p == "*" || p == tool {
				return true
			}
		}
		return false
	}
	if matches(e.cfg.Deny) {
		return false
	}
	if len(e.cfg.Allow) == 0 {
		return true
	}
	return matches(e.cfg.Allow)
}

func (r *Registry) entry(name string) (*entry, error) {
	e, ok := r.servers[name]
	if !ok {
		return nil, fmt.Errorf("unknown server %q (configured: %v)", name, r.names)
	}
	return e, nil
}

// Call invokes a tool on an upstream, connecting first if needed.
func (r *Registry) Call(ctx context.Context, name, tool string, args any) (*mcp.CallToolResult, error) {
	e, err := r.entry(name)
	if err != nil {
		return nil, err
	}
	var res *mcp.CallToolResult
	err = r.withSession(ctx, e, func(s Session) error {
		cctx, cancel := context.WithTimeout(ctx, r.opts.CallTimeout)
		defer cancel()
		var callErr error
		start := r.opts.Now()
		res, callErr = s.CallTool(cctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if callErr == nil {
			e.mu.Lock()
			e.latency = r.opts.Now().Sub(start)
			e.lastOK = r.opts.Now()
			e.mu.Unlock()
		}
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Tools returns the upstream tool catalog, cached for SchemaTTL.
func (r *Registry) Tools(ctx context.Context, name string) ([]*mcp.Tool, error) {
	e, err := r.entry(name)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.tools != nil && r.opts.Now().Sub(e.toolsAt) < r.opts.SchemaTTL {
		tools := append([]*mcp.Tool(nil), e.tools...)
		e.mu.Unlock()
		return tools, nil
	}
	e.mu.Unlock()

	var tools []*mcp.Tool
	err = r.withSession(ctx, e, func(s Session) error {
		cursor := ""
		for page := 0; page < 20; page++ {
			res, err := s.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return err
			}
			tools = append(tools, res.Tools...)
			if res.NextCursor == "" {
				return nil
			}
			cursor = res.NextCursor
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.tools, e.toolsAt = tools, r.opts.Now()
	e.mu.Unlock()
	return tools, nil
}

func (r *Registry) withSession(ctx context.Context, e *entry, fn func(Session) error) error {
	s, err := r.acquire(ctx, e)
	if err != nil {
		return err
	}
	callErr := fn(s)
	r.release(e, callErr)
	return callErr
}

// acquire returns a live session with one inflight reference held.
func (r *Registry) acquire(ctx context.Context, e *entry) (Session, error) {
	now := r.opts.Now()

	e.mu.Lock()
	if e.sess != nil {
		e.inflight++
		e.lastUsed = now
		s := e.sess
		e.mu.Unlock()
		return s, nil
	}
	if now.Before(e.backoffUntil) {
		lastErr := e.lastErr
		wait := e.backoffUntil.Sub(now).Round(time.Second)
		e.mu.Unlock()
		if lastErr == nil {
			lastErr = fmt.Errorf("no connection")
		}
		return nil, fmt.Errorf("upstream %q unavailable: %v (retry in %s)", e.name, lastErr, wait)
	}
	e.mu.Unlock()

	// Slow path: one connector at a time, everyone else waits and reuses.
	e.dialMu.Lock()
	defer e.dialMu.Unlock()

	e.mu.Lock()
	if e.sess != nil {
		e.inflight++
		e.lastUsed = now
		s := e.sess
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	dctx, cancel := context.WithTimeout(ctx, r.opts.ConnectTimeout)
	start := r.opts.Now()
	s, err := r.opts.Dial(dctx, e.name, e.cfg)
	cancel()
	if err != nil {
		e.mu.Lock()
		e.recordFailure(err, r.opts.Now())
		e.mu.Unlock()
		return nil, fmt.Errorf("upstream %q: %w", e.name, err)
	}

	e.mu.Lock()
	e.sess = s
	e.inflight = 1
	e.lastUsed = now
	e.lastOK = r.opts.Now()
	e.latency = r.opts.Now().Sub(start)
	e.lastErr = nil
	e.backoff = 0
	e.backoffUntil = time.Time{}
	e.mu.Unlock()
	return s, nil
}

func (r *Registry) release(e *entry, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.inflight--
	e.lastUsed = r.opts.Now()
	if err == nil || e.inflight > 0 {
		if err != nil {
			e.recordFailure(err, r.opts.Now())
		}
		return
	}
	// A transport error means the session is no longer trustworthy; drop it so
	// the next call reconnects (subject to backoff).
	e.recordFailure(err, r.opts.Now())
	if e.sess != nil {
		s := e.sess
		e.sess = nil
		go s.Close()
	}
}

func (e *entry) recordFailure(err error, now time.Time) {
	e.lastErr = err
	e.lastErrAt = now
	if e.backoff == 0 {
		e.backoff = 5 * time.Second
	} else if e.backoff < 60*time.Second {
		e.backoff *= 2
		if e.backoff > 60*time.Second {
			e.backoff = 60 * time.Second
		}
	}
	e.backoffUntil = now.Add(e.backoff)
}

type Snapshot struct {
	Name        string
	Transport   string
	State       string // up | idle | down | unknown
	Tools       int
	LatencyMs   *int64
	LastOK      time.Time
	LastError   string
	RetryInSecs int
}

// Snapshot reports state without touching the network.
func (r *Registry) Snapshot() []Snapshot {
	now := r.opts.Now()
	out := make([]Snapshot, 0, len(r.names))
	for _, name := range r.names {
		e := r.servers[name]
		e.mu.Lock()
		s := Snapshot{
			Name:      name,
			Transport: e.cfg.Transport,
			Tools:     len(e.tools),
			LastOK:    e.lastOK,
		}
		switch {
		case e.sess != nil:
			s.State = "up"
		case e.lastErr != nil && (e.backoffUntil.After(now) || e.lastErrAt.After(e.lastOK)):
			s.State = "down"
			s.LastError = e.lastErr.Error()
			if e.backoffUntil.After(now) {
				s.RetryInSecs = int(e.backoffUntil.Sub(now).Round(time.Second) / time.Second)
			}
		case !e.lastOK.IsZero():
			s.State = "idle"
		default:
			s.State = "unknown"
		}
		if e.latency > 0 {
			ms := e.latency.Milliseconds()
			s.LatencyMs = &ms
		}
		e.mu.Unlock()
		out = append(out, s)
	}
	return out
}

// Start launches the idle sweeper. Close stops it.
func (r *Registry) Start() {
	r.stop = make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(r.opts.SweepInterval)
		defer t.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-t.C:
				r.sweep()
			}
		}
	}()
}

func (r *Registry) sweep() {
	now := r.opts.Now()
	for _, e := range r.servers {
		e.mu.Lock()
		if e.sess != nil && e.inflight == 0 && now.Sub(e.lastUsed) >= r.opts.IdleTimeout {
			s := e.sess
			e.sess = nil
			e.mu.Unlock()
			r.opts.Logger.Info("idle close", "server", e.name)
			go s.Close()
			continue
		}
		e.mu.Unlock()
	}
}

// Close stops the sweeper and closes every session.
func (r *Registry) Close() {
	if r.stop != nil {
		close(r.stop)
		r.wg.Wait()
	}
	for _, e := range r.servers {
		e.mu.Lock()
		s := e.sess
		e.sess = nil
		e.mu.Unlock()
		if s != nil {
			s.Close()
		}
	}
}
