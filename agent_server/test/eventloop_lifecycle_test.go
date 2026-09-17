package test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	common "stock-ai/agent-server"
)

type manualClock struct {
	mu           sync.Mutex
	now          time.Time
	timers       []*manualTimer
	resetGate    <-chan struct{}
	resetStarted chan struct{}
	resetOnce    sync.Once
	armChanged   chan struct{}
}

type manualTimer struct {
	clock    *manualClock
	c        chan time.Time
	deadline time.Time
	active   bool
}

func newManualClock() *manualClock {
	return &manualClock{
		now:        time.Unix(1_700_000_000, 0),
		armChanged: make(chan struct{}),
	}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(deadline time.Time) common.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{
		clock:    c,
		c:        make(chan time.Time, 1),
		deadline: deadline,
		active:   deadline.After(c.now),
	}
	if !timer.active {
		timer.c <- c.now
	}
	c.timers = append(c.timers, timer)
	c.notifyArmLocked()
	return timer
}

func (c *manualClock) WaitUntilArmedAt(t *testing.T, deadline time.Time) {
	t.Helper()
	for {
		c.mu.Lock()
		for _, timer := range c.timers {
			if timer.active && timer.deadline.Equal(deadline) {
				c.mu.Unlock()
				return
			}
		}
		changed := c.armChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-time.After(time.Second):
			t.Fatalf("timer was not armed for %s", deadline)
		}
	}
}

func (c *manualClock) notifyArmLocked() {
	close(c.armChanged)
	c.armChanged = make(chan struct{})
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	for _, timer := range c.timers {
		if timer.active && !now.Before(timer.deadline) {
			timer.active = false
			timer.c <- now
		}
	}
	c.mu.Unlock()
}

func (t *manualTimer) C() <-chan time.Time { return t.c }

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *manualTimer) ResetAt(deadline time.Time) bool {
	if t.clock.resetStarted != nil {
		t.clock.resetOnce.Do(func() { close(t.clock.resetStarted) })
	}
	if t.clock.resetGate != nil {
		<-t.clock.resetGate
	}
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.deadline = deadline
	if deadline.After(t.clock.now) {
		t.active = true
	} else {
		t.active = false
		select {
		case t.c <- t.clock.now:
		default:
		}
	}
	t.clock.notifyArmLocked()
	return wasActive
}

func TestEventLoopStartReturnsAfterTimerIsArmed(t *testing.T) {
	clock := newManualClock()
	resetGate := make(chan struct{})
	clock.resetGate = resetGate
	clock.resetStarted = make(chan struct{})
	loop := common.NewEventLoopWithClock(clock)
	if err := loop.RegisterEvent("pending", func(context.Context) error { return nil }, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	startReturned := make(chan error, 1)
	go func() { startReturned <- loop.Start() }()

	select {
	case <-clock.resetStarted:
	case <-time.After(time.Second):
		t.Fatal("event loop did not attempt to arm its timer")
	}
	select {
	case err := <-startReturned:
		t.Fatalf("Start() returned before timer arm completed: %v", err)
	default:
	}
	close(resetGate)
	select {
	case err := <-startReturned:
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start() did not return after timer arm completed")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestEventLoopRunsOneShotExactlyOnce(t *testing.T) {
	clock := newManualClock()
	startTime := clock.Now()
	loop := common.NewEventLoopWithClock(clock)
	runs := make(chan struct{}, 2)
	checked := make(chan struct{})

	err := loop.RegisterEvent("one-shot", func(context.Context) error {
		runs <- struct{}{}
		return nil
	}, time.Minute, 1)
	if err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.RegisterEvent("check-one-shot", func(context.Context) error {
		close(checked)
		return nil
	}, 61*time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent(check) error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = loop.Stop(ctx)
	})

	clock.Advance(time.Minute)
	select {
	case <-runs:
	case <-time.After(time.Second):
		t.Fatal("one-shot event did not run")
	}
	clock.WaitUntilArmedAt(t, startTime.Add(61*time.Minute))

	clock.Advance(time.Hour)
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not process the later clock advance")
	}
	select {
	case <-runs:
		t.Fatal("one-shot event ran more than once")
	default:
	}
}

func TestEventLoopRejectsInvalidRegistration(t *testing.T) {
	loop := common.NewEventLoopWithClock(newManualClock())
	task := func(context.Context) error { return nil }

	tests := []struct {
		name     string
		taskName string
		task     common.TaskFunc
		interval time.Duration
		count    int
		want     error
	}{
		{name: "blank name", taskName: "  ", task: task, interval: time.Second, count: 1, want: common.ErrEventNameRequired},
		{name: "nil task", taskName: "task", interval: time.Second, count: 1, want: common.ErrTaskFuncNil},
		{name: "zero interval", taskName: "task", task: task, count: 1, want: common.ErrIntervalInvalid},
		{name: "negative interval", taskName: "task", task: task, interval: -time.Second, count: 1, want: common.ErrIntervalInvalid},
		{name: "zero count", taskName: "task", task: task, interval: time.Second, want: common.ErrCountInvalid},
		{name: "count below forever", taskName: "task", task: task, interval: time.Second, count: -2, want: common.ErrCountInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loop.RegisterEvent(tt.taskName, tt.task, tt.interval, tt.count)
			if !errors.Is(err, tt.want) {
				t.Fatalf("RegisterEvent() error = %v, want %v", err, tt.want)
			}
		})
	}

	if err := loop.RegisterEvent("duplicate", task, time.Second, 1); err != nil {
		t.Fatalf("first RegisterEvent() error = %v", err)
	}
	if err := loop.RegisterEvent("duplicate", task, time.Second, 1); !errors.Is(err, common.ErrAlreadyExist) {
		t.Fatalf("duplicate RegisterEvent() error = %v, want %v", err, common.ErrAlreadyExist)
	}
}

func TestEventLoopFiniteTaskDoesNotOverlapOrPileUp(t *testing.T) {
	clock := newManualClock()
	startTime := clock.Now()
	loop := common.NewEventLoopWithClock(clock)
	started := make(chan int, 4)
	release := make(chan struct{})
	checked := make(chan struct{})
	var mu sync.Mutex
	runCount := 0

	err := loop.RegisterEvent("finite", func(context.Context) error {
		mu.Lock()
		runCount++
		run := runCount
		mu.Unlock()
		started <- run
		<-release
		return nil
	}, time.Minute, 3)
	if err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.RegisterEvent("overlap-check", func(context.Context) error {
		close(checked)
		return nil
	}, 11*time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent(check) error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	clock.Advance(time.Minute)
	assertRunStarted(t, started, 1)
	clock.WaitUntilArmedAt(t, startTime.Add(11*time.Minute))
	clock.Advance(10 * time.Minute)
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not process overlap check")
	}
	assertNoRunStarted(t, started)

	release <- struct{}{}
	assertRunStarted(t, started, 2)
	release <- struct{}{}
	clock.WaitUntilArmedAt(t, clock.Now().Add(time.Minute))
	clock.Advance(time.Minute)
	assertRunStarted(t, started, 3)
	release <- struct{}{}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "finite"); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestEventLoopDeleteCompletesWaitAndAllowsNameReuse(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	task := func(context.Context) error { return nil }
	if err := loop.RegisterEvent("replaceable", task, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.DelEvent("replaceable"); err != nil {
		t.Fatalf("DelEvent() error = %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := loop.Wait(waitCtx, "replaceable"); !errors.Is(err, common.ErrEventDeleted) {
		t.Fatalf("Wait() error = %v, want %v", err, common.ErrEventDeleted)
	}
	if err := loop.RegisterEvent("replaceable", task, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent() after deletion error = %v", err)
	}
}

func TestEventLoopStopCancelsCooperativeTaskAndCanRestart(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := loop.RegisterEvent("background", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}, time.Minute, -1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := loop.Start(); !errors.Is(err, common.ErrLoopRunning) {
		t.Fatalf("second Start() error = %v, want %v", err, common.ErrLoopRunning)
	}
	clock.Advance(time.Minute)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background task did not start")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("task did not observe cancellation before Stop returned")
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "background"); !errors.Is(err, common.ErrLoopStopped) {
		t.Fatalf("Wait() error = %v, want %v", err, common.ErrLoopStopped)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() after completed Stop error = %v", err)
	}
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

func TestEventLoopStopTimeoutKeepsLoopInStoppingState(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := loop.RegisterEvent("non-cooperative", func(context.Context) error {
		close(started)
		<-release
		return nil
	}, time.Minute, -1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative task did not start")
	}

	stopCtx, cancelStop := context.WithCancel(context.Background())
	cancelStop()
	if err := loop.Stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want %v", err, context.Canceled)
	}
	if err := loop.Start(); !errors.Is(err, common.ErrLoopStopping) {
		t.Fatalf("Start() while task exits error = %v, want %v", err, common.ErrLoopStopping)
	}
	if err := loop.RegisterEvent("too-late", func(context.Context) error { return nil }, time.Minute, 1); !errors.Is(err, common.ErrLoopStopping) {
		t.Fatalf("RegisterEvent() while stopping error = %v, want %v", err, common.ErrLoopStopping)
	}
	close(release)
	finishCtx, cancelFinish := context.WithTimeout(context.Background(), time.Second)
	defer cancelFinish()
	if err := loop.Stop(finishCtx); err != nil {
		t.Fatalf("Stop() after task release error = %v", err)
	}
}

func TestEventLoopErrorAndPanicDoNotStopOtherTasks(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	wantError := errors.New("task failed")
	healthyRan := make(chan struct{})
	if err := loop.RegisterEvent("error", func(context.Context) error { return wantError }, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent(error) error = %v", err)
	}
	if err := loop.RegisterEvent("panic", func(context.Context) error { panic("boom") }, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent(panic) error = %v", err)
	}
	if err := loop.RegisterEvent("healthy", func(context.Context) error {
		close(healthyRan)
		return nil
	}, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent(healthy) error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "error"); !errors.Is(err, wantError) {
		t.Fatalf("Wait(error) = %v, want %v", err, wantError)
	}
	if err := loop.Wait(waitCtx, "panic"); err == nil || !strings.Contains(err.Error(), "panic in event[panic]: boom") {
		t.Fatalf("Wait(panic) = %v, want recovered panic", err)
	}
	if err := loop.Wait(waitCtx, "healthy"); err != nil {
		t.Fatalf("Wait(healthy) error = %v", err)
	}
	select {
	case <-healthyRan:
	default:
		t.Fatal("healthy task did not run")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestEventLoopDeleteCancelsAlreadyDispatchedTask(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	started := make(chan struct{})
	canceled := make(chan struct{})
	if err := loop.RegisterEvent("delete-running", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}, time.Minute, -1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	if err := loop.DelEvent("delete-running"); err != nil {
		t.Fatalf("DelEvent() error = %v", err)
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "delete-running"); !errors.Is(err, common.ErrEventDeleted) {
		t.Fatalf("Wait() error = %v, want %v", err, common.ErrEventDeleted)
	}
	select {
	case <-canceled:
	default:
		t.Fatal("deleted task did not observe cancellation")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestEventLoopStopBeforeStartCompletesPendingWait(t *testing.T) {
	loop := common.NewEventLoopWithClock(newManualClock())
	if err := loop.RegisterEvent("pending", func(context.Context) error { return nil }, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "pending"); !errors.Is(err, common.ErrLoopStopped) {
		t.Fatalf("Wait() error = %v, want %v", err, common.ErrLoopStopped)
	}
}

func TestEventLoopCancelsInvocationContextAfterNaturalCompletion(t *testing.T) {
	clock := newManualClock()
	loop := common.NewEventLoopWithClock(clock)
	invocationContext := make(chan context.Context, 1)
	if err := loop.RegisterEvent("context-lifecycle", func(ctx context.Context) error {
		invocationContext <- ctx
		return nil
	}, time.Minute, 1); err != nil {
		t.Fatalf("RegisterEvent() error = %v", err)
	}
	if err := loop.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	clock.Advance(time.Minute)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := loop.Wait(waitCtx, "context-lifecycle"); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	ctx := <-invocationContext
	select {
	case <-ctx.Done():
	default:
		t.Fatal("invocation context remains live after callback completion")
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := loop.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func assertRunStarted(t *testing.T, started <-chan int, want int) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("run = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("run %d did not start", want)
	}
}

func assertNoRunStarted(t *testing.T, started <-chan int) {
	t.Helper()
	select {
	case got := <-started:
		t.Fatalf("overlapping run %d started", got)
	default:
	}
}
