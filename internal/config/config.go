// Package config loads and validates the gateway configuration.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Duration is a JSON string such as "60s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"60s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }

type Upstream struct {
	Transport string            `json:"transport"`
	URL       string            `json:"url,omitempty"`
	UploadURL string            `json:"upload_url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	CWD       string            `json:"cwd,omitempty"`
	Allow     []string          `json:"allow,omitempty"`
	Deny      []string          `json:"deny,omitempty"`

	// UploadRequiresAny names the tools whose permission makes staging useful
	// for this upstream. nil means the default (create_issue/update_issue);
	// an explicit empty list disables uploads.
	UploadRequiresAny []string `json:"upload_requires_any,omitempty"`
}

type Config struct {
	Listen         []string            `json:"listen"`
	Path           string              `json:"path"`
	Token          string              `json:"token"`
	StatusToken    string              `json:"status_token"`
	IdleTimeout    Duration            `json:"idle_timeout"`
	ConnectTimeout Duration            `json:"connect_timeout"`
	CallTimeout    Duration            `json:"call_timeout"`
	SchemaTTL      Duration            `json:"schema_ttl"`
	MaxUpload      int64               `json:"max_upload"`
	Servers        map[string]Upstream `json:"servers"`
}

// DefaultPath is $XDG_CONFIG_HOME/mcp-gateway/config.json (or ~/.config/...).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(dir, "mcp-gateway", "config.json")
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.expandEnv(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Path == "" {
		c.Path = "/mcp"
	}
	if c.MaxUpload <= 0 {
		c.MaxUpload = 25 << 20
	}
	if len(c.Listen) == 0 {
		c.Listen = []string{"127.0.0.1:8930"}
	}
	// An HTTP upstream that exposes a staging route on the same listener gets
	// /upload/<name> for free; upload_url overrides it.
	for name, s := range c.Servers {
		if s.Transport == "http" && s.UploadURL == "" {
			if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
				u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/mcp") + "/upload"
				s.UploadURL = u.String()
			}
		}
		c.Servers[name] = s
	}

	defaults := []struct {
		d   *Duration
		def time.Duration
	}{
		{&c.IdleTimeout, 60 * time.Second},
		{&c.ConnectTimeout, 10 * time.Second},
		{&c.CallTimeout, 120 * time.Second},
		{&c.SchemaTTL, 5 * time.Minute},
	}
	for _, d := range defaults {
		if *d.d <= 0 {
			*d.d = Duration(d.def)
		}
	}
}

func (c *Config) Validate() error {
	addrs, err := c.ListenAddrs()
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("listen contains no addresses")
	}
	nonLoopback := ""
	for _, a := range addrs {
		if _, _, err := net.SplitHostPort(a); err != nil {
			return fmt.Errorf("listen %q: %w", a, err)
		}
		if nonLoopback == "" && !loopback(a) {
			nonLoopback = a
		}
	}
	if c.Token == "" && nonLoopback != "" {
		return fmt.Errorf("listen includes non-loopback %q: set token", nonLoopback)
	}
	if !strings.HasPrefix(c.Path, "/") {
		return fmt.Errorf("path must start with /")
	}
	if len(c.Servers) == 0 {
		return fmt.Errorf("servers is empty")
	}
	for name, s := range c.Servers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("server name is empty")
		}
		switch s.Transport {
		case "http":
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("server %q: url must be an absolute http(s) URL", name)
			}
		case "stdio":
			if len(s.Command) == 0 {
				return fmt.Errorf("server %q: command is required for stdio", name)
			}
		default:
			return fmt.Errorf("server %q: transport must be http or stdio, got %q", name, s.Transport)
		}
	}
	return nil
}

// ListenAddrs flattens listen, tolerating comma-separated entries.
func (c *Config) ListenAddrs() ([]string, error) {
	var out []string
	for _, entry := range c.Listen {
		for _, a := range strings.Split(entry, ",") {
			if a = strings.TrimSpace(a); a != "" {
				out = append(out, a)
			}
		}
	}
	return out, nil
}

func (c *Config) expandEnv() error {
	missing := map[string]bool{}
	expand := func(s string) string {
		return os.Expand(s, func(k string) string {
			v, ok := os.LookupEnv(k)
			if !ok {
				missing[k] = true
			}
			return v
		})
	}
	for i := range c.Listen {
		c.Listen[i] = expand(c.Listen[i])
	}
	c.Token = expand(c.Token)
	c.StatusToken = expand(c.StatusToken)
	for name, s := range c.Servers {
		s.URL = expand(s.URL)
		s.CWD = expand(s.CWD)
		for i := range s.Command {
			s.Command[i] = expand(s.Command[i])
		}
		for k, v := range s.Headers {
			s.Headers[k] = expand(v)
		}
		for k, v := range s.Env {
			s.Env[k] = expand(v)
		}
		c.Servers[name] = s
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for k := range missing {
			names = append(names, k)
		}
		return fmt.Errorf("unset environment variable(s): %s", strings.Join(names, ", "))
	}
	return nil
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
