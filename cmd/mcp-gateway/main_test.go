package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jnchk/mcp-gateway/internal/config"
)

func TestWithToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cases := []struct {
		name, token, header string
		want                int
	}{
		{"no token", "", "", 200},
		{"missing", "s3cret", "", 401},
		{"wrong", "s3cret", "Bearer nope", 401},
		{"right", "s3cret", "Bearer s3cret", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			withToken(tc.token, ok).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestInstructionsListServers(t *testing.T) {
	got := instructions(&config.Config{Servers: map[string]config.Upstream{
		"red":    {Transport: "http", URL: "http://h"},
		"review": {Transport: "http", URL: "http://h"},
	}})
	for _, want := range []string{"list_servers", "list_tools", "call_tool", "red, review"} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}
}
