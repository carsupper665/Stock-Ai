package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	common "stock-ai/agent-server"
)

func main() {
	logger := log.New(os.Stdout, "agent-server: ", log.LstdFlags)
	if err := run(logger); err != nil {
		logger.Printf("stopped with error: %v", err)
		os.Exit(1)
	}
}

func run(logger *log.Logger) (runErr error) {
	config, address, eventScanInterval, err := processConfig()
	if err != nil {
		return err
	}
	config.Logger = logger
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	defer listener.Close()

	runtime, err := common.OpenAgentRuntime(config)
	if err != nil {
		return err
	}
	runtimeOwned := true
	defer func() {
		if runtimeOwned {
			runErr = errors.Join(runErr, runtime.Close())
		}
	}()
	loop, err := common.NewEventLoop()
	if err != nil {
		return err
	}
	server, err := common.NewServer(
		loop,
		[]common.BackgroundTask{
			runtime.LocalEventTask(nil, eventScanInterval),
			runtime.PriceEventTask(nil, eventScanInterval),
			runtime.PositionEventTask(nil, eventScanInterval),
		},
		[]io.Closer{runtime},
		logger,
		10*time.Second,
		runtime.Handler(),
	)
	if err != nil {
		return err
	}
	runtimeOwned = false
	if ctx.Err() != nil {
		return server.Shutdown()
	}
	if err := server.Start(listener); err != nil {
		return errors.Join(err, server.Shutdown())
	}
	logger.Printf("ready: listening on %s", listener.Addr())

	select {
	case <-ctx.Done():
		shutdownErr := server.Shutdown()
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelWait()
		return errors.Join(shutdownErr, server.Wait(waitCtx))
	case waitErr := <-serverDone(server):
		return waitErr
	}
}

func processConfig() (common.RuntimeConfig, string, time.Duration, error) {
	defaultMaxLoop, err := envPositiveInt("AGENT_SERVER_DEFAULT_MAX_LOOP", 20)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	defaultMaxToolCall, err := envPositiveInt("AGENT_SERVER_DEFAULT_MAX_TOOL_CALL", 40)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	requestTimeout, err := envPositiveDuration("AGENT_SERVER_REQUEST_TIMEOUT", 30*time.Second)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	toolTimeout, err := envPositiveDuration("AGENT_SERVER_TOOL_TIMEOUT", 10*time.Second)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	stopTimeout, err := envPositiveDuration("AGENT_SERVER_STOP_TIMEOUT", 5*time.Second)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	eventScanInterval, err := envPositiveDuration("AGENT_SERVER_EVENT_SCAN_INTERVAL", time.Second)
	if err != nil {
		return common.RuntimeConfig{}, "", 0, err
	}
	config := common.RuntimeConfig{
		DatabasePath:        envString("AGENT_SERVER_DB_PATH", "agent-server.db"),
		HistoryDirectory:    envString("AGENT_SERVER_HISTORY_DIR", "agent-history"),
		AdminToken:          os.Getenv("AGENT_SERVER_ADMIN_TOKEN"),
		BackendURL:          os.Getenv("AGENT_SERVER_BACKEND_URL"),
		BackendUserToken:    os.Getenv("AGENT_SERVER_BACKEND_USER_TOKEN"),
		GatewayURL:          os.Getenv("AGENT_SERVER_GATEWAY_URL"),
		GatewayRuntimeToken: os.Getenv("AGENT_SERVER_GATEWAY_RUNTIME_TOKEN"),
		DefaultPrompt:       envString("AGENT_SERVER_DEFAULT_PROMPT", "You are an autonomous trading agent."),
		DefaultMaxLoop:      defaultMaxLoop,
		DefaultMaxToolCall:  defaultMaxToolCall,
		RequestTimeout:      requestTimeout,
		ToolTimeout:         toolTimeout,
		StopTimeout:         stopTimeout,
	}
	return config, envString("AGENT_SERVER_ADDR", "127.0.0.1:8080"), eventScanInterval, nil
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envPositiveInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func envPositiveDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return value, nil
}

func serverDone(server *common.Server) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- server.Wait(context.Background())
	}()
	return done
}
