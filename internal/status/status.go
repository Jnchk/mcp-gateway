// Package status serves the read-only health document consumed by the TUI plugin.
package status

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Jnchk/mcp-gateway/internal/registry"
)

type upstream struct {
	Name        string  `json:"name"`
	Transport   string  `json:"transport"`
	State       string  `json:"state"`
	Tools       int     `json:"tools"`
	LatencyMs   *int64  `json:"latency_ms"`
	LastOK      *string `json:"last_ok"`
	LastError   *string `json:"last_error"`
	RetryInSecs int     `json:"retry_in_s"`
}

type document struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Servers     []upstream `json:"servers"`
}

// Handler returns the /status handler. An empty token disables the endpoint:
// health data is internal, so it is never served unauthenticated.
func Handler(reg *registry.Registry, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token == "" {
			http.NotFound(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if len(got) > 7 && got[:7] == "Bearer " {
			got = got[7:]
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		doc := document{GeneratedAt: time.Now().UTC(), Servers: []upstream{}}
		for _, s := range reg.Snapshot() {
			u := upstream{
				Name:        s.Name,
				Transport:   s.Transport,
				State:       s.State,
				Tools:       s.Tools,
				LatencyMs:   s.LatencyMs,
				RetryInSecs: s.RetryInSecs,
			}
			if !s.LastOK.IsZero() {
				t := s.LastOK.UTC().Format(time.RFC3339)
				u.LastOK = &t
			}
			if s.LastError != "" {
				e := s.LastError
				u.LastError = &e
			}
			doc.Servers = append(doc.Servers, u)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(doc)
	})
}
