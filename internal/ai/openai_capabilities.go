package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// PreflightImages rechecks effective metadata before the service finalizes its
// image count. A transient discovery failure filled by explicit configuration
// must not leave an image-bearing prompt after recovery to text-only metadata.
// No note content is sent during discovery.
func (p *OpenAIProvider) PreflightImages(ctx context.Context, request Request) error {
	if len(request.Images) == 0 {
		return ctx.Err()
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return err
	}
	if caps.ImageInput != Supported {
		return fmt.Errorf("openai-compatible image input is unavailable")
	}
	return nil
}

// Capabilities returns configured or discovered image support for the selected model.
func (p *OpenAIProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	if err := p.capMu.lock(ctx); err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	defer p.capMu.unlock()
	if p.capabilities != nil && time.Now().Before(p.capExpires) {
		return *p.capabilities, nil
	}
	capabilities, err := p.discoverCapabilities(ctx)
	capabilities.ResolvedModel = p.options.Model
	capabilities.ImageInput = EffectiveImageSupport(capabilities.ImageInput, Supported, p.options.ImageInput)
	if err == nil && ctx.Err() == nil {
		p.capabilities = &capabilities
		p.capExpires = time.Now().Add(5 * time.Minute)
	}
	// Explicit configuration can fill missing metadata, but an unsuccessful
	// lookup is still not cached and cannot override known text-only metadata.
	if err != nil && capabilities.ImageInput == Supported && p.options.ImageInput == "supported" && ctx.Err() == nil {
		capabilities.Uncacheable = true
		return capabilities, nil
	}
	return capabilities, err
}

func (p *OpenAIProvider) discoverCapabilities(parent context.Context) (Capabilities, error) {
	if p.options.ImageInput == "unsupported" {
		return Capabilities{ImageInput: EffectiveImageSupport(Unknown, Supported, p.options.ImageInput), ResolvedModel: p.options.Model}, nil
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.options.BaseURL+"/models/"+url.PathEscape(p.options.Model), nil)
	if err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	if p.options.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.options.APIKey)
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return Capabilities{ImageInput: Unknown}, ErrProviderUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		// Compatible endpoints need not implement model metadata. Missing metadata
		// is distinct from a retryable transport/authentication/server failure.
		return Capabilities{ImageInput: Unknown}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return Capabilities{ImageInput: Unknown}, ErrProviderUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return Capabilities{ImageInput: Unknown}, ErrProtocol
	}
	// Compatible endpoints expose different metadata extensions. Missing fields
	// leave support unknown; a model name alone does not establish image support.
	var model struct {
		ID              string   `json:"id"`
		InputModalities []string `json:"input_modalities"`
		Architecture    struct {
			InputModalities []string `json:"input_modalities"`
		} `json:"architecture"`
		Capabilities struct {
			Vision *bool    `json:"vision"`
			Input  []string `json:"input"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(data, &model) != nil {
		return Capabilities{ImageInput: Unknown}, ErrProtocol
	}
	if model.ID != p.options.Model {
		return Capabilities{ImageInput: Unknown}, nil
	}
	for _, inputs := range [][]string{model.InputModalities, model.Architecture.InputModalities, model.Capabilities.Input} {
		if inputs == nil {
			continue
		}
		if slices.Contains(inputs, "image") {
			return Capabilities{ImageInput: Supported}, nil
		}
		return Capabilities{ImageInput: Unsupported}, nil
	}
	if model.Capabilities.Vision != nil {
		if *model.Capabilities.Vision {
			return Capabilities{ImageInput: Supported}, nil
		}
		return Capabilities{ImageInput: Unsupported}, nil
	}
	return Capabilities{ImageInput: Unknown}, nil
}
