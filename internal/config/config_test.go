package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `{
  "servers": {
    "redmine": {"transport": "http", "url": "http://127.0.0.1:8888/mcp"}
  }
}`

func TestLoadDefaults(t *testing.T) {
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != "/mcp" {
		t.Fatalf("path = %q", c.Path)
	}
	got, err := c.ListenAddrs()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "127.0.0.1:8930" {
		t.Fatalf("listen = %v", got)
	}
	if c.IdleTimeout.Value() != 60*time.Second || c.ConnectTimeout.Value() != 10*time.Second ||
		c.CallTimeout.Value() != 120*time.Second || c.SchemaTTL.Value() != 5*time.Minute {
		t.Fatalf("timeouts: %v %v %v %v", c.IdleTimeout, c.ConnectTimeout, c.CallTimeout, c.SchemaTTL)
	}
}

func TestLoadRejectsWhatMatters(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown field", `{"servers":{"a":{"transport":"http","url":"http://h"}},"bogus":1}`, "unknown field"},
		{"no servers", `{"servers":{}}`, "servers is empty"},
		{"bad transport", `{"servers":{"a":{"transport":"sse","url":"http://h"}}}`, "transport must be"},
		{"bad url", `{"servers":{"a":{"transport":"http","url":"localhost:1"}}}`, "absolute http(s)"},
		{"stdio without command", `{"servers":{"a":{"transport":"stdio"}}}`, "command is required"},
		{"bad listen", `{"listen":["127.0.0.1"],"servers":{"a":{"transport":"http","url":"http://h"}}}`, "listen"},
		{"non-loopback without token", `{"listen":["0.0.0.0:9"],"servers":{"a":{"transport":"http","url":"http://h"}}}`, "non-loopback"},
		{"path without slash", `{"path":"mcp","servers":{"a":{"transport":"http","url":"http://h"}}}`, "path must"},
		{"missing env", `{"servers":{"a":{"transport":"http","url":"http://h","headers":{"Authorization":"Bearer ${NOPE}"}}}}`, "NOPE"},
		{"bad duration", `{"idle_timeout":"60","servers":{"a":{"transport":"http","url":"http://h"}}}`, "duration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoadExpandsEnv(t *testing.T) {
	t.Setenv("TEST_TOKEN", "s3cret")
	body := `{
	  "token": "${TEST_TOKEN}",
	  "status_token": "${TEST_TOKEN}",
	  "listen": ["127.0.0.1:1"],
	  "servers": {
	    "a": {
	      "transport": "http",
	      "url": "http://127.0.0.1:2/mcp",
	      "headers": {"Authorization": "Bearer ${TEST_TOKEN}"}
	    }
	  }
	}`
	c, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "s3cret" || c.Servers["a"].Headers["Authorization"] != "Bearer s3cret" {
		t.Fatalf("expansion failed: %#v", c)
	}
}

func TestListenAddrsSplitsCommas(t *testing.T) {
	c := &Config{Listen: []string{"127.0.0.1:1, 127.0.0.1:2", " , ", "127.0.0.1:3"}}
	addrs, err := c.ListenAddrs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3"}
	if len(addrs) != len(want) {
		t.Fatalf("listen = %v", addrs)
	}
	for i := range want {
		if addrs[i] != want[i] {
			t.Fatalf("listen = %v, want %v", addrs, want)
		}
	}
}
