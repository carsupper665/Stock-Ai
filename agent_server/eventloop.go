package common

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

var (
	ErrEventNameRequired = errors.New("event name is required")
	ErrTaskFuncNil       = errors.New("task func is nil")
	ErrIntervalInvalid   = errors.New("interval must be > 0")
	ErrCountInvalid      = errors.New("count must be a positive integer or -1")
	ErrEventNotFound     = errors.New("event not found")
	ErrAlreadyExist      = errors.New("event with the same name already exists")
	ErrLoopRunning       = errors.New("event loop is already running")
	ErrLoopStopping      = errors.New("event loop is stopping")
	ErrEventDeleted      = errors.New("event deleted")
	ErrLoopStopped       = errors.New("event loop stopped")
)

type TaskFunc func(context.Context) error

// Timer is the minimal timer contract used by EventLoop. It permits deterministic
// lifecycle tests without making scheduling depend on wall-clock sleeps.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	ResetAt(time.Time) bool
}

// Clock provides EventLoop's scheduling time.
type Clock interface {
	Now() time.Time
	NewTimer(time.Time) Timer
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(deadline time.Time) Timer {
	return &realTimer{Timer: time.NewTimer(time.Until(deadline))}
}

type realTimer struct{ *time.Timer }

func (t *realTimer) C() <-chan time.Time { return t.Timer.C }
func (t *realTimer) ResetAt(deadline time.Time) bool {
	return t.Timer.Reset(time.Until(deadline))
}

type event struct {
	f         TaskFunc
	name      string
	nextRun   time.Time
	interval  time.Duration
	remaining int
	running   bool
	deleting  bool
	cancel    context.CancelFunc
	done      chan struct{}
	result    error
	doneOnce  sync.Once
}

type loopState uint8

const (
	loopStopped loopState = iota
	loopRunning
	loopStopping
)

type EventLoop struct {
	mu       sync.Mutex
	events   map[string]*event
	finished map[string]*event
	clock    Clock
	wake     chan struct{}
	state    loopState
	stopDone chan struct{}
	workers  sync.WaitGroup
}

func NewEventLoop() (*EventLoop, error) {
	return NewEventLoopWithClock(realClock{}), nil
}

func NewEventLoopWithClock(clock Clock) *EventLoop {
	if clock == nil {
		clock = realClock{}
	}
	return &EventLoop{
		events:   make(map[string]*event),
		finished: make(map[string]*event),
		clock:    clock,
		wake:     make(chan struct{}, 1),
		state:    loopStopped,
	}
}

func (el *EventLoop) RegisterEvent(name string, f TaskFunc, interval time.Duration, count int) error {
	if strings.TrimSpace(name) == "" {
		return ErrEventNameRequired
	}
	if f == nil {
		return ErrTaskFuncNil
	}
	if interval <= 0 {
		return ErrIntervalInvalid
	}
	if count == 0 || count < -1 {
		return ErrCountInvalid
	}

	el.mu.Lock()
	if el.state == loopStopping {
		el.mu.Unlock()
		return ErrLoopStopping
	}
	if _, exists := el.events[name]; exists {
		el.mu.Unlock()
		return ErrAlreadyExist
	}
	delete(el.finished, name)
	el.events[name] = &event{
		f:         f,
		name:      name,
		nextRun:   el.clock.Now().Add(interval),
		interval:  interval,
		remaining: count,
		done:      make(chan struct{}),
	}
	el.mu.Unlock()
	el.signalWake()
	return nil
}

func (el *EventLoop) Start() error {
	el.mu.Lock()
	switch el.state {
	case loopRunning:
		el.mu.Unlock()
		return ErrLoopRunning
	case loopStopping:
		el.mu.Unlock()
		return ErrLoopStopping
	}
	select {
	case <-el.wake:
	default:
	}
	el.state = loopRunning
	el.stopDone = make(chan struct{})
	ready := make(chan struct{})
	el.mu.Unlock()

	go el.run(ready)
	<-ready
	return nil
}

func (el *EventLoop) run(ready chan<- struct{}) {
	timer := el.clock.NewTimer(el.clock.Now().Add(time.Hour))
	defer timer.Stop()
	first := true
	for {
		deadline, stopped := el.dispatchDue()
		if stopped {
			if first {
				close(ready)
			}
			el.workers.Wait()
			el.mu.Lock()
			el.state = loopStopped
			close(el.stopDone)
			el.mu.Unlock()
			return
		}

		if !timer.Stop() {
			select {
			case <-timer.C():
			default:
			}
		}
		timer.ResetAt(deadline)
		if first {
			close(ready)
			first = false
		}
		select {
		case <-timer.C():
		case <-el.wake:
		}
	}
}

func (el *EventLoop) dispatchDue() (time.Time, bool) {
	el.mu.Lock()
	defer el.mu.Unlock()
	if el.state != loopRunning {
		return time.Time{}, true
	}

	now := el.clock.Now()
	deadline := now.Add(time.Hour)
	for _, ev := range el.events {
		if ev.deleting || ev.remaining == 0 {
			continue
		}
		if ev.running {
			continue
		}
		if now.Before(ev.nextRun) {
			if ev.nextRun.Before(deadline) {
				deadline = ev.nextRun
			}
			continue
		}

		if ev.remaining > 0 {
			ev.remaining--
		}
		ev.running = true
		ev.nextRun = now.Add(ev.interval)
		ctx, cancel := context.WithCancel(context.Background())
		ev.cancel = cancel
		el.workers.Add(1)
		go el.execute(ev, ctx, cancel)
	}
	return deadline, false
}

func (el *EventLoop) execute(ev *event, ctx context.Context, cancel context.CancelFunc) {
	defer el.workers.Done()
	err := callTask(ev.name, ev.f, ctx)
	cancel()

	el.mu.Lock()
	ev.running = false
	ev.cancel = nil
	ev.result = err
	terminal := ev.deleting || ev.remaining == 0 || el.state != loopRunning
	if terminal {
		if ev.deleting {
			ev.result = ErrEventDeleted
		} else if el.state != loopRunning {
			ev.result = ErrLoopStopped
		}
		ev.doneOnce.Do(func() { close(ev.done) })
		delete(el.events, ev.name)
		el.finished[ev.name] = ev
	}
	el.mu.Unlock()
	el.signalWake()
}

func callTask(name string, f TaskFunc, ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic in event[%s]: %v\n%s", name, recovered, debug.Stack())
		}
	}()
	return f(ctx)
}

func (el *EventLoop) Stop(ctx context.Context) error {
	el.mu.Lock()
	if el.state == loopStopped {
		for _, ev := range el.events {
			ev.result = ErrLoopStopped
			ev.doneOnce.Do(func() { close(ev.done) })
			delete(el.events, ev.name)
			el.finished[ev.name] = ev
		}
		el.mu.Unlock()
		return nil
	}
	if el.state == loopRunning {
		el.state = loopStopping
		for _, ev := range el.events {
			if ev.cancel != nil {
				ev.cancel()
			}
			if !ev.running {
				ev.result = ErrLoopStopped
				ev.doneOnce.Do(func() { close(ev.done) })
				delete(el.events, ev.name)
				el.finished[ev.name] = ev
			}
		}
	}
	done := el.stopDone
	el.mu.Unlock()
	el.signalWake()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (el *EventLoop) Wait(ctx context.Context, name string) error {
	el.mu.Lock()
	ev, ok := el.events[name]
	if !ok {
		ev, ok = el.finished[name]
		if !ok {
			el.mu.Unlock()
			return ErrEventNotFound
		}
	}
	done := ev.done
	el.mu.Unlock()

	select {
	case <-done:
		el.mu.Lock()
		result := ev.result
		el.mu.Unlock()
		return result
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (el *EventLoop) DelEvent(name string) error {
	el.mu.Lock()
	ev, ok := el.events[name]
	if !ok {
		el.mu.Unlock()
		return ErrEventNotFound
	}
	ev.deleting = true
	if ev.cancel != nil {
		ev.cancel()
	}
	if !ev.running {
		ev.result = ErrEventDeleted
		ev.doneOnce.Do(func() { close(ev.done) })
		delete(el.events, name)
		el.finished[name] = ev
	}
	el.mu.Unlock()
	el.signalWake()
	return nil
}

func (el *EventLoop) IsEmpty() bool {
	el.mu.Lock()
	defer el.mu.Unlock()
	return len(el.events) == 0
}

func (el *EventLoop) signalWake() {
	select {
	case el.wake <- struct{}{}:
	default:
	}
}
