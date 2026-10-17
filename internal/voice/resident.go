package voice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ResidentConfig selects a trusted installed whisper-server and GGML model.
// Config limits/defaults also apply here; Executable defaults to whisper-server.
// Timeout includes queueing, startup and inference. StartupTimeout further bounds
// model loading. Only auto and cpu are supported; gpu requires unavailable engine
// attestation and is rejected before any I/O.
type ResidentConfig struct {
	Config
	StartupTimeout time.Duration
	// AllowLocalHTTP explicitly accepts upstream's unauthenticated loopback
	// endpoint. Do not enable on hosts with untrusted local users/processes.
	AllowLocalHTTP bool
}

// ResidentInfo contains no paths, audio, engine logs or local endpoint secrets.
type ResidentInfo struct {
	Enabled bool
	Running bool
	Closed  bool
	Engine  string
	Backend string // unknown: auto is not evidence of GPU use; cpu: --no-gpu.
}

// ErrResidentClosed indicates a resident session has been permanently closed.
var ErrResidentClosed = errors.New("resident voice session is closed")

// Resident serializes bounded WAV requests against one lazily loaded model.
// Call Close when the recording session ends. Failed/canceled active requests
// kill the worker; a subsequent request may lazily start a fresh worker.
type Resident struct {
	config ResidentConfig
	slot   chan struct{}
	life   context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	info   ResidentInfo
	worker *residentWorker // protected by slot
}

type residentWorker struct {
	cmd       *exec.Cmd
	done      chan struct{}
	client    *http.Client
	transport *http.Transport
	url       string
	model     string
	overflow  atomic.Bool
}

// NewResident constructs a lazy session without probing paths or software.
func NewResident(config ResidentConfig) *Resident {
	ctx, cancel := context.WithCancel(context.Background())
	return &Resident{config: config, slot: make(chan struct{}, 1), life: ctx, cancel: cancel,
		info: ResidentInfo{Enabled: config.Enabled, Engine: "whisper.cpp/server", Backend: "unknown"}}
}

// Info returns a bounded, concurrency-safe status snapshot without I/O.
func (r *Resident) Info() ResidentInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// Close cancels queued/active calls, kills the process group and waits for reap.
// It is idempotent and the session cannot be reopened.
func (r *Resident) Close() error {
	r.cancel()
	r.slot <- struct{}{}
	defer func() { <-r.slot }()
	r.stop()
	r.mu.Lock()
	r.info.Closed = true
	r.mu.Unlock()
	return nil
}

func (r *Resident) stop() {
	if w := r.worker; w != nil {
		w.kill()
		<-w.done
		w.transport.CloseIdleConnections()
		r.worker = nil
	}
	r.mu.Lock()
	r.info.Running = false
	r.mu.Unlock()
}

func (w *residentWorker) kill() { _ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL) }

// Transcribe accepts a complete bounded 16 kHz mono PCM16 WAV. Integration may
// submit revisable rolling windows and then a final utterance. Model overrides
// must equal the configured model; switching models requires a new session.
// Disabled takes precedence over closure. Otherwise an already-closed session
// returns ErrResidentClosed, even with a canceled caller context. For calls that
// begin while open, caller cancellation/deadlines observed during admission take
// precedence over concurrent Close; shutdown alone returns ErrResidentClosed
// before admission and context.Canceled once admitted for engine work.
func (r *Resident) Transcribe(ctx context.Context, wav []byte, options Options) (Transcript, error) {
	c := r.config.Config
	if !c.Enabled {
		return Transcript{}, ErrDisabled
	}
	if r.life.Err() != nil {
		return Transcript{}, ErrResidentClosed
	}
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if !r.config.AllowLocalHTTP {
		return Transcript{}, errors.New("resident whisper-server requires AllowLocalHTTP: its loopback API is unauthenticated; use only with trusted local users/processes")
	}
	if c.Device == "gpu" {
		return Transcript{}, errors.New("explicit GPU is unsupported: installed whisper-server cannot attest CUDA/Vulkan inference without CPU fallback; use auto (unverified) or cpu")
	}
	if options.Model != "" && options.Model != c.Model {
		return Transcript{}, errors.New("resident model override requires a new session")
	}
	if c.Executable == "" {
		c.Executable = "whisper-server"
	}
	var err error
	c, err = NewTranscriber(c).settings(Options{Language: options.Language})
	if err != nil {
		return Transcript{}, err
	}
	startup := r.config.StartupTimeout
	if startup == 0 {
		startup = time.Minute
	}
	if startup < 0 {
		return Transcript{}, errors.New("resident startup timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	select {
	case r.slot <- struct{}{}:
		defer func() { <-r.slot }()
	case <-ctx.Done():
		return Transcript{}, ctx.Err()
	case <-r.life.Done():
		if err := ctx.Err(); err != nil {
			return Transcript{}, err
		}
		return Transcript{}, ErrResidentClosed
	}
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	if r.life.Err() != nil {
		return Transcript{}, ErrResidentClosed
	}
	// Only admitted work links shutdown to engine cancellation. Queue admission
	// observes the lifecycle separately so its sentinel cannot race AfterFunc.
	unhook := context.AfterFunc(r.life, cancel)
	defer unhook()
	if int64(len(wav)) > c.MaxAudioBytes {
		return Transcript{}, errors.New("WAV exceeds audio byte limit")
	}
	if err := validateWAV(wav, c.MaxAudioDuration); err != nil {
		return Transcript{}, err
	}
	if r.worker != nil {
		select {
		case <-r.worker.done:
			r.stop()
		default:
		}
	}
	if r.worker == nil {
		startCtx, startCancel := context.WithTimeout(ctx, startup)
		err = r.start(startCtx, c)
		startCancel()
		if err != nil {
			r.stop()
			return Transcript{}, err
		}
	}
	w := r.worker
	// Multipart is bounded by the validated WAV plus small constant metadata.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "audio.wav")
	_, _ = part.Write(wav)
	_ = form.WriteField("response_format", "json")
	_ = form.WriteField("language", c.Language)
	_ = form.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, w.url, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := w.client.Do(req)
	if err != nil {
		r.stop()
		if ctx.Err() != nil {
			return Transcript{}, ctx.Err()
		}
		if w.overflow.Load() {
			return Transcript{}, ErrOutputLimit
		}
		return Transcript{}, errors.New("resident inference transport failed; verify installed whisper-server compatibility")
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, c.MaxOutputBytes+1))
	_ = resp.Body.Close()
	if int64(len(data)) > c.MaxOutputBytes || w.overflow.Load() {
		r.stop()
		return Transcript{}, ErrOutputLimit
	}
	if ctx.Err() != nil {
		r.stop()
		return Transcript{}, ctx.Err()
	}
	if readErr != nil || resp.StatusCode != http.StatusOK {
		r.stop()
		return Transcript{}, errors.New("resident inference failed")
	}
	var result struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(data, &result) != nil || result.Text == nil {
		r.stop()
		return Transcript{}, errors.New("resident response requires whisper-server JSON text schema")
	}
	language := c.Language
	if language == "auto" {
		language = ""
	}
	return Transcript{Text: *result.Text, Language: language, Engine: "whisper.cpp/server", Model: w.model}, nil
}

func (r *Resident) start(ctx context.Context, c Config) error {
	model, modelPath, err := openRegular(c.Model, c.MaxModelBytes)
	if err != nil {
		return errors.New("open installed resident GGML model failed")
	}
	defer model.Close()
	var magic [4]byte
	if _, err := io.ReadFull(model, magic[:]); err != nil || string(magic[:]) != "lmgg" {
		return errors.New("resident model requires whisper.cpp GGML magic")
	}
	_, _ = model.Seek(0, io.SeekStart)
	path, err := exec.LookPath(c.Executable)
	if err != nil {
		return errors.New("find installed whisper-server failed")
	}
	engine, _, err := openRegular(path, 0)
	if err != nil {
		return errors.New("open installed whisper-server failed")
	}
	defer engine.Close()
	info, err := engine.Stat()
	if err != nil || info.Mode().Perm()&0111 == 0 {
		return errors.New("whisper-server must be an executable regular file")
	}
	// Upstream cannot inherit a listener. Release an ephemeral port, then verify
	// the peer socket belongs to our child on EVERY dial, before sending audio.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("reserve resident loopback port failed")
	}
	address := listener.Addr().String()
	_, port, _ := net.SplitHostPort(address)
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		listener.Close()
		return errors.New("create resident endpoint secret failed")
	}
	prefix := "/" + hex.EncodeToString(secret[:])
	args := []string{"--host", "127.0.0.1", "--port", port, "--request-path", prefix, "--public", "/proc/self/fd/3/no-public", "-m", "/proc/self/fd/3", "-t", strconv.Itoa(c.Threads), "-l", c.Language, "-nt"}
	if c.Device == "cpu" {
		args = append(args, "--no-gpu")
	}
	cmd := exec.Command("/proc/self/fd/4", args...)
	cmd.ExtraFiles = []*os.File{model, engine}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.WaitDelay = time.Second
	w := &residentWorker{cmd: cmd, done: make(chan struct{}), model: modelPath}
	logs := &residentLog{limit: c.MaxOutputBytes, overflow: &w.overflow, kill: w.kill}
	cmd.Stdout, cmd.Stderr = logs, logs
	_ = listener.Close()
	if err := cmd.Start(); err != nil {
		return errors.New("start installed whisper-server failed")
	}
	r.worker = w
	w.transport = &http.Transport{Proxy: nil, DisableCompression: true, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return residentDial(ctx, address, cmd.Process.Pid)
		}}
	w.client = &http.Client{Transport: w.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w.url = "http://" + address + prefix + "/inference"
	go func() {
		_ = cmd.Wait()
		w.kill()
		r.mu.Lock()
		r.info.Running = false
		close(w.done)
		r.mu.Unlock()
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := residentDial(ctx, address, cmd.Process.Pid)
		if err == nil {
			_ = conn.Close()
			r.mu.Lock()
			select {
			case <-w.done:
				r.mu.Unlock()
				return errors.New("whisper-server exited during startup")
			default:
			}
			r.info.Running = true
			if c.Device == "cpu" {
				r.info.Backend = "cpu"
			}
			r.mu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.done:
			if w.overflow.Load() {
				return ErrOutputLimit
			}
			return errors.New("whisper-server exited during startup; verify flags and model compatibility")
		case <-ticker.C:
		}
	}
}

// residentLog discards diagnostics (which may contain transcript text), retaining
// only a bounded lifetime byte counter. Both process streams share the budget.
type residentLog struct {
	mu       sync.Mutex
	count    int64
	limit    int64
	overflow *atomic.Bool
	kill     func()
}

func (l *residentLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.overflow.Load() {
		l.count += int64(len(p))
		if l.count > l.limit {
			l.overflow.Store(true)
			l.kill()
		}
	}
	return len(p), nil
}

// residentDial fails closed unless the established server-side socket is owned
// by exactly our direct child. Checking the connected tuple, not just LISTEN,
// closes the free-port race and prevents audio reaching another local service.
func residentDial(ctx context.Context, address string, pid int) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, err
	}
	// Accepted sockets become visible in the child's fd table asynchronously.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if residentOwnsSocket(conn, pid) {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return nil, ctx.Err()
		case <-timer.C:
			conn.Close()
			return nil, errors.New("resident socket ownership not verified")
		case <-ticker.C:
		}
	}
}

func residentOwnsSocket(conn net.Conn, pid int) bool {
	local := conn.RemoteAddr().(*net.TCPAddr)
	remote := conn.LocalAddr().(*net.TCPAddr)
	// Linux /proc/net/tcp displays IPv4 addresses little-endian.
	tuple := func(a *net.TCPAddr) string {
		ip := a.IP.To4()
		return fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], a.Port)
	}
	file, err := os.Open("/proc/net/tcp")
	if err != nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	_ = file.Close()
	if err != nil || len(data) > 4<<20 {
		return false
	}
	return residentSocketTableOwned(data, tuple(local), tuple(remote), pid)
}
