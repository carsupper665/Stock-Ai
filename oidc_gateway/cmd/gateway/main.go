package main

import (
	"log"
	"net/http"
	"oidc_gateway/internal/gateway"
	"time"
)

func main() {
	cfg, err := gateway.LoadConfig()
	if err != nil {
		log.Fatalf("OIDC gateway configuration invalid: %v", err)
	}
	app, err := gateway.New(cfg)
	if err != nil {
		log.Fatalf("OIDC gateway configuration invalid: %v", err)
	}
	server := &http.Server{Addr: cfg.ListenAddress, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	log.Printf("OIDC gateway listening on %s", cfg.ListenAddress)
	log.Fatal(server.ListenAndServe())
}
