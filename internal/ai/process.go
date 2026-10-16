//go:build linux

package ai

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// limitedBuffer also stops an endlessly writing process immediately, rather than
// retaining a truncated result and waiting until the request timeout.
type limitedBuffer struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
	onWrite  func([]byte)
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > b.limit-b.data.Len() {
		b.overflow = true
		b.cancel()
		return 0, ErrOutputLimit
	}
	_, _ = b.data.Write(p)
	if b.onWrite != nil {
		b.onWrite(p)
	}
	return len(p), nil
}

func (b *limitedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data.Bytes())
}

func (b *limitedBuffer) exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

func command(ctx context.Context, executable string, args []string, dir string, env []string, stdin io.Reader) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, env, stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second
	return cmd
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
