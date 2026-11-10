package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// Authenticate protects all API routes and the legacy WebSocket endpoint when
// GOOSECAB_AGENT_TOKEN is set. Wrap the assembled server, not just an individual
// router, so newly added API endpoints cannot accidentally bypass authentication.
// The observer shell and health endpoint remain public. An empty token preserves
// local-development behavior.
func Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		protected := path == "/api" || strings.HasPrefix(path, "/api/") || path == "/ws" || strings.HasPrefix(path, "/ws/")
		if protected {
			if token := os.Getenv("GOOSECAB_AGENT_TOKEN"); token != "" {
				// Hash first so the comparison itself has fixed-size inputs, even
				// when the supplied credential has a different length.
				expected := sha256.Sum256([]byte("Bearer " + token))
				actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
				if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
					w.Header().Set("WWW-Authenticate", `Bearer realm="GooseCab"`)
					w.Header().Set("Cache-Control", "no-store")
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
