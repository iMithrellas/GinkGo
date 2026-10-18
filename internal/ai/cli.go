//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// CLIOptions configures an explicitly trusted, foreground generation wrapper.
// Executable and Args are fixed administrator input, never model output. The
// wrapper reads one Request JSON object from stdin and writes exactly one
// Response JSON object to stdout (no events or diagnostic text). Diagnostics go
// to stderr and are intentionally not included in errors, to avoid leaking notes
// or credentials. The wrapper MUST disable tools/hooks/integrations, start fresh
// tasks and keep all work in its foreground process group; detached remote work
// cannot be cancelled by this adapter. Use NewOpenCode for direct OpenCode V2.
type CLIOptions struct {
	Options
	Executable string
	Args       []string
	TempDir    string
}

// CLIProvider implements the opt-in JSON wrapper contract in CLIOptions.
type CLIProvider struct{ options CLIOptions }

// Capabilities requires an explicit declaration for the wrapper's selected model.
func (p *CLIProvider) Capabilities(context.Context) (Capabilities, error) {
	support := Support(p.options.ImageInput)
	if support == "auto" {
		support = Unknown
	}
	return Capabilities{ImageInput: support}, nil
}

// NewCLI validates options without launching the wrapper or probing auth.
func NewCLI(options CLIOptions) (*CLIProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	if options.Executable == "" {
		return nil, fmt.Errorf("ai wrapper executable is required")
	}
	options.Options = limits
	options.Args = append([]string(nil), options.Args...)
	return &CLIProvider{options: options}, nil
}

// Complete runs a fresh foreground wrapper with bounded stdin/stdout/stderr.
func (p *CLIProvider) Complete(parent context.Context, request Request) (Response, error) {
	if p.options.ImageInput != "supported" {
		request.Images = nil
	}
	if len(request.Images) != 0 {
		images, err := imagecontext.Validate(parent, request.Images)
		if err != nil {
			return Response{}, err
		}
		request.Images = images
	}
	if len(request.System) > p.options.MaxInputBytes || len(request.Prompt) > p.options.MaxInputBytes-len(request.System) {
		return Response{}, ErrInputLimit
	}
	input, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("encode ai request: %w", err)
	}
	if len(input) > encodedInputLimit(p.options.MaxInputBytes, request.Images) {
		return Response{}, ErrInputLimit
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	defer cancel()
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-wrapper-")
	if err != nil {
		return Response{}, fmt.Errorf("create ai work directory: %w", err)
	}
	defer os.RemoveAll(dir)
	stdout := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	stderr := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	cmd := command(ctx, p.options.Executable, p.options.Args, dir, os.Environ(), bytes.NewReader(input))
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	_ = killGroup(cmd)
	if stdout.exceeded() || stderr.exceeded() {
		return Response{}, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if err != nil {
		return Response{}, fmt.Errorf("ai wrapper failed: %w", err)
	}
	var output struct {
		Text *string `json:"text"`
	}
	if err := decodeObject(stdout.bytes(), &output); err != nil || output.Text == nil {
		return Response{}, ErrProtocol
	}
	return Response{Text: *output.Text}, nil
}
