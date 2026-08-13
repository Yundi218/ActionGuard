package main

import (
	"log"
	"net/http"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/httpapi"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: cfg.APIAddr, Handler: httpapi.NewRouter(httpapi.Dependencies{})}
	log.Printf("api listening on %s", cfg.APIAddr)
	log.Fatal(server.ListenAndServe())
}
