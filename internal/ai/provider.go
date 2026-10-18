// Package ai provides note generation with text and image inputs.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// Request contains system instructions, user context, and optional images.
type Request struct {
	System string               `json:"system"`
	Prompt string               `json:"prompt"`
	Images []imagecontext.Image `json:"images,omitempty"`
}

func decodeObject(input []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return ErrProtocol
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}

// Response contains uninterpreted model output.
type Response struct {
	Text string `json:"text"`
}

// Provider generates text without performing application actions.
type Provider interface {
	Complete(context.Context, Request) (Response, error)
}

// Options bounds a single request, including startup and response transport.
// Zero fields select conservative defaults. Negative values are invalid.
type Options struct {
	Timeout        time.Duration
	MaxInputBytes  int
	MaxOutputBytes int
	// ImageInput declares selected-model support when metadata is unavailable:
	// auto (default), supported or unsupported. Adapters must also support images.
	ImageInput string
}

var (
	// ErrInputLimit indicates an oversized encoded request.
	ErrInputLimit = errors.New("ai input exceeds byte limit")
	// ErrOutputLimit indicates oversized provider output (including framing).
	ErrOutputLimit = errors.New("ai output exceeds byte limit")
	// ErrProtocol indicates incompatible or malformed provider framing.
	ErrProtocol = errors.New("invalid ai provider response")
	// ErrAuthentication indicates rejected provider/server credentials.
	ErrAuthentication = errors.New("ai provider authentication failed")
	// ErrModelUnavailable indicates explicit rejection of the selected model.
	ErrModelUnavailable = errors.New("ai provider model unavailable")
	// ErrGenerationRestricted means the provider refuses generation-only use.
	ErrGenerationRestricted = errors.New("ai provider rejects generation-only use")
	// ErrProviderUnavailable indicates an upstream availability failure.
	ErrProviderUnavailable = errors.New("ai provider temporarily unavailable")
	// ErrRateLimited indicates the provider rejected the request rate.
	ErrRateLimited = errors.New("ai provider rate limited")
	// ErrOpenCodeConsole indicates saved Console credentials cannot be reused
	// without allowing remote policy/configuration to weaken local isolation.
	ErrOpenCodeConsole = errors.New("opencode generation isolation does not support saved Console credentials; use a separate OpenCode profile without Console or an explicitly isolated wrapper")
)

func (o Options) normalized() (Options, error) {
	if o.ImageInput == "" {
		o.ImageInput = "auto"
	}
	if o.ImageInput != "auto" && o.ImageInput != "supported" && o.ImageInput != "unsupported" {
		return o, fmt.Errorf("image_input must be auto, supported or unsupported")
	}
	if o.Timeout < 0 || o.MaxInputBytes < 0 || o.MaxOutputBytes < 0 {
		return o, fmt.Errorf("ai limits must be nonnegative")
	}
	if o.Timeout == 0 {
		o.Timeout = 120 * time.Second
	}
	if o.MaxInputBytes == 0 {
		o.MaxInputBytes = 1 << 20
	}
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = 1 << 20
	}
	return o, nil
}

// encodedInputLimit allows for base64 image data and message framing.
func encodedInputLimit(textLimit int, images []imagecontext.Image) int {
	if len(images) == 0 {
		return textLimit
	}
	return textLimit + imagecontext.MaxTotalBytes*4/3 + (64 << 10)
}
