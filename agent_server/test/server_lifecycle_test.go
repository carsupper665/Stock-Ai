package test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

func TestServerStartsHTTPAndCancelsBackgroundWorkOnShutdown(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	taskStarted := make(chan struct{})
	taskCanceled := make(chan struct{})
	server, err := common.NewServer(loop, []common.BackgroundTask{{
		Name:     "observable-background-work",
		Interval: time.Minute,
		Run: func(ctx context.Context) error {
			close(taskStarted)
			<-ctx.Done()
			close(taskCanceled)
			return ctx.Err()
		},
	}}, nil, nil, time.Second)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	response, err := http.Get("http://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("GET /health Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("health status = %q, want ok", body.Status)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/health", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	methodResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /health error = %v", err)
	}
	defer methodResponse.Body.Close()
	if methodResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health status = %d, want %d", methodResponse.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := methodResponse.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("POST /health Content-Type = %q, want application/json", got)
	}
	if got := methodResponse.Header.Get("Allow"); got != http.MethodGet {
		t.Fatalf("POST /health Allow = %q, want GET", got)
	}
	var methodError struct {
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
		Message    string `json:"msg"`
	}
	if err := json.NewDecoder(methodResponse.Body).Decode(&methodError); err != nil {
		t.Fatalf("decode method error: %v", err)
	}
	if methodError.StatusCode != http.StatusMethodNotAllowed || methodError.Error != "METHOD_NOT_ALLOWED" || methodError.Message != "method not allowed" {
		t.Fatalf("POST /health body = %+v", methodError)
	}
	notFoundResponse, err := http.Get("http://" + listener.Addr().String() + "/missing")
	if err != nil {
		t.Fatalf("GET /missing error = %v", err)
	}
	defer notFoundResponse.Body.Close()
	if notFoundResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /missing status = %d, want %d", notFoundResponse.StatusCode, http.StatusNotFound)
	}
	if got := notFoundResponse.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("GET /missing Content-Type = %q, want application/json", got)
	}
	var notFoundError struct {
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
		Message    string `json:"msg"`
	}
	if err := json.NewDecoder(notFoundResponse.Body).Decode(&notFoundError); err != nil {
		t.Fatalf("decode not-found error: %v", err)
	}
	if notFoundError.StatusCode != http.StatusNotFound || notFoundError.Error != "NOT_FOUND" || notFoundError.Message != "route not found" {
		t.Fatalf("GET /missing body = %+v", notFoundError)
	}

	clock.Advance(time.Minute)
	select {
	case <-taskStarted:
	case <-time.After(time.Second):
		t.Fatal("background task did not start")
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-taskCanceled:
	default:
		t.Fatal("background task did not observe cancellation before shutdown returned")
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := server.Wait(waitCtx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if _, err := http.Get("http://" + listener.Addr().String() + "/health"); err == nil {
		t.Fatal("server accepted a request after shutdown")
	}
}

func TestServerShutdownIsIdempotentAndClosesResourcesInOrder(t *testing.T) {
	var mu sync.Mutex
	closed := make([]string, 0, 2)
	resource := func(name string) io.Closer {
		return closerFunc(func() error {
			mu.Lock()
			defer mu.Unlock()
			closed = append(closed, name)
			return nil
		})
	}
	server, err := common.NewServer(
		common.NewEventLoopWithClock(newManualClock()),
		nil,
		[]io.Closer{resource("database"), resource("logs")},
		nil,
		time.Second,
	)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(closed, ","); got != "database,logs" {
		t.Fatalf("resource close order = %q, want database,logs", got)
	}
}

func TestNewServerRollsBackTasksAfterPartialInitializationFailure(t *testing.T) {
	loop := common.NewEventLoopWithClock(newManualClock())
	_, err := common.NewServer(loop, []common.BackgroundTask{
		{Name: "registered-first", Interval: time.Minute, Run: func(context.Context) error { return nil }},
		{Name: "invalid", Interval: 0, Run: func(context.Context) error { return nil }},
	}, nil, nil, time.Second)
	if !errors.Is(err, common.ErrIntervalInvalid) {
		t.Fatalf("NewServer() error = %v, want %v", err, common.ErrIntervalInvalid)
	}
	if !loop.IsEmpty() {
		t.Fatal("partially registered tasks remain after initialization failure")
	}
}

func TestServerShutdownTimeoutReportsWorkAndDefersResourceClose(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	started := make(chan struct{})
	release := make(chan struct{})
	resourceClosed := make(chan struct{})
	server, err := common.NewServer(loop, []common.BackgroundTask{{
		Name:     "non-cooperative",
		Interval: time.Minute,
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}}, []io.Closer{closerFunc(func() error {
		close(resourceClosed)
		return nil
	})}, nil, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background task did not start")
	}

	if err := server.Shutdown(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	select {
	case <-resourceClosed:
		t.Fatal("resource closed while background work was still running")
	default:
	}
	close(release)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := server.Wait(waitCtx); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	select {
	case <-resourceClosed:
	default:
		t.Fatal("resource was not closed after background work finished")
	}
}

func TestBackgroundTaskErrorAndPanicDoNotStopServer(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	wantError := errors.New("background failure")
	errorRan := make(chan struct{})
	panicRan := make(chan struct{})
	server, err := common.NewServer(loop, []common.BackgroundTask{
		{Name: "error", Interval: time.Minute, Run: func(context.Context) error {
			close(errorRan)
			return wantError
		}},
		{Name: "panic", Interval: time.Minute, Run: func(context.Context) error {
			close(panicRan)
			panic("boom")
		}},
	}, nil, log.New(io.Discard, "", 0), time.Second)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if err := server.Start(listener); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)
	for name, ran := range map[string]<-chan struct{}{"error task": errorRan, "panic task": panicRan} {
		select {
		case <-ran:
		case <-time.After(time.Second):
			t.Fatalf("%s did not run", name)
		}
	}

	response, err := http.Get("http://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health after callback failures error = %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if err := server.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
