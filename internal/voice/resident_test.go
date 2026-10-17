package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// A separate copy of the test process acts as an installed server. No model,
// microphone, network service, Python, whisper installation or GPU is needed.
func init() {
	mode := os.Getenv("GINKGO_RESIDENT_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "child-hang" {
		<-time.After(time.Hour)
		os.Exit(0)
	}
	args := os.Args[1:]
	var host, port, prefix, model string
	cpu := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host":
			i++
			host = args[i]
		case "--port":
			i++
			port = args[i]
		case "--request-path":
			i++
			prefix = args[i]
		case "-m":
			i++
			model = args[i]
		case "-t", "-l", "--public":
			i++
		case "--no-gpu":
			cpu = true
		case "-nt":
		default:
			os.Exit(87)
		}
	}
	if host != "127.0.0.1" || len(prefix) != 65 {
		os.Exit(88)
	}
	data, err := os.ReadFile(model)
	if err != nil || !strings.HasPrefix(string(data), "lmgg") {
		os.Exit(89)
	}
	if mode == "startup-hang" {
		_ = os.WriteFile(os.Getenv("GINKGO_RESIDENT_MARKER"), []byte(strconv.Itoa(os.Getpid())), 0600)
		<-time.After(time.Hour)
	}
	if mode == "startup-exit" {
		os.Exit(10)
	}
	if mode == "startup-logs" {
		_, _ = os.Stderr.WriteString(strings.Repeat("private raw log", 10000))
		<-time.After(time.Hour)
	}
	if mode == "descendants" {
		child := exec.Command("/proc/self/exe")
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "GINKGO_RESIDENT_TEST_HELPER=") {
				child.Env = append(child.Env, env)
			}
		}
		child.Env = append(child.Env, "GINKGO_RESIDENT_TEST_HELPER=child-hang")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(94)
		}
		_ = os.WriteFile(os.Getenv("GINKGO_RESIDENT_MARKER"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, port))
	if err != nil {
		os.Exit(90)
	}
	var count int
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/inference", func(w http.ResponseWriter, req *http.Request) {
		count++
		if req.ParseMultipartForm(1<<20) != nil {
			os.Exit(91)
		}
		defer req.MultipartForm.RemoveAll()
		file, _, err := req.FormFile("file")
		if err != nil {
			os.Exit(92)
		}
		audio, _ := io.ReadAll(file)
		_ = file.Close()
		if validateWAV(audio, time.Minute) != nil || req.FormValue("response_format") != "json" {
			os.Exit(93)
		}
		switch mode {
		case "hang":
			_ = os.WriteFile(os.Getenv("GINKGO_RESIDENT_MARKER"), []byte("active"), 0600)
			<-req.Context().Done()
			return
		case "oversize":
			_, _ = io.WriteString(w, strings.Repeat("a", 10000))
			return
		case "bad-json":
			_, _ = io.WriteString(w, `{"error":"private secret"}`)
			return
		case "redirect":
			http.Redirect(w, req, "http://127.0.0.1:1/private-secret", http.StatusFound)
			return
		case "http-error":
			w.WriteHeader(500)
			_, _ = io.WriteString(w, "private secret")
			return
		case "inference-logs":
			_, _ = os.Stderr.WriteString(strings.Repeat("private secret", 10000))
			<-req.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": fmt.Sprintf("  exact\ntext %d %d %t %s  ", os.Getpid(), count, cpu, req.FormValue("language"))})
	})
	_ = http.Serve(listener, mux)
	os.Exit(0)
}

func residentFixture(t *testing.T, mode string) *Resident {
	t.Helper()
	t.Setenv("GINKGO_RESIDENT_TEST_HELPER", mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := writeFixture(t, filepath.Join(t.TempDir(), "model.bin"), []byte("lmggfake"), 0600)
	r := NewResident(ResidentConfig{Config: Config{Enabled: true, Executable: exe, Model: model, Device: "cpu", Timeout: 5 * time.Second}, StartupTimeout: 2 * time.Second, AllowLocalHTTP: true})
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestResidentReuseAndClose(t *testing.T) {
	r := residentFixture(t, "ok")
	if r.Info().Running {
		t.Fatal("not lazy")
	}
	ctx := context.Background()
	first, err := r.Transcribe(ctx, wavFixture(), Options{Language: "fr"})
	if err != nil {
		t.Fatal(err)
	}
	pid := r.worker.cmd.Process.Pid
	if first.Text != fmt.Sprintf("  exact\ntext %d 1 true fr  ", pid) || first.Language != "fr" {
		t.Fatalf("%#v", first)
	}
	second, err := r.Transcribe(ctx, wavFixture(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != fmt.Sprintf("  exact\ntext %d 2 true auto  ", pid) || second.Language != "" {
		t.Fatalf("%#v", second)
	}
	if info := r.Info(); !info.Running || info.Backend != "cpu" {
		t.Fatalf("%#v", info)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("worker not reaped")
	}
	if _, err := r.Transcribe(ctx, wavFixture(), Options{}); !errors.Is(err, ErrResidentClosed) {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResidentAutoUnknown(t *testing.T) {
	r := residentFixture(t, "ok")
	r.config.Device = "auto"
	result, err := r.Transcribe(context.Background(), wavFixture(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "false auto") || r.Info().Backend != "unknown" {
		t.Fatal(result, r.Info())
	}
}

func TestResidentValidationBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  ResidentConfig
		audio   []byte
		options Options
		want    error
	}{
		{"disabled", ResidentConfig{Config: Config{Executable: "/missing", Model: "/missing", Device: "gpu"}}, nil, Options{}, ErrDisabled},
		{"no-local-http-consent", ResidentConfig{Config: Config{Enabled: true}}, nil, Options{}, nil},
		{"gpu", ResidentConfig{Config: Config{Enabled: true, Device: "gpu"}, AllowLocalHTTP: true}, nil, Options{}, nil},
		{"bad-wav", ResidentConfig{Config: Config{Enabled: true}, AllowLocalHTTP: true}, []byte("bad"), Options{}, nil},
		{"model-override", ResidentConfig{Config: Config{Enabled: true}, AllowLocalHTTP: true}, wavFixture(), Options{Model: "other"}, nil},
		{"audio-limit", ResidentConfig{Config: Config{Enabled: true, MaxAudioBytes: 44}, AllowLocalHTTP: true}, wavFixture(), Options{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewResident(tc.config)
			defer r.Close()
			_, err := r.Transcribe(context.Background(), tc.audio, tc.options)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("got %v", err)
			}
			if r.worker != nil {
				t.Fatal("started worker")
			}
			if tc.name == "gpu" && !strings.Contains(err.Error(), "cannot attest CUDA/Vulkan") {
				t.Fatal(err)
			}
		})
	}
}

func TestResidentFailuresBoundedAndSanitized(t *testing.T) {
	for _, mode := range []string{"startup-hang", "startup-exit", "startup-logs", "oversize", "bad-json", "redirect", "http-error", "inference-logs"} {
		t.Run(mode, func(t *testing.T) {
			r := residentFixture(t, mode)
			if mode == "startup-hang" {
				r.config.StartupTimeout = 150 * time.Millisecond
			}
			r.config.MaxOutputBytes = 1024
			_, err := r.Transcribe(context.Background(), wavFixture(), Options{})
			if err == nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "http://") {
				t.Fatal("sensitive error", err)
			}
			if r.worker != nil || r.Info().Running {
				t.Fatal("worker not cleaned up")
			}
			if mode == "oversize" || mode == "startup-logs" || mode == "inference-logs" {
				if !errors.Is(err, ErrOutputLimit) {
					t.Fatal(err)
				}
			}
		})
	}
}

func waitResidentMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("inference did not start")
		case <-ticker.C:
		}
	}
}

func TestResidentQueueCancellationAndClose(t *testing.T) {
	r := residentFixture(t, "hang")
	marker := filepath.Join(t.TempDir(), "active")
	t.Setenv("GINKGO_RESIDENT_MARKER", marker)
	active := make(chan error, 1)
	go func() { _, err := r.Transcribe(context.Background(), wavFixture(), Options{}); active <- err }()
	waitResidentMarker(t, marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Transcribe(ctx, wavFixture(), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-active; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.Info().Running {
		t.Fatal("still running")
	}
}

func TestResidentActiveCancellationRestarts(t *testing.T) {
	r := residentFixture(t, "hang")
	marker := filepath.Join(t.TempDir(), "active")
	t.Setenv("GINKGO_RESIDENT_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.Transcribe(ctx, wavFixture(), Options{}); done <- err }()
	waitResidentMarker(t, marker)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.worker != nil {
		t.Fatal("canceled worker retained")
	}
	t.Setenv("GINKGO_RESIDENT_TEST_HELPER", "ok")
	if _, err := r.Transcribe(context.Background(), wavFixture(), Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestResidentRejectForeignSocketAndProxy(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := listener.Accept(); accepted <- c }()
	// A connected socket owned by this test process is NOT owned by PID 1.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := residentDial(ctx, listener.Addr().String(), 1); err == nil {
		conn.Close()
		t.Fatal("foreign owner accepted")
	}
	c := <-accepted
	defer c.Close()
	data, err := io.ReadAll(c)
	if err != nil || len(data) != 0 {
		t.Fatal("sent bytes to foreign process", len(data), err)
	}
	var proxyCalls atomic.Int64
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(500) })}
	proxyListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	go proxy.Serve(proxyListener)
	t.Setenv("HTTP_PROXY", "http://"+proxyListener.Addr().String())
	t.Setenv("ALL_PROXY", "http://"+proxyListener.Addr().String())
	t.Setenv("NO_PROXY", "")
	r := residentFixture(t, "ok")
	if _, err := r.Transcribe(context.Background(), wavFixture(), Options{}); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("used inherited proxy")
	}
}

func TestResidentSocketTableRejectsInvalid(t *testing.T) {
	for _, line := range []string{"", "truncated", "0: A B 0A 0 0 0 0 0 42", "0: A B 01 0 0 0 0 0 0"} {
		if residentSocketTableOwned([]byte(line), "A", "B", os.Getpid()) {
			t.Fatal("invalid table accepted", strconv.Quote(line))
		}
	}
}

func TestResidentStartupCancellationReaps(t *testing.T) {
	r := residentFixture(t, "startup-hang")
	marker := filepath.Join(t.TempDir(), "startup")
	t.Setenv("GINKGO_RESIDENT_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Transcribe(ctx, wavFixture(), Options{}); done <- err }()
	waitResidentMarker(t, marker)
	data, _ := os.ReadFile(marker)
	pid, _ := strconv.Atoi(string(data))
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if pid == 0 || syscall.Kill(pid, 0) == nil {
		t.Fatal("startup worker not reaped")
	}
}

func TestResidentCloseKillsDescendants(t *testing.T) {
	r := residentFixture(t, "descendants")
	marker := filepath.Join(t.TempDir(), "descendant")
	t.Setenv("GINKGO_RESIDENT_MARKER", marker)
	if _, err := r.Transcribe(context.Background(), wavFixture(), Options{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), ") Z ") {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("descendant survived group kill")
		case <-ticker.C:
		}
	}
}

func TestResidentConcurrentRequestsSerialize(t *testing.T) {
	r := residentFixture(t, "ok")
	const requests = 8
	results := make(chan error, requests)
	for i := 0; i < requests; i++ {
		go func() { _, err := r.Transcribe(context.Background(), wavFixture(), Options{}); results <- err }()
	}
	for i := 0; i < requests; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	result, err := r.Transcribe(context.Background(), wavFixture(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != fmt.Sprintf("  exact\ntext %d 9 true auto  ", r.worker.cmd.Process.Pid) {
		t.Fatalf("requests did not share worker: %q", result.Text)
	}
}

func TestResidentPostCloseSentinelConcurrent(t *testing.T) {
	r := NewResident(ResidentConfig{Config: Config{Enabled: true}, AllowLocalHTTP: true})
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Exercise the previously racing admission/AfterFunc paths repeatedly. No
	// installed engine/model is required: every call must fail before any I/O.
	wav := wavFixture()
	var callers sync.WaitGroup
	for i := 0; i < 32; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for j := 0; j < 100; j++ {
				if _, err := r.Transcribe(context.Background(), wav, Options{}); err != ErrResidentClosed {
					t.Errorf("post-close error = %v, want ErrResidentClosed", err)
					return
				}
			}
		}()
	}
	callers.Wait()
}

func TestResidentAdmissionErrorPrecedence(t *testing.T) {
	for _, closed := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("closed=%t/enabled=%t", closed, enabled), func(t *testing.T) {
				r := NewResident(ResidentConfig{Config: Config{Enabled: enabled}, AllowLocalHTTP: true})
				defer r.Close()
				if closed {
					_ = r.Close()
				}
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				want := error(context.Canceled)
				if closed {
					want = ErrResidentClosed
				}
				if !enabled {
					want = ErrDisabled
				}
				if _, err := r.Transcribe(ctx, wavFixture(), Options{}); err != want {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
	}
}

func TestResidentQueuedCloseSentinel(t *testing.T) {
	r := NewResident(ResidentConfig{Config: Config{Enabled: true}, AllowLocalHTTP: true})
	// Hold admission without launching a process so Close remains in progress
	// while pending requests observe shutdown. The worker remains untouched.
	r.slot <- struct{}{}
	results := make(chan error, 32)
	for i := 0; i < cap(results); i++ {
		go func() {
			_, err := r.Transcribe(context.Background(), wavFixture(), Options{})
			results <- err
		}()
	}
	closed := make(chan struct{})
	go func() { _ = r.Close(); close(closed) }()
	<-r.life.Done()
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != ErrResidentClosed {
			t.Errorf("shutdown admission error = %v, want ErrResidentClosed", err)
		}
	}
	<-r.slot
	<-closed
}

func TestResidentQueuedCallerDeadline(t *testing.T) {
	r := NewResident(ResidentConfig{Config: Config{Enabled: true}, AllowLocalHTTP: true})
	defer r.Close()
	r.slot <- struct{}{}
	defer func() { <-r.slot }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.Transcribe(ctx, wavFixture(), Options{}); err != context.DeadlineExceeded {
		t.Fatalf("queued deadline error = %v, want context.DeadlineExceeded", err)
	}
	if r.life.Err() != nil {
		t.Fatal("caller deadline closed resident session")
	}
}
