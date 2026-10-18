//go:build linux

package ai

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

func claudeInput(prompt string, images []imagecontext.Image) ([]byte, error) {
	content := []any{map[string]any{"type": "text", "text": prompt}}
	for _, im := range images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": im.MediaType, "data": base64.StdEncoding.EncodeToString(im.Data)}})
	}
	data, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}, "parent_tool_use_id": nil, "client_composed": true})
	return append(data, '\n'), err
}

func claudeFrames(data []byte, visit func(map[string]json.RawMessage) error) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), len(data)+1)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var frame map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame == nil {
			return ErrProtocol
		}
		var kind string
		if json.Unmarshal(frame["type"], &kind) != nil || kind == "" {
			return ErrProtocol
		}
		if err := visit(frame); err != nil {
			return err
		}
	}
	if scanner.Err() != nil {
		return ErrProtocol
	}
	return nil
}

func claudeModels(data []byte) ([]claudeModel, error) {
	var models []claudeModel
	found := false
	err := claudeFrames(data, func(frame map[string]json.RawMessage) error {
		if string(frame["type"]) != `"control_response"` {
			return nil
		}
		var wrapper struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Models []claudeModel `json:"models"`
			} `json:"response"`
		}
		if json.Unmarshal(frame["response"], &wrapper) != nil || wrapper.RequestID != "ginkgo-initialize" || wrapper.Subtype != "success" || found {
			return ErrProtocol
		}
		found = true
		models = wrapper.Response.Models
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrProtocol
	}
	return models, nil
}

// The audited 2.1.299 init reports built-in plugin metadata even in safe mode.
// sec-default is conditional on managed settings/Team/Enterprise and participates
// in native security policy; do not turn it off to make subscription auth work.
// These entries are not action-tool grants. Validate the actual model toolset.
// Never accept a similarly named user/plugin path or a new unaudited builtin.
func claudeBuiltinPlugin(plugin json.RawMessage) bool {
	var p struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if json.Unmarshal(plugin, &p) != nil || p.Path != "builtin" || p.Source != p.Name+"@builtin" {
		return false
	}
	return p.Name == "cc-plugin-agents-md" || p.Name == "cc-plugin-plugin-authoring" || p.Name == "cc-plugin-sec-default"
}

func claudeResult(data []byte, schema bool) (Response, error) {
	var response Response
	found := false
	initialized := false
	err := claudeFrames(data, func(frame map[string]json.RawMessage) error {
		var kind, subtype string
		_ = json.Unmarshal(frame["type"], &kind)
		_ = json.Unmarshal(frame["subtype"], &subtype)
		if kind == "control_request" {
			return ErrGenerationRestricted
		}
		// Safe mode intentionally retains mandatory managed hooks. Their native
		// lifecycle events are not model action-tool calls or approval requests.
		if kind == "system" && subtype == "init" {
			if initialized {
				return ErrProtocol
			}
			initialized = true
			var tools []string
			if bytes.Equal(bytes.TrimSpace(frame["tools"]), []byte("null")) || json.Unmarshal(frame["tools"], &tools) != nil {
				return ErrProtocol
			}
			if (!schema && len(tools) != 0) || (schema && (len(tools) != 1 || tools[0] != "StructuredOutput")) {
				return ErrGenerationRestricted
			}
			for _, key := range []string{"mcp_servers", "skills", "slash_commands"} {
				var items []json.RawMessage
				if value, ok := frame[key]; ok && (json.Unmarshal(value, &items) != nil || len(items) > 0) {
					return ErrGenerationRestricted
				}
			}
			var plugins []json.RawMessage
			if raw, ok := frame["plugins"]; ok {
				if json.Unmarshal(raw, &plugins) != nil {
					return ErrProtocol
				}
				for _, plugin := range plugins {
					if !claudeBuiltinPlugin(plugin) {
						return ErrGenerationRestricted
					}
				}
			}
		}
		if kind == "assistant" {
			var message struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content"`
			}
			if json.Unmarshal(frame["message"], &message) != nil {
				return ErrProtocol
			}
			for _, block := range message.Content {
				if strings.HasSuffix(block.Type, "tool_use") && (block.Type != "tool_use" || !schema || block.Name != "StructuredOutput") {
					return ErrGenerationRestricted
				}
			}
		}
		if kind != "result" {
			return nil
		} // Progress/partial text is never the answer.
		if found {
			return ErrProtocol
		}
		found = true
		var isError bool
		if json.Unmarshal(frame["is_error"], &isError) != nil {
			return ErrProtocol
		}
		if isError || subtype != "success" {
			if err := claudeFailure(frame["result"], frame["errors"], frame["api_error_status"]); err != nil {
				return err
			}
			return ErrProviderUnavailable
		}
		if !initialized {
			return ErrProtocol
		}
		// A validated init followed by a complete native success result confirms
		// admission of the submitted turn even if its final output is unusable.
		// This is transport evidence only, not a claim the model used an image.
		admitted := true
		response.ImagesSubmitted = &admitted
		if schema {
			structured, ok := frame["structured_output"]
			if !ok || bytes.Equal(bytes.TrimSpace(structured), []byte("null")) {
				return ErrProtocol
			}
			var normalized bytes.Buffer
			if json.Compact(&normalized, structured) != nil {
				return ErrProtocol
			}
			response.Text = normalized.String()
		} else {
			if json.Unmarshal(frame["result"], &response.Text) != nil {
				return ErrProtocol
			}
		}
		return nil
	})
	if err != nil {
		return Response{ImagesSubmitted: response.ImagesSubmitted}, err
	}
	if !found {
		return Response{ImagesSubmitted: response.ImagesSubmitted}, ErrProtocol
	}
	return response, nil
}

// Provider diagnostics can contain prompts/credentials. Classify, never echo.
func claudeFailure(parts ...[]byte) error {
	var combined strings.Builder
	for _, part := range parts {
		combined.Write(part)
		combined.WriteByte(' ')
	}
	text := strings.ToLower(combined.String())
	switch {
	case strings.Contains(text, "cannot use --strict-mcp-config when an enterprise mcp config is present"), strings.Contains(text, "cannot dynamically configure mcp servers when an enterprise mcp config is present"):
		return fmt.Errorf("%w: native enterprise MCP policy conflicts with strict empty MCP configuration; ask the administrator for a compatible generation-only runtime", ErrClaudeCodeIsolation)
	case strings.Contains(text, "authentication"), strings.Contains(text, "not logged in"), strings.Contains(text, "invalid api key"), strings.Contains(text, "oauth"), strings.Contains(text, "401"), strings.Contains(text, "403"):
		return ErrAuthentication
	case strings.Contains(text, "rate_limit"), strings.Contains(text, "rate limit"), strings.Contains(text, "429"):
		return ErrRateLimited
	case strings.Contains(text, "model_not_found"), strings.Contains(text, "model not found"), strings.Contains(text, "model unavailable"), strings.Contains(text, "model_not_available"), strings.Contains(text, "invalid model"):
		return ErrModelUnavailable
	case strings.Contains(text, "overloaded"), strings.Contains(text, "503"), strings.Contains(text, "529"):
		return ErrProviderUnavailable
	}
	return nil
}

var _ Provider = (*ClaudeCodeProvider)(nil)
var _ CapabilityProvider = (*ClaudeCodeProvider)(nil)
var _ ImageRequestPreflighter = (*ClaudeCodeProvider)(nil)
