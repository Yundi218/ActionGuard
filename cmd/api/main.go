package main

import (
	"log"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/httpapi"
	"github.com/Yundi218/ActionGuard/internal/httpserver"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	server := httpserver.New(cfg.APIAddr, httpapi.NewRouter(httpapi.Dependencies{}))
	log.Printf("api listening on %s", cfg.APIAddr)
	log.Fatal(server.ListenAndServe())
}
