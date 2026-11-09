package main

import("log";"net/http";"os";"github.com/jiejhong/GooseCab/internal/gateway";"github.com/jiejhong/GooseCab/internal/p1";"github.com/jiejhong/GooseCab/internal/registry";"github.com/jiejhong/GooseCab/internal/store")
func main(){path:=os.Getenv("GOOSECAB_DB");if path==""{path="goosecab.db"};s,err:=store.Open(path);if err!=nil{log.Fatal(err)};defer s.Close();api:=&gateway.API{Registry:registry.New()};svc:=p1.New(s);mux:=(&gateway.P1API{Service:svc}).Handler(api.Handler());mux.HandleFunc("/healthz",func(w http.ResponseWriter,_ *http.Request){w.WriteHeader(200)});h:=(&gateway.WS{Store:s}).Handler(mux);log.Println("GooseCab gateway listening on :8080");log.Fatal(http.ListenAndServe(":8080",h))}
