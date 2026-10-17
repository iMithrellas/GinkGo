package sherpavoice

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"
)

// PCMSource owns one recorder and its pipe. Close must unblock Read, terminate
// and reap the recorder. Only one caller reads; Close may run concurrently.
type PCMSource interface{ io.ReadCloser }

type pipeSource struct {
	io.ReadCloser
	cmd  *exec.Cmd
	once sync.Once
}

func (s *pipeSource) Close() error {
	s.once.Do(func() {
		_ = s.ReadCloser.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
	})
	return nil
}

// StartMicrophone uses one trusted installed PipeWire executable, fixed format
// arguments and an optional target, without a shell. Call only after start.
func StartMicrophone(executable, target string) (PCMSource, error) {
	args := []string{"--rate=16000", "--channels=1", "--format=s16", "--raw"}
	if target != "" {
		args = append(args, "--target", target)
	}
	args = append(args, "-")
	cmd := exec.Command(executable, args...)
	// Native/model/recorder diagnostics never flow into application events.
	cmd.Stderr = io.Discard
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("recorder pipe failed")
	}
	if err = cmd.Start(); err != nil {
		_ = pipe.Close()
		return nil, errors.New("recorder start failed")
	}
	return &pipeSource{ReadCloser: pipe, cmd: cmd}, nil
}

// Capture feeds the frontend independently of offline ASR. A full 8-frame
// capture queue faults rather than shifting sample offsets or growing memory.
// Graceful stop flushes; cancellation, source EOF, and errors discard and join.
func Capture(ctx context.Context, stop <-chan struct{}, source PCMSource, e *Engine) error {
	// Publication follows the caller's lifetime even after graceful stop starts.
	// The separate child context below only controls the recorder reader.
	e.authority = ctx
	if e.stopping == nil {
		e.stopping = stop
	}
	ctx, cancel := context.WithCancel(ctx)
	frames := make(chan []float32, 8)
	errorsOut := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			buf := make([]byte, 1024)
			_, err := io.ReadFull(source, buf)
			if err != nil {
				select {
				case errorsOut <- errors.New("recorder stream ended"):
				default:
				}
				return
			}
			p := make([]float32, 512)
			for i := range p {
				p[i] = float32(int16(binary.LittleEndian.Uint16(buf[2*i:]))) / 32768
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case frames <- p:
			default:
				select {
				case errorsOut <- errors.New("capture queue overflow"):
				default:
				}
				return
			}
		}
	}()
	cleanup := func() { cancel(); _ = source.Close(); <-done }
	defer e.Cancel()
	stall := time.NewTimer(5 * time.Second)
	defer stall.Stop()
	for {
		select {
		case <-stall.C:
			cleanup()
			return errors.New("recorder stalled")
		case <-ctx.Done():
			cleanup()
			return ctx.Err()
		case <-stop:
			cleanup()
			return e.Stop()
		case err := <-errorsOut:
			cleanup()
			return err
		case p := <-frames:
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(5 * time.Second)
			if err := e.Process(p); err != nil {
				cleanup()
				return err
			}
		}
	}
}
