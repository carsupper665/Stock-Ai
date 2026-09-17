package common

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	ErrEventLoopRequired      = errors.New("event loop is required")
	ErrListenerRequired       = errors.New("listener is required")
	ErrShutdownTimeoutInvalid = errors.New("shutdown timeout must be > 0")
	ErrServerRunning          = errors.New("server is already running")
	ErrServerStopped          = errors.New("server is stopped")
)

type BackgroundTask struct {
	Name     string
	Interval time.Duration
	Run      TaskFunc
}

type Server struct {
	mu              sync.Mutex
	loop            *EventLoop
	http            *http.Server
	listener        net.Listener
	resources       []io.Closer
	logger          *log.Logger
	shutdownTimeout time.Duration
	shutdownOnce    sync.Once
	shutdownDone    chan struct{}
	shutdownErr     error
	serveErr        error
	started         bool
	stopping        bool
}

func NewServer(loop *EventLoop, tasks []BackgroundTask, resources []io.Closer, logger *log.Logger, shutdownTimeout time.Duration, application ...http.Handler) (*Server, error) {
	if loop == nil {
		return nil, ErrEventLoopRequired
	}
	if shutdownTimeout <= 0 {
		return nil, ErrShutdownTimeoutInvalid
	}
	if len(application) > 1 || (len(application) == 1 && application[0] == nil) {
		return nil, errors.New("exactly one non-nil application handler may be provided")
	}

	registered := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if err := loop.RegisterEvent(task.Name, task.Run, task.Interval, -1); err != nil {
			for _, name := range registered {
				_ = loop.DelEvent(name)
			}
			return nil, fmt.Errorf("register background task %q: %w", task.Name, err)
		}
		registered = append(registered, task.Name)
	}

	mux := http.NewServeMux()
	var applicationHealth interface{ HealthError() error }
	if len(application) == 1 {
		applicationHealth, _ = application[0].(interface{ HealthError() error })
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeHTTPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if applicationHealth != nil && applicationHealth.HealthError() != nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "PERSISTENCE_UNAVAILABLE", "Agent persistence is unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	if len(application) == 1 {
		mux.Handle("/api/", application[0])
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeHTTPError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	})
	return &Server{
		loop:            loop,
		http:            &http.Server{Handler: mux},
		resources:       append([]io.Closer(nil), resources...),
		logger:          logger,
		shutdownTimeout: shutdownTimeout,
		shutdownDone:    make(chan struct{}),
	}, nil
}

func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"status_code":%d,"error":%q,"msg":%q}`, status, code, message)
}

func (s *Server) Start(listener net.Listener) error {
	if listener == nil {
		return ErrListenerRequired
	}

	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return ErrServerStopped
	}
	if s.started {
		s.mu.Unlock()
		return ErrServerRunning
	}
	if err := s.loop.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("start event loop: %w", err)
	}
	s.started = true
	s.listener = listener
	s.mu.Unlock()

	go func() {
		err := s.http.Serve(listener)
		s.mu.Lock()
		stopping := s.stopping
		if errors.Is(err, http.ErrServerClosed) || (stopping && errors.Is(err, net.ErrClosed)) {
			err = nil
		}
		s.serveErr = err
		s.mu.Unlock()
		if !stopping {
			s.beginShutdown()
		}
	}()
	return nil
}

func (s *Server) Shutdown() error {
	s.beginShutdown()
	timer := time.NewTimer(s.shutdownTimeout)
	defer timer.Stop()
	select {
	case <-s.shutdownDone:
		return s.shutdownErr
	case <-timer.C:
		return fmt.Errorf("server shutdown: %w", context.DeadlineExceeded)
	}
}

func (s *Server) Wait(ctx context.Context) error {
	select {
	case <-s.shutdownDone:
		s.mu.Lock()
		err := errors.Join(s.serveErr, s.shutdownErr)
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) beginShutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		listener := s.listener
		s.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		go s.finishShutdown()
	})
}

func (s *Server) finishShutdown() {
	var err error
	for _, resource := range s.resources {
		if quiescer, ok := resource.(interface{ Quiesce() error }); ok {
			err = errors.Join(err, quiescer.Quiesce())
		}
	}
	results := make(chan error, 2)
	go func() {
		err := s.http.Shutdown(context.Background())
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		results <- err
	}()
	go func() {
		results <- s.loop.Stop(context.Background())
	}()
	err = errors.Join(err, <-results, <-results)
	for _, resource := range s.resources {
		if resource == nil {
			continue
		}
		if closeErr := resource.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
			if s.logger != nil {
				s.logger.Printf("close server resource: %v", closeErr)
			}
		}
	}
	s.shutdownErr = err
	close(s.shutdownDone)
}
