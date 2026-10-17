package voiceworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func assertStoppedWithoutStart(t *testing.T, path string) {
	t.Helper()
	commands := lines(t, path)
	if len(commands) != 2 || !strings.HasPrefix(commands[0], "init auto ") || commands[1] != "stop" {
		t.Fatalf("expected one init and stop, never capture start/retry: %v", commands)
	}
}

func TestEarlyStopUsesShutdownBudget(t *testing.T) {
	for _, startup := range []time.Duration{80 * time.Millisecond, time.Nanosecond} {
		t.Run(startup.String(), func(t *testing.T) {
			c, path := fakeClient(t, "delayed-ready")
			c.config.StartupTimeout = startup
			c.config.ShutdownTimeout = 750 * time.Millisecond
			stop := make(chan struct{})
			close(stop)
			if err := c.Run(context.Background(), testConfig(), stop, func(Event) error { return nil }); err != nil {
				t.Fatalf("stop within shutdown budget rejected: %v", err)
			}
			assertStoppedWithoutStart(t, path)
		})
	}
}

func TestStopBeforeReadyDeadlineUsesShutdownBudget(t *testing.T) {
	c, path := fakeClient(t, "delayed-ready")
	c.config.StartupTimeout = 100 * time.Millisecond
	c.config.ShutdownTimeout = 750 * time.Millisecond
	stop := make(chan struct{})
	// Trigger before the ready deadline, while initialization is still pending.
	timer := time.AfterFunc(30*time.Millisecond, func() { close(stop) })
	defer timer.Stop()
	if err := c.Run(context.Background(), testConfig(), stop, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertStoppedWithoutStart(t, path)
}

func TestEarlyStopTimeoutIsShutdownNotStartup(t *testing.T) {
	c, path := fakeClient(t, "no-ready")
	c.config.StartupTimeout = 20 * time.Millisecond
	c.config.ShutdownTimeout = 150 * time.Millisecond
	stop := make(chan struct{})
	close(stop)
	start := time.Now()
	if err := c.Run(context.Background(), testConfig(), stop, func(Event) error { return nil }); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatal(err)
	}
	if time.Since(start) < c.config.ShutdownTimeout {
		t.Fatal("shutdown budget cut short")
	}
	commands := lines(t, path)
	if len(commands) != 1 || !strings.HasPrefix(commands[0], "init auto ") {
		t.Fatalf("retried after stop: %v", commands)
	}
}
