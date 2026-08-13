package main

import (
	"log"
	"net/http"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/httpapi"
	"github.com/Yundi218/ActionGuard/internal/httpserver"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	server := newAPIServer(cfg.APIAddr)
	log.Printf("api listening on %s", cfg.APIAddr)
	log.Fatal(server.ListenAndServe())
}

func newAPIServer(addr string) *http.Server {
	return httpserver.New(addr, httpapi.NewRouter(httpapi.Dependencies{}))
}
