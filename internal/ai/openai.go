package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// OpenAIOptions configures a local or remote OpenAI-compatible chat endpoint.
// BaseURL includes the API prefix, e.g. http://127.0.0.1:11434/v1. APIKey is an
// optional secret resolved by the caller; providers never read global config.
type OpenAIOptions struct {
	Options
	BaseURL string
	Model   string
	APIKey  string
}

// OpenAIProvider performs stateless, non-streaming, tool-free chat completion.
type OpenAIProvider struct {
	options      OpenAIOptions
	capMu        capabilityGate
	capabilities *Capabilities
	capExpires   time.Time
}

// NewOpenAI validates options without contacting the endpoint.
func NewOpenAI(options OpenAIOptions) (*OpenAIProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(options.BaseURL)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("openai base URL must be an HTTP(S) API prefix without credentials or query")
	}
	if options.Model == "" {
		return nil, fmt.Errorf("openai model is required")
	}
	options.Options = limits
	options.BaseURL = strings.TrimRight(options.BaseURL, "/")
	return &OpenAIProvider{options: options}, nil
}

// Complete sends only system/user messages; no tools or executable instructions
// are supplied. Truncated completions and attempted tool calls fail closed.
func (p *OpenAIProvider) Complete(parent context.Context, request Request) (completion Response, completionErr error) {
	wantedImages := len(request.Images) != 0
	submittedImages := false
	submissionKnown := true
	defer func() {
		if wantedImages && submissionKnown {
			completion.ImagesSubmitted = &submittedImages
		}
	}()
	if len(request.System) > p.options.MaxInputBytes || len(request.Prompt) > p.options.MaxInputBytes-len(request.System) {
		return Response{}, ErrInputLimit
	}
	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	messages := []message{}
	if request.System != "" {
		messages = append(messages, message{Role: "system", Content: request.System})
	}
	var content any = request.Prompt
	if len(request.Images) != 0 {
		caps, err := p.Capabilities(parent)
		if err != nil || caps.ImageInput != Supported {
			request.Images = nil
		} else {
			images, err := imagecontext.Validate(parent, request.Images)
			if err != nil {
				return Response{}, err
			}
			parts := []any{map[string]any{"type": "text", "text": request.Prompt}}
			for _, image := range images {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]string{
						"url": "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
					},
				})
			}
			content = parts
		}
	}
	messages = append(messages, message{Role: "user", Content: content})
	input, err := json.Marshal(struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Stream   bool      `json:"stream"`
	}{Model: p.options.Model, Messages: messages})
	if err != nil {
		return Response{}, fmt.Errorf("encode openai request: %w", err)
	}
	if len(input) > encodedInputLimit(p.options.MaxInputBytes, request.Images) {
		return Response{}, ErrInputLimit
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, p.options.BaseURL+"/chat/completions", bytes.NewReader(input))
	if err != nil {
		return Response{}, fmt.Errorf("create openai request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if p.options.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+p.options.APIKey)
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	submissionKnown = false
	result, err := client.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		// Transport errors can contain credentials/notes in URLs or remote text.
		return Response{}, fmt.Errorf("openai request transport failed")
	}
	defer result.Body.Close()
	submissionKnown = true
	submittedImages = len(request.Images) != 0
	output, err := io.ReadAll(io.LimitReader(result.Body, int64(p.options.MaxOutputBytes)+1))
	if len(output) > p.options.MaxOutputBytes {
		return Response{}, ErrOutputLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("read openai response failed")
	}
	if result.StatusCode == 401 || result.StatusCode == 403 {
		return Response{}, ErrAuthentication
	}
	if result.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("openai completion returned HTTP %d", result.StatusCode)
	}
	var response struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role         string            `json:"role"`
				Content      *string           `json:"content"`
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage   `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	// Compatible APIs add metadata; reject malformed/trailing JSON but tolerate
	// extra response metadata. Only assistant text is ever consumed.
	decoder := json.NewDecoder(bytes.NewReader(output))
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF || len(response.Choices) != 1 {
		return Response{}, ErrProtocol
	}
	choice := response.Choices[0]
	if choice.FinishReason != "stop" || choice.Message.Role != "assistant" || choice.Message.Content == nil || len(choice.Message.ToolCalls) != 0 || (len(choice.Message.FunctionCall) != 0 && string(choice.Message.FunctionCall) != "null") {
		return Response{}, ErrProtocol
	}
	return Response{Text: *choice.Message.Content}, nil
}
