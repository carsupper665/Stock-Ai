package service

import (
	"context"
	"sync"
	"time"

	"server/domain"
)

type sandboxTickHandler func(context.Context, string, time.Time) error
type sandboxAutoTickPlanner func(context.Context, string, time.Duration) (time.Time, bool, error)

type SandboxRuntimeManager struct {
	mu               sync.Mutex
	runtimes         map[string]*sandboxRuntime
	tickHandler      sandboxTickHandler
	autoTickPlanner  sandboxAutoTickPlanner
	autoTickInterval time.Duration
}

type sandboxRuntime struct {
	sandboxID        string
	tickCh           chan sandboxRuntimeTick
	controlCh        chan sandboxControl
	doneCh           chan struct{}
	autoTickInterval time.Duration
}

type sandboxRuntimeTick struct {
	ctx   context.Context
	at    time.Time
	errCh chan error
}

type sandboxControl struct {
	kind string
}

const sandboxControlStop = "stop"
const defaultSandboxRuntimeAutoTickInterval = time.Second

func NewSandboxRuntimeManager() *SandboxRuntimeManager {
	return &SandboxRuntimeManager{
		runtimes:         make(map[string]*sandboxRuntime),
		autoTickInterval: defaultSandboxRuntimeAutoTickInterval,
	}
}

func (m *SandboxRuntimeManager) SetTickHandler(handler sandboxTickHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickHandler = handler
}

func (m *SandboxRuntimeManager) SetAutoTickPlanner(planner sandboxAutoTickPlanner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoTickPlanner = planner
}

func (m *SandboxRuntimeManager) SetAutoTickInterval(interval time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoTickInterval = interval
}

func (m *SandboxRuntimeManager) Start(ctx context.Context, sandboxID string) error {
	if sandboxID == "" {
		return domain.ValidationError("INVALID_SANDBOX_ID", "sandbox_id is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if runtime, ok := m.runtimes[sandboxID]; ok && runtime.isActive() {
		return nil
	}

	autoTickInterval := m.autoTickInterval
	if autoTickInterval <= 0 {
		autoTickInterval = defaultSandboxRuntimeAutoTickInterval
	}
	runtime := &sandboxRuntime{
		sandboxID:        sandboxID,
		tickCh:           make(chan sandboxRuntimeTick),
		controlCh:        make(chan sandboxControl),
		doneCh:           make(chan struct{}),
		autoTickInterval: autoTickInterval,
	}
	m.runtimes[sandboxID] = runtime
	go runtime.loop(m)
	return nil
}

func (m *SandboxRuntimeManager) Tick(ctx context.Context, sandboxID string, at time.Time) error {
	if at.IsZero() {
		return domain.ValidationError("INVALID_REPLAY_TICK", "replay tick time is required")
	}
	runtime := m.runtime(sandboxID)
	if runtime == nil || !runtime.isActive() {
		return domain.ConflictError("SANDBOX_RUNTIME_NOT_RUNNING", "sandbox runtime is not running")
	}

	errCh := make(chan error, 1)
	tick := sandboxRuntimeTick{ctx: ctx, at: at.UTC(), errCh: errCh}
	select {
	case runtime.tickCh <- tick:
	case <-runtime.doneCh:
		return domain.ConflictError("SANDBOX_RUNTIME_NOT_RUNNING", "sandbox runtime is not running")
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-errCh:
		return err
	case <-runtime.doneCh:
		return domain.ConflictError("SANDBOX_RUNTIME_NOT_RUNNING", "sandbox runtime is not running")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *SandboxRuntimeManager) Stop(ctx context.Context, sandboxID string) error {
	return m.stopRuntime(ctx, sandboxID)
}

func (m *SandboxRuntimeManager) Pause(ctx context.Context, sandboxID string) error {
	return m.stopRuntime(ctx, sandboxID)
}

func (m *SandboxRuntimeManager) stopRuntime(ctx context.Context, sandboxID string) error {
	runtime := m.runtime(sandboxID)
	if runtime == nil {
		return nil
	}
	if !runtime.isActive() {
		m.removeRuntime(sandboxID, runtime)
		return nil
	}

	select {
	case runtime.controlCh <- sandboxControl{kind: sandboxControlStop}:
	case <-runtime.doneCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-runtime.doneCh:
		m.removeRuntime(sandboxID, runtime)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *SandboxRuntimeManager) IsRunning(sandboxID string) bool {
	runtime := m.runtime(sandboxID)
	return runtime != nil && runtime.isActive()
}

func (m *SandboxRuntimeManager) runtime(sandboxID string) *sandboxRuntime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtimes[sandboxID]
}

func (m *SandboxRuntimeManager) removeRuntime(sandboxID string, runtime *sandboxRuntime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.runtimes[sandboxID]; current == runtime {
		delete(m.runtimes, sandboxID)
	}
}

func (m *SandboxRuntimeManager) handleTick(ctx context.Context, sandboxID string, at time.Time) error {
	m.mu.Lock()
	handler := m.tickHandler
	m.mu.Unlock()
	if handler == nil {
		return domain.ConflictError("SANDBOX_RUNTIME_HANDLER_MISSING", "sandbox runtime tick handler is not configured")
	}
	return handler(ctx, sandboxID, at)
}

func (m *SandboxRuntimeManager) handleAutoTick(ctx context.Context, sandboxID string, elapsed time.Duration) {
	m.mu.Lock()
	planner := m.autoTickPlanner
	m.mu.Unlock()
	if planner == nil {
		return
	}
	at, ok, err := planner(ctx, sandboxID, elapsed)
	if err != nil || !ok {
		return
	}
	_ = m.handleTick(ctx, sandboxID, at)
}

func (r *sandboxRuntime) loop(manager *SandboxRuntimeManager) {
	defer close(r.doneCh)
	autoTicker := time.NewTicker(r.autoTickInterval)
	defer autoTicker.Stop()
	lastAutoTick := time.Now()
	for {
		select {
		case tick := <-r.tickCh:
			tick.errCh <- manager.handleTick(tick.ctx, r.sandboxID, tick.at)
		case <-autoTicker.C:
			now := time.Now()
			elapsed := now.Sub(lastAutoTick)
			if elapsed <= 0 {
				elapsed = r.autoTickInterval
			}
			manager.handleAutoTick(context.Background(), r.sandboxID, elapsed)
			lastAutoTick = now
		case cmd := <-r.controlCh:
			if cmd.kind == sandboxControlStop {
				return
			}
		}
	}
}

func (r *sandboxRuntime) isActive() bool {
	select {
	case <-r.doneCh:
		return false
	default:
		return true
	}
}
