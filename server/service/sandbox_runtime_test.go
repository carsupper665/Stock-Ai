package service

import (
	"context"
	"testing"
	"time"

	"server/model/store"
)

func TestSandboxRuntimeAdvancesReplayByTicksAndStopsOnCommand(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-runtime", "account-runtime")

	ctx := context.Background()
	processed := 0
	app.Sandboxes.SetProcessor(func(ctx context.Context, sandboxID string) error {
		if sandboxID == "sandbox-runtime" {
			processed++
		}
		return nil
	})

	if _, err := app.Sandboxes.Start(ctx, "sandbox-runtime"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	processed = 0

	tickAt := time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)
	if err := app.Runtime.Tick(ctx, "sandbox-runtime", tickAt); err != nil {
		t.Fatalf("tick sandbox runtime: %v", err)
	}

	sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime")
	if err != nil {
		t.Fatalf("get sandbox after tick: %v", err)
	}
	if sandbox.Status != store.SandboxStatusRunning {
		t.Fatalf("expected sandbox to remain running, got %s", sandbox.Status)
	}
	if !sandbox.ReplayCurrentTime.Equal(tickAt) {
		t.Fatalf("expected replay time %s, got %s", tickAt, sandbox.ReplayCurrentTime)
	}
	if processed != 1 {
		t.Fatalf("expected processor to run once for tick, got %d", processed)
	}

	if err := app.Runtime.Stop(ctx, "sandbox-runtime"); err != nil {
		t.Fatalf("stop sandbox runtime: %v", err)
	}
	if app.Runtime.IsRunning("sandbox-runtime") {
		t.Fatalf("expected runtime to stop")
	}
	if err := app.Runtime.Tick(ctx, "sandbox-runtime", tickAt.Add(time.Minute)); err == nil {
		t.Fatalf("expected tick after stop to fail")
	}
}

func TestSandboxRuntimeAutoTickAdvancesReplayAfterStart(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-runtime-auto", "account-runtime-auto")
	app.Runtime.SetAutoTickInterval(10 * time.Millisecond)

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-runtime-auto"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	defer func() {
		if err := app.Runtime.Stop(context.Background(), "sandbox-runtime-auto"); err != nil {
			t.Fatalf("stop runtime: %v", err)
		}
	}()

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime-auto")
		if err != nil {
			t.Fatalf("get sandbox: %v", err)
		}
		if sandbox.ReplayCurrentTime.After(start) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime-auto")
	if err != nil {
		t.Fatalf("get sandbox after timeout: %v", err)
	}
	t.Fatalf("expected auto runtime tick to advance replay time beyond %s, got %s", start, sandbox.ReplayCurrentTime)
}

func TestSandboxRuntimeAutoTickUsesElapsedWallTimeWhenHandlerIsSlow(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-runtime-slow", "account-runtime-slow")
	app.Runtime.SetAutoTickInterval(10 * time.Millisecond)

	var handled int
	app.Runtime.SetTickHandler(func(ctx context.Context, sandboxID string, at time.Time) error {
		handled++
		if handled == 1 {
			time.Sleep(60 * time.Millisecond)
			return nil
		}
		return app.Sandboxes.HandleRuntimeTick(ctx, sandboxID, at)
	})

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-runtime-slow"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}
	defer func() {
		if err := app.Runtime.Stop(context.Background(), "sandbox-runtime-slow"); err != nil {
			t.Fatalf("stop runtime: %v", err)
		}
	}()

	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	wantAtLeast := start.Add(40 * time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime-slow")
		if err != nil {
			t.Fatalf("get sandbox: %v", err)
		}
		if !sandbox.ReplayCurrentTime.Before(wantAtLeast) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime-slow")
	if err != nil {
		t.Fatalf("get sandbox after timeout: %v", err)
	}
	t.Fatalf("expected slow-handler auto tick to preserve elapsed time at least %s, got %s", wantAtLeast, sandbox.ReplayCurrentTime)
}

func TestSandboxRuntimeDoesNotAdvanceReplayFromWallClockDrift(t *testing.T) {
	app := newTestApp(t)
	seedReplaySandbox(t, app.DB, "sandbox-runtime-clock", "account-runtime-clock")

	ctx := context.Background()
	if _, err := app.Sandboxes.Start(ctx, "sandbox-runtime-clock"); err != nil {
		t.Fatalf("start sandbox: %v", err)
	}

	pastAnchor := time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC)
	if err := app.DB.Model(&store.Sandbox{}).
		Where("id = ?", "sandbox-runtime-clock").
		Update("runtime_anchor_at", pastAnchor).
		Error; err != nil {
		t.Fatalf("set runtime anchor: %v", err)
	}

	sandbox, err := app.Sandboxes.Get(ctx, "sandbox-runtime-clock")
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if !sandbox.ReplayCurrentTime.Equal(want) {
		t.Fatalf("expected replay time to wait for tick at %s, got %s", want, sandbox.ReplayCurrentTime)
	}
}
