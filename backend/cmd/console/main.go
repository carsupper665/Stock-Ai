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

	"backend/internal/config"
	"backend/internal/console"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	configuration, err := config.Load(".env")
	if err != nil {
		return err
	}
	handler, err := console.New(console.Config{
		UserToken: configuration.UserToken, AgentToken: os.Getenv("AGENT_SERVER_ADMIN_TOKEN"),
		GatewayAdminToken: os.Getenv("LLM_SERVER_ADMIN_TOKEN"), GatewayRuntimeToken: os.Getenv("LLM_SERVER_RUNTIME_TOKEN"),
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
