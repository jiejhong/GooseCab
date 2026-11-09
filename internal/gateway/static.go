package gateway

import ("embed"; "net/http")
//go:embed web/index.html
var observerFS embed.FS
func Observer(next http.Handler) http.Handler { return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { if r.URL.Path=="/"||r.URL.Path=="/index.html" { b,_:=observerFS.ReadFile("web/index.html"); w.Header().Set("Content-Type","text/html; charset=utf-8"); _,_=w.Write(b); return }; next.ServeHTTP(w,r) }) }
