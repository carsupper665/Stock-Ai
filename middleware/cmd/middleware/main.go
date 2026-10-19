package main

import (
	"context"
	"log"
	"net/http"
	"oidc_gateway/internal/gateway"
	"os"
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
	if len(os.Args) == 2 && os.Args[1] == "preflight" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := app.Preflight(ctx, os.Stdout); err != nil {
			log.Fatalf("OIDC preflight failed: %v", err)
		}
		return
	}
	server := &http.Server{Addr: cfg.ListenAddress, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	log.Printf("OIDC gateway listening on %s; IdP client %s must register redirect URI %s and post-logout URI %s", cfg.ListenAddress, cfg.ClientID, cfg.RedirectURI, cfg.PostLogoutURI)
	log.Fatal(server.ListenAndServe())
}
