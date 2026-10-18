package imagecontext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// CommandRunner runs a capture command with a limit on stdout size.
type CommandRunner interface {
	Run(ctx context.Context, executable string, args []string, limit int) ([]byte, error)
}

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, executable string, args []string, limit int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = 250 * time.Millisecond
	output := &boundedOutput{limit: limit}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s capture command failed", executable)
	}
	return output.Bytes(), nil
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("capture output exceeds limit")
	}
	return b.Buffer.Write(p)
}
