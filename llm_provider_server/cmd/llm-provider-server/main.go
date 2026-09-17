package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gateway "stock-ai/llm-provider-server"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	adminToken := os.Getenv("LLM_SERVER_ADMIN_TOKEN")
	runtimeToken := os.Getenv("LLM_SERVER_RUNTIME_TOKEN")
	masterKey := os.Getenv("LLM_SERVER_MASTER_KEY")
	dbPath := envOrDefault("LLM_SERVER_DB_PATH", "llm-provider.db")
	address := envOrDefault("LLM_SERVER_ADDR", "127.0.0.1:8090")
	timeout, err := time.ParseDuration(envOrDefault("LLM_SERVER_PROVIDER_TIMEOUT", "30s"))
	if err != nil {
		return fmt.Errorf("LLM_SERVER_PROVIDER_TIMEOUT: %w", err)
	}
	gatewayServer, err := gateway.Open(dbPath, adminToken, runtimeToken, masterKey, timeout)
	if err != nil {
		return err
	}
	defer gatewayServer.Close()
	shutdownSignal, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	httpServer := &http.Server{Handler: gatewayServer.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	log.Printf("ready: listening on %s", listener.Addr())

	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-shutdownSignal.Done():
	}
	gatewayServer.BeginShutdown()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownContext)
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	serveErr := <-serveResult
	gatewayServer.WaitForRequests()
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	if !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
