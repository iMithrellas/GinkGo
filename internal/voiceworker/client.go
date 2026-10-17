package voiceworker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

var (
	// ErrProtocol indicates invalid, incomplete, or out-of-order helper output.
	ErrProtocol = errors.New("voiceworker: protocol failure")
	// ErrStartupTimeout indicates that the initial ready handshake did not arrive.
	ErrStartupTimeout = errors.New("voiceworker: ready timeout")
	// ErrShutdownTimeout indicates that graceful stop or exit did not complete.
	ErrShutdownTimeout = errors.New("voiceworker: shutdown timeout")
	// ErrChild indicates a launch, transport, stderr limit, or process failure.
	ErrChild = errors.New("voiceworker: helper failure")
)

// ClientConfig selects a trusted installed helper, independently of wire config.
// Timeouts default to 30s startup and 5s shutdown, bounded to (0, 5m].
type ClientConfig struct {
	Executable      string
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
}

// Client is a lazy, concurrency-safe supervisor configuration. New never does I/O.
type Client struct{ config ClientConfig }

// New creates a lazy supervisor; validation and process launch happen only Run.
func New(config ClientConfig) *Client { return &Client{config: config} }

type readResult struct {
	data []byte
	err  error
}

// Run owns one isolated helper process group. Closing stop requests graceful
// finalization, including before ready. Context cancellation, callback errors,
// EOF without stopped, and crashes abort. A nil stop channel disables requests.
// emit is serialized and must return promptly; cancellation cannot interrupt
// arbitrary caller Go code, but still kills and reaps the helper independently.
// No subscriber connection or public IPC lifetime belongs in stop or ctx.
func (c *Client) Run(ctx context.Context, config Config, stop <-chan struct{}, emit func(Event) error) error {
	return c.run(ctx, config, stop, emit, false)
}

// Verify initializes the real native models and waits for a verified ready,
// then stops the helper without ever sending start or opening the microphone.
func (c *Client) Verify(ctx context.Context, config Config) error {
	if !config.Enabled {
		return errors.New("voiceworker: verification requires an enabled init")
	}
	return c.run(ctx, config, nil, func(Event) error { return nil }, true)
}

func (c *Client) run(ctx context.Context, config Config, stop <-chan struct{}, emit func(Event) error, verifyOnly bool) error {
	if !config.Enabled {
		return nil
	}
	if config.AttemptProvider != "" || config.FallbackReason != "" {
		return errors.New("voiceworker: fallback attempt is supervisor owned")
	}
	ready := false
	err := c.runAttempt(ctx, config, stop, emit, &ready, verifyOnly)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || ready || config.Provider == CPU || !config.AllowCPUFallback || (!errors.Is(err, ErrChild) && !errors.Is(err, ErrStartupTimeout)) {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-stop:
		return err
	default:
	}
	config.AttemptProvider = CPU
	config.FallbackReason = "cuda_init_failed"
	err = c.runAttempt(ctx, config, stop, emit, &ready, verifyOnly)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *Client) runAttempt(ctx context.Context, config Config, stop <-chan struct{}, emit func(Event) error, acceptedReady *bool, verifyOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.Validate(); err != nil {
		return err
	}
	cc := c.config
	if !filepath.IsAbs(cc.Executable) || emit == nil {
		return errors.New("voiceworker: invalid client configuration")
	}
	if cc.StartupTimeout == 0 {
		cc.StartupTimeout = 30 * time.Second
	}
	if cc.ShutdownTimeout == 0 {
		cc.ShutdownTimeout = 5 * time.Second
	}
	if cc.StartupTimeout < 0 || cc.StartupTimeout > 5*time.Minute || cc.ShutdownTimeout < 0 || cc.ShutdownTimeout > 5*time.Minute {
		return errors.New("voiceworker: invalid deadline")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrChild
	}
	epoch := hex.EncodeToString(nonce[:])
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return ErrChild
	}
	defer stdinR.Close()
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return ErrChild
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return ErrChild
	}
	defer stderrR.Close()
	defer stderrW.Close()
	cmd := exec.Command(cc.Executable)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cmd.Start() != nil {
		return ErrChild
	}
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	runCtx, cancel := context.WithCancel(ctx)
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	exited := false
	// Always kill the group, even after successful direct-child exit: descendants
	// may still hold pipes. os.File closes unblock our own transport goroutines.
	defer func() {
		cancel()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		stdinW.Close()
		stdoutR.Close()
		stderrR.Close()
		if !exited {
			<-wait
		}
	}()
	reads := make(chan readResult, 8)
	go func() {
		scanner := bufio.NewScanner(stdoutR)
		scanner.Buffer(make([]byte, 4096), MaxEventBytes+1)
		scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
			for i, b := range data {
				if b == '\n' {
					if i > MaxEventBytes {
						return 0, nil, ErrProtocol
					}
					return i + 1, data[:i], nil
				}
			}
			if atEOF && len(data) > 0 {
				return 0, nil, ErrProtocol
			}
			return 0, nil, nil
		})
		for scanner.Scan() {
			r := readResult{data: append([]byte(nil), scanner.Bytes()...)}
			select {
			case reads <- r:
			case <-runCtx.Done():
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		select {
		case reads <- readResult{err: err}:
		case <-runCtx.Done():
		}
	}()
	stderrFailure := make(chan struct{}, 1)
	go func() {
		n, _ := io.CopyN(io.Discard, stderrR, 64*1024+1)
		if n > 64*1024 {
			stderrFailure <- struct{}{}
		}
	}()
	stopWrite := make(chan struct{})
	startWrite := make(chan struct{})
	writes := make(chan error, 1)
	go func() {
		enc := json.NewEncoder(stdinW)
		err := enc.Encode(Command{Version: Version, Type: "init", Epoch: epoch, Config: &config})
		if err == nil {
			select {
			case <-stopWrite:
				err = enc.Encode(Command{Version: Version, Type: "stop", Epoch: epoch})
			case <-startWrite:
				select {
				case <-stopWrite:
				default:
					err = enc.Encode(Command{Version: Version, Type: "start", Epoch: epoch})
				}
				if err == nil {
					select {
					case <-stopWrite:
						err = enc.Encode(Command{Version: Version, Type: "stop", Epoch: epoch})
					case <-runCtx.Done():
						return
					}
				}
			case <-runCtx.Done():
				return
			}
		}
		if err == nil {
			err = stdinW.Close()
		}
		writes <- err
	}()
	// One callback goroutine bounds outstanding deliveries; never spawn one per
	// event. An uncooperative callback can leak at most this caller-owned goroutine.
	deliver := make(chan Event)
	delivered := make(chan error, 1)
	go func() {
		for {
			select {
			case e := <-deliver:
				err := emit(e)
				select {
				case delivered <- err:
				case <-runCtx.Done():
					return
				}
			case <-runCtx.Done():
				return
			}
		}
	}()
	startup := time.NewTimer(cc.StartupTimeout)
	defer startup.Stop()
	var startupC <-chan time.Time = startup.C
	retireStartup := func() {
		startup.Stop()
		startupC = nil
	}
	var shutdown *time.Timer
	var shutdownC <-chan time.Time
	defer func() {
		if shutdown != nil {
			shutdown.Stop()
		}
	}()
	beginShutdown := func() {
		if shutdown == nil {
			shutdown = time.NewTimer(cc.ShutdownTimeout)
			shutdownC = shutdown.C
		}
	}
	validator := NewValidator(epoch, config)
	stopping, stopped, eof := false, false, false
	requestStop := func() {
		stopping = true
		stop = nil
		close(stopWrite)
		retireStartup()
		beginShutdown()
	}
	var exitErr error
	var pending chan Event
	var ack <-chan error
	var current Event
	fatalEvent := false
	for {
		if eof && exited {
			if exitErr != nil {
				return ErrChild
			}
			if !stopped {
				return ErrProtocol
			}
			return nil
		}
		input := reads
		if pending != nil || ack != nil {
			input = nil
		}
		select {
		case pending <- current:
			pending = nil
			ack = delivered
		case err := <-ack:
			ack = nil
			if err != nil {
				return err
			}
			if fatalEvent {
				return ErrChild
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			requestStop()
		case <-startupC:
			// If stop and startup become ready together, stop owns the deadline.
			// Recheck it here rather than relying on select's random precedence.
			select {
			case <-stop:
				requestStop()
			default:
				return ErrStartupTimeout
			}
		case <-shutdownC:
			return ErrShutdownTimeout
		case <-stderrFailure:
			return ErrChild
		case err := <-writes:
			writes = nil
			if err != nil {
				return ErrChild
			}
		case err := <-wait:
			exited = true
			exitErr = err
			wait = nil
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			beginShutdown()
		case r := <-input:
			if r.err != nil {
				if r.err != io.EOF {
					return ErrProtocol
				}
				eof = true
				reads = nil
				beginShutdown()
				continue
			}
			e, err := validator.Decode(r.data)
			if err != nil {
				return fmt.Errorf("%w: invalid message", ErrProtocol)
			}
			if e.Type == "ready" {
				*acceptedReady = true
				retireStartup()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !stopping {
					if verifyOnly {
						requestStop()
					} else {
						select {
						case <-stop:
							requestStop()
						default:
							close(startWrite)
						}
					}
				}
			}
			if e.Type == "stopped" {
				if !stopping {
					return ErrProtocol
				}
				stopped = true
			}
			fatalEvent = e.Type == "error"
			current = e
			pending = deliver
		}
	}
}
