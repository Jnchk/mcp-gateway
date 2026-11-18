// Package upload forwards a staged file to an upstream's own /upload route so
// clients never talk to upstreams directly.
package upload

import (
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jnchk/mcp-gateway/internal/registry"
)

const upstreamTimeout = 60 * time.Second

// Handler serves POST /upload/<server>?filename=<name>.
func Handler(reg *registry.Registry, token string, maxBytes int64) http.Handler {
	client := &http.Client{Timeout: upstreamTimeout}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !authorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/upload/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "usage: POST /upload/<server>?filename=<name>", http.StatusBadRequest)
			return
		}
		upstream, ok := reg.Upstream(name)
		if !ok {
			http.Error(w, "unknown server "+name, http.StatusBadRequest)
			return
		}
		if upstream.Transport != "http" || upstream.UploadURL == "" {
			http.Error(w, "server "+name+" does not support uploads (needs an http upstream with upload_url)", http.StatusBadRequest)
			return
		}
		// Staged ids are consumed by create_issue or update_issue; the same
		// allow/deny policy that gates those tools also gates staging.
		if !reg.Allowed(name, "create_issue") && !reg.Allowed(name, "update_issue") {
			http.Error(w, "server "+name+" allows neither create_issue nor update_issue, so uploads are disabled for it", http.StatusForbidden)
			return
		}
		filename := r.URL.Query().Get("filename")
		if filename == "" {
			http.Error(w, "filename is required", http.StatusBadRequest)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
		if err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if int64(len(body)) > maxBytes {
			http.Error(w, "file exceeds the gateway upload limit", http.StatusRequestEntityTooLarge)
			return
		}

		target := upstream.UploadURL + "?filename=" + url.QueryEscape(filename)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, "building upstream request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Accept", "application/json")
		for k, v := range upstream.Headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "upstream "+name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
	})
}

func authorized(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	if len(got) > 7 && got[:7] == "Bearer " {
		got = got[7:]
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
