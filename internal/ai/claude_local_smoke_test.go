//go:build linux

package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// Opt-in actual native CLI transport smoke. All model responses come from this
// localhost fixture; the only credential is synthetic. No paid provider is used.
func TestClaudeCodeRealLocalTransportSmoke(t *testing.T) {
	if os.Getenv("GINKGO_CLAUDE_CODE_LOCAL_SMOKE") != "1" {
		t.Skip("opt-in localhost native transport smoke (no paid inference)")
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude not installed")
	}
	home := t.TempDir()
	config := filepath.Join(home, ".claude")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_") || key == "CLAUDECODE" {
			t.Setenv(key, "")
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("ANTHROPIC_API_KEY", "ginkgo-local-fixture-not-a-real-api-key")
	// Block incidental external requests even if a native feature ignores the
	// integration's nonessential-traffic switches. The real key is never present.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	marker := filepath.Join(home, "CANARY_EXECUTED")
	settings := fmt.Sprintf(`{"apiKeyHelper":%q,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":%q}]}]}}`, "touch "+marker, "touch "+marker)
	if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	const canary = "GINKGO_USER_INSTRUCTIONS_MUST_NOT_BE_SENT"
	if err := os.WriteFile(filepath.Join(config, "CLAUDE.md"), []byte(canary), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(io.LimitReader(r.Body, 10_000_001)).Decode(&body) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		var tools []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(body["tools"], &tools)
		stop := "end_turn"
		if len(tools) > 0 {
			stop = "tool_use"
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		message := map[string]any{"id": "msg_local_fixture", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event string, value any) {
			data, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		}
		emit("message_start", map[string]any{"type": "message_start", "message": message})
		block := map[string]any{"type": "text", "text": ""}
		delta := map[string]any{"type": "text_delta", "text": `{"ok":true}`}
		if stop == "tool_use" {
			block = map[string]any{"type": "tool_use", "id": "formatter_fixture", "name": "StructuredOutput", "input": map[string]any{}}
			delta = map[string]any{"type": "input_json_delta", "partial_json": `{"ok":true}`}
		}
		emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": block})
		emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	p, err := NewClaudeCode(ClaudeCodeOptions{Executable: executable, Options: Options{ImageInput: "supported", Timeout: 30 * time.Second}, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := claudeLocalIsolation(context.Background()); err != nil {
		t.Skip("native local policy blocks isolated fixture")
	}
	images := []imagecontext.Image{claudeImage(t, "png", color.White), claudeImage(t, "jpeg", color.Black)}
	for _, credential := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv(credential, "ginkgo-local-fixture-not-a-real-credential")
		for _, schema := range []bool{false, true} {
			request := Request{System: "GINKGO_FIXTURE_SYSTEM_ONLY", Prompt: "Analyze these images without taking actions.", Images: images}
			if schema {
				request.OutputSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
			}
			response, err := p.Complete(context.Background(), request)
			if err != nil || response.Text != `{"ok":true}` || response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
				t.Fatalf("native credential=%s schema=%v: %+v %v", credential, schema, response, err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 {
		t.Fatalf("expected four localhost model requests, got %d", len(requests))
	}
	for i, body := range requests {
		var tools []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body["tools"], &tools); err != nil && len(body["tools"]) > 0 {
			t.Fatal(err)
		}
		if (i%2 == 0 && len(tools) != 0) || (i%2 == 1 && (len(tools) != 1 || tools[0].Name != "StructuredOutput")) {
			t.Fatalf("action tools exposed: %s", body["tools"])
		}
		if strings.Contains(string(body["system"]), canary) || !strings.Contains(string(body["system"]), "GINKGO_FIXTURE_SYSTEM_ONLY") {
			t.Fatal("replacement system leaked or lost instructions")
		}
		var messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					Data      string `json:"data"`
					MediaType string `json:"media_type"`
				} `json:"source"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		var sent []string
		for _, message := range messages {
			for _, block := range message.Content {
				if block.Type == "image" {
					sent = append(sent, block.Source.Data)
				}
			}
		}
		if len(sent) != len(images) {
			t.Fatalf("native CLI dropped images: %d", len(sent))
		}
		for j, im := range images {
			if sent[j] != base64.StdEncoding.EncodeToString(im.Data) {
				t.Fatalf("native image %d bytes/order changed", j)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native user hook/helper executed")
	}
}
