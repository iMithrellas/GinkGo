package voiceworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is a fake installed executable. It never loads models or
// audio. The shell wrapper invokes only this test in an independent process.
func TestHelperProcess(t *testing.T) {
	i := 0
	for i < len(os.Args) && os.Args[i] != "--" {
		i++
	}
	if i == len(os.Args) {
		return
	}
	mode, path := os.Args[i+1], os.Args[i+2]
	if mode == "blocked-input" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		os.Exit(70)
	}
	init, err := DecodeCommand(s.Bytes())
	if err != nil || init.Type != "init" {
		os.Exit(71)
	}
	log := func(line string) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			os.Exit(72)
		}
		fmt.Fprintln(f, line)
		f.Close()
	}
	log("init " + string(init.Config.ExecutionProvider()) + " " + init.Epoch)
	if mode == "retry" && init.Config.ExecutionProvider() != CPU {
		os.Exit(17)
	}
	if mode == "always-fail" || mode == "nonzero" {
		os.Exit(18)
	}
	if mode == "no-ready" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if mode == "delayed-ready" {
		time.Sleep(180 * time.Millisecond)
	}
	if mode == "malformed" {
		fmt.Fprintln(os.Stdout, `{"private":"/model/secret"}`)
		os.Exit(0)
	}
	if mode == "oversized" {
		fmt.Fprintln(os.Stdout, strings.Repeat("x", MaxEventBytes+1))
		os.Exit(0)
	}
	if mode == "stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("/secret/model", 10000))
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if mode == "descendant" || mode == "descendant-exit" {
		cmd := exec.Command("sleep", "60")
		cmd.Stdout = os.Stdout
		cmd.Stdin = os.Stdin
		cmd.Stderr = os.Stderr
		if cmd.Start() != nil {
			os.Exit(73)
		}
		log("pid " + strconv.Itoa(cmd.Process.Pid))
		if mode == "descendant-exit" {
			os.Exit(19)
		}
	}
	seq := uint64(1)
	emit := func(e Event) {
		e.Version = Version
		e.Epoch = init.Epoch
		e.Sequence = seq
		seq++
		if json.NewEncoder(os.Stdout).Encode(e) != nil {
			os.Exit(74)
		}
	}
	status := ProviderStatus{Requested: init.Config.Provider, Effective: Unknown}
	if init.Config.ExecutionProvider() == CPU {
		status.Effective = CPU
		status.FallbackReason = init.Config.FallbackReason
	}
	emit(Event{Type: "ready", Provider: &status})
	for s.Scan() {
		cmd, e := DecodeCommand(s.Bytes())
		if e != nil || cmd.Epoch != init.Epoch {
			os.Exit(75)
		}
		log(cmd.Type)
		switch cmd.Type {
		case "start":
			if mode == "after-ready-fail" {
				os.Exit(20)
			}
			if mode == "flood" {
				for {
					level := 0.5
					emit(Event{Type: "level", Level: &level})
				}
			}
		case "stop":
			if mode == "ignore-stop" {
				time.Sleep(time.Hour)
				os.Exit(0)
			}
			emit(Event{Type: "stopped"})
			os.Exit(0)
		default:
			os.Exit(76)
		}
	}
	os.Exit(77)
}

func fakeClient(t *testing.T, mode string) (*Client, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "commands")
	script := filepath.Join(dir, "ginkgo-voice")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(script, []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec "+quote(exe)+" -test.run=^TestHelperProcess$ -- "+quote(mode)+" "+quote(path)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return New(ClientConfig{Executable: script, StartupTimeout: 2 * time.Second, ShutdownTimeout: 300 * time.Millisecond}), path
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
}

func TestDisabledIsolation(t *testing.T) {
	c := New(ClientConfig{Executable: "/nonexistent/helper", StartupTimeout: -1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(ctx, Config{}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientGracefulFlow(t *testing.T) {
	c, path := fakeClient(t, "normal")
	stop := make(chan struct{})
	var events []string
	err := c.Run(context.Background(), testConfig(), stop, func(e Event) error {
		events = append(events, e.Type)
		if e.Type == "ready" {
			close(stop)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "ready,stopped" {
		t.Fatal(events)
	}
	l := lines(t, path)
	if l[len(l)-1] != "stop" || !strings.HasPrefix(l[0], "init auto ") {
		t.Fatal(l)
	}
}

func TestStopBeforeReady(t *testing.T) {
	c, path := fakeClient(t, "normal")
	stop := make(chan struct{})
	close(stop)
	if err := c.Run(context.Background(), testConfig(), stop, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	l := lines(t, path)
	if len(l) != 2 || l[1] != "stop" {
		t.Fatalf("capture authorized on early stop: %v", l)
	}
}

func TestCPUFallback(t *testing.T) {
	c, path := fakeClient(t, "retry")
	stop := make(chan struct{})
	err := c.Run(context.Background(), testConfig(), stop, func(e Event) error {
		if e.Type == "ready" {
			if e.Provider.Requested != Auto || e.Provider.Effective != CPU || e.Provider.FallbackReason != "cuda_init_failed" {
				t.Errorf("bad fallback: %+v", e.Provider)
			}
			close(stop)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	l := lines(t, path)
	if len(l) < 3 || !strings.HasPrefix(l[0], "init auto ") || !strings.HasPrefix(l[1], "init cpu ") || strings.Fields(l[0])[2] == strings.Fields(l[1])[2] {
		t.Fatal(l)
	}
}

func TestNoRetry(t *testing.T) {
	for _, tc := range []struct {
		name, mode               string
		cpu, noFallback, stopped bool
		attempts                 int
	}{
		{"cpu failure", "always-fail", true, false, false, 1},
		{"no fallback", "always-fail", false, true, false, 1},
		{"stop", "always-fail", false, false, true, 1},
		{"two attempts only", "always-fail", false, false, false, 2},
		{"post-ready crash", "after-ready-fail", false, false, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, path := fakeClient(t, tc.mode)
			config := testConfig()
			if tc.cpu {
				config.Provider = CPU
			}
			if tc.noFallback {
				config.AllowCPUFallback = false
			}
			stop := make(chan struct{})
			if tc.stopped {
				close(stop)
			}
			err := c.Run(context.Background(), config, stop, func(Event) error { return nil })
			if err == nil {
				t.Fatal("expected failure")
			}
			n := 0
			for _, l := range lines(t, path) {
				if strings.HasPrefix(l, "init ") {
					n++
				}
			}
			if n != tc.attempts {
				t.Fatalf("%d attempts, want %d", n, tc.attempts)
			}
		})
	}
}

func TestProcessFailures(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{
		{"malformed", ErrProtocol}, {"oversized", ErrProtocol}, {"nonzero", ErrChild}, {"stderr", ErrChild}, {"no-ready", ErrStartupTimeout},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, _ := fakeClient(t, tc.mode)
			c.config.StartupTimeout = 150 * time.Millisecond
			config := testConfig()
			config.AllowCPUFallback = false
			err := c.Run(context.Background(), config, nil, func(Event) error { return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked child diagnostics")
			}
		})
	}
}

func TestBlockedInputAndCancellation(t *testing.T) {
	c, _ := fakeClient(t, "blocked-input")
	config := testConfig()
	config.AllowCPUFallback = false
	// More than a pipe buffer: the child never reads any of this init message.
	large := "/" + strings.Repeat("x", 4095)
	config.Parakeet = Graph{large, large, large, large}
	config.KWS = config.Parakeet
	config.KeywordsFile = large
	config.SileroModel = large
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Run(ctx, config, nil, func(Event) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	close(stop)
	if err := c.Run(context.Background(), config, stop, func(Event) error { return nil }); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatal(err)
	}
}

func TestReadyTimeoutFallback(t *testing.T) {
	c, path := fakeClient(t, "no-ready")
	c.config.StartupTimeout = 100 * time.Millisecond
	if err := c.Run(context.Background(), testConfig(), nil, func(Event) error { return nil }); !errors.Is(err, ErrStartupTimeout) {
		t.Fatal(err)
	}
	if got := lines(t, path); len(got) != 2 || !strings.HasPrefix(got[1], "init cpu ") {
		t.Fatal(got)
	}
}

func TestSilenceDoesNotStall(t *testing.T) {
	c, _ := fakeClient(t, "normal")
	c.config.StartupTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, testConfig(), nil, func(e Event) error {
			if e.Type == "ready" {
				close(ready)
			}
			return nil
		})
	}()
	<-ready
	select {
	case err := <-done:
		t.Fatalf("silence falsely timed out: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCancellationReleasesBlockedCallback(t *testing.T) {
	c, _ := fakeClient(t, "flood")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx, testConfig(), nil, func(Event) error { close(entered); <-release; return nil })
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback blocked cancellation")
	}
	close(release)
}

func TestBlockedConsumerAndOutput(t *testing.T) {
	for _, mode := range []string{"normal", "flood"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := fakeClient(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stop := make(chan struct{})
			release := make(chan struct{})
			entered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- c.Run(ctx, testConfig(), stop, func(e Event) error {
					if e.Type == "ready" {
						close(entered)
						<-release
					}
					return nil
				})
			}()
			<-entered
			close(stop)
			select {
			case err := <-done:
				if !errors.Is(err, ErrShutdownTimeout) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("blocked consumer wedged shutdown")
			}
			close(release)
		})
	}
}

func TestCallbackError(t *testing.T) {
	c, _ := fakeClient(t, "normal")
	want := errors.New("caller failure")
	if err := c.Run(context.Background(), testConfig(), nil, func(Event) error { return want }); err != want {
		t.Fatal(err)
	}
}

func TestGracefulShutdownDeadline(t *testing.T) {
	c, _ := fakeClient(t, "ignore-stop")
	stop := make(chan struct{})
	err := c.Run(context.Background(), testConfig(), stop, func(e Event) error {
		if e.Type == "ready" {
			close(stop)
		}
		return nil
	})
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatal(err)
	}
}

func TestStopCancelRace(t *testing.T) {
	for i := 0; i < 10; i++ {
		c, _ := fakeClient(t, "normal")
		ctx, cancel := context.WithCancel(context.Background())
		stop := make(chan struct{})
		var once sync.Once
		err := c.Run(ctx, testConfig(), stop, func(e Event) error {
			if e.Type == "ready" {
				once.Do(func() { go close(stop); cancel() })
			}
			return nil
		})
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestGroupCleanup(t *testing.T) {
	for _, mode := range []string{"descendant", "descendant-exit"} {
		t.Run(mode, func(t *testing.T) {
			c, path := fakeClient(t, mode)
			config := testConfig()
			config.AllowCPUFallback = false
			stop := make(chan struct{})
			err := c.Run(context.Background(), config, stop, func(e Event) error {
				if e.Type == "ready" {
					close(stop)
				}
				return nil
			})
			if mode == "descendant" && err != nil || mode == "descendant-exit" && !errors.Is(err, ErrChild) {
				t.Fatal(err)
			}
			pid := 0
			for _, l := range lines(t, path) {
				if strings.HasPrefix(l, "pid ") {
					pid, _ = strconv.Atoi(strings.TrimPrefix(l, "pid "))
				}
			}
			if pid == 0 {
				t.Fatal("missing descendant pid")
			}
			deadline := time.Now().Add(time.Second)
			for {
				data, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if errors.Is(e, os.ErrNotExist) || e == nil && strings.Contains(string(data), ") Z ") {
					break
				}
				if time.Now().After(deadline) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Fatal("descendant survived process-group cleanup")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestCommandEOFIsNotGraceful(t *testing.T) {
	_, err := DecodeCommand([]byte(`{"version":1,"type":"stop","epoch":"x","config":null}`))
	if err == nil {
		t.Fatal("null accepted")
	}
	if _, err := DecodeCommand(nil); err == nil || errors.Is(err, io.EOF) {
		t.Fatal("missing command accepted")
	}
}
