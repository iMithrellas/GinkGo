package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// Capabilities returns configured or discovered image support for the selected model.
func (p *OpenAIProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	p.capMu.Lock()
	defer p.capMu.Unlock()
	if p.capabilities != nil {
		return *p.capabilities, nil
	}
	capabilities, err := p.discoverCapabilities(ctx)
	if err == nil && ctx.Err() == nil {
		p.capabilities = &capabilities
	}
	return capabilities, err
}

func (p *OpenAIProvider) discoverCapabilities(parent context.Context) (Capabilities, error) {
	if p.options.ImageInput != "auto" {
		return Capabilities{ImageInput: Support(p.options.ImageInput)}, nil
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
		return Capabilities{ImageInput: Unknown}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Capabilities{ImageInput: Unknown}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return Capabilities{ImageInput: Unknown}, nil
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
	if json.Unmarshal(data, &model) != nil || model.ID != p.options.Model {
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
