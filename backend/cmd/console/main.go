package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/auth"
	"backend/internal/config"
	"backend/internal/console"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	agentToken, err := config.Secret("AGENT_SERVER_ADMIN_TOKEN", "agent_admin_token")
	if err != nil {
		return err
	}
	llmAdminToken, err := config.Secret("LLM_SERVER_ADMIN_TOKEN", "llm_admin_token")
	if err != nil {
		return err
	}
	llmRuntimeToken, err := config.Secret("LLM_SERVER_RUNTIME_TOKEN", "llm_runtime_token")
	if err != nil {
		return err
	}
	handler, err := console.New(console.Config{
		OIDC: &auth.OIDCSettings{
			Issuer: os.Getenv("OIDC_ISSUER"), Audience: os.Getenv("OIDC_AUDIENCE"), Subject: os.Getenv("OIDC_USER_SUBJECT"),
			JWKSURL: os.Getenv("OIDC_JWKS_URL"), TokenType: env("OIDC_TOKEN_TYPE", "at+jwt"),
		},
		AgentToken:        agentToken,
		GatewayAdminToken: llmAdminToken, GatewayRuntimeToken: llmRuntimeToken,
		BackendURL: os.Getenv("BACKEND_URL"), AgentURL: os.Getenv("AGENT_SERVER_URL"), GatewayURL: os.Getenv("LLM_SERVER_URL"),
		Public: os.DirFS(env("CONSOLE_PUBLIC_DIR", "public")),
	})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: env("CONSOLE_ADDR", "127.0.0.1:8088"), Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	stopped := make(chan error, 1)
	go func() {
		if os.Getenv("CONSOLE_TLS_CERT") != "" || os.Getenv("CONSOLE_TLS_KEY") != "" {
			stopped <- server.ListenAndServeTLS(os.Getenv("CONSOLE_TLS_CERT"), os.Getenv("CONSOLE_TLS_KEY"))
			return
		}
		stopped <- server.ListenAndServe()
	}()
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
