package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	for _, token := range []string{"", "secret"} {
		t.Run("token="+token, func(t *testing.T) {
			t.Setenv("GOOSECAB_AGENT_TOKEN", token)
			for _, path := range []string{"/api", "/api/", "/api/future-route", "/ws", "/ws/", "/", "/index.html", "/healthz", "/apiary"} {
				for _, auth := range []string{"", "Bearer wrong", "Bearer secret", "secret", "Bearer secret ", "Bearer secret, Bearer wrong"} {
					t.Run(path+"/"+auth, func(t *testing.T) {
						called := false
						h := Authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
						r := httptest.NewRequest(http.MethodPost, path, nil)
						if auth != "" {
							r.Header.Set("Authorization", auth)
						}
						w := httptest.NewRecorder()
						h.ServeHTTP(w, r)
						public := path == "/" || path == "/index.html" || path == "/healthz" || path == "/apiary"
						allowed := public || token == "" || auth == "Bearer secret"
						if called != allowed {
							t.Fatalf("downstream called=%v, want %v", called, allowed)
						}
						if !allowed && (w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" || w.Header().Get("Cache-Control") != "no-store") {
							t.Fatalf("bad unauthorized response: %v", w.Result())
						}
					})
				}
			}
		})
	}
}

func TestAuthenticateRejectsDuplicateAuthorization(t *testing.T) {
	t.Setenv("GOOSECAB_AGENT_TOKEN", "secret")
	h := Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("ambiguous credentials reached handler") }))
	r := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	r.Header.Add("Authorization", "Bearer secret")
	r.Header.Add("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
