//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// This opt-in check uses only a loopback Responses mock and synthetic metadata.
// It never reads credentials or allows native traffic to a paid model endpoint.
func TestCodexNativeLoopbackIsolation(t *testing.T) {
	if os.Getenv("GINKGO_CODEX_NATIVE_SMOKE") != "1" {
		t.Skip("opt in with GINKGO_CODEX_NATIVE_SMOKE=1; loopback mock only, no paid inference")
	}
	for _, auth := range []string{"api-key", "chatgpt"} {
		t.Run(auth, func(t *testing.T) { codexNativeLoopbackIsolation(t, auth) })
	}
}

func codexNativeLoopbackIsolation(t *testing.T, auth string) {
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	root := t.TempDir()
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	canary := "GINKGO_FORBIDDEN_REAL_HOME_CANARY"
	skill := filepath.Join(outside, ".agents", "skills", "audit-skill")
	if err = os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: audit-skill\ndescription: audit\n---\n"+canary), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", outside)
	if err = codexPreflight(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// These are deliberately invalid synthetic credentials in disposable homes.
	// ChatGPT exercises actual native saved-auth loading/status, NOT a real OAuth
	// exchange or refresh. The auth-free loopback provider below exercises actual
	// native Responses framing, NOT authenticated ChatGPT backend transport.
	if auth == "api-key" {
		login := exec.CommandContext(ctx, executable, "login", "--with-api-key")
		login.Dir, login.Env, login.Stdin = root, codexEnvironment(home, root), strings.NewReader("sk-ginkgo-loopback-only-not-a-real-key\n")
		if output, err := login.CombinedOutput(); err != nil {
			t.Fatalf("synthetic native login: %v %s", err, output)
		}
	} else {
		claims, _ := json.Marshal(map[string]any{"email": "synthetic@example.invalid", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "synthetic-account", "chatgpt_plan_type": "plus", "chatgpt_user_id": "synthetic-user"}})
		jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
		data, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"id_token": jwt, "access_token": jwt, "refresh_token": "synthetic-not-a-real-refresh-token", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC().Format(time.RFC3339)})
		if err := os.WriteFile(filepath.Join(home, "auth.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = codexVerifyVersion(ctx, executable, root, codexEnvironment(home, root)); err != nil {
		t.Fatal(err)
	}
	if err = codexVerifyNativeAuth(ctx, executable, root, codexEnvironment(home, root)); err != nil {
		t.Fatal(err)
	}
	m := codexModel{ID: "audit-model", Model: "audit-model", DisplayName: "Loopback audit", Modalities: []string{"text", "image"}}
	catalog, err := codexWriteCatalog(root, m)
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan []byte, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, imagecontext.MaxTotalBytes*4/3+(1<<20)))
		captured <- data
		if r.Header.Get("Content-Encoding") != "" {
			t.Errorf("unexpected request compression: %s", r.Header.Get("Content-Encoding"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		item := `{"type":"message","id":"msg_audit","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"answer\":\"OK\"}"}]}`
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"audit-response\",\"status\":\"completed\",\"output\":[%s],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n", item, item)
	}))
	defer server.Close()
	args := codexArgs(catalog)
	provider := `model_providers.audit={name="Loopback only",base_url="` + server.URL + `/v1",wire_api="responses",requires_openai_auth=false}`
	args = append(args, "-c", `model_provider="audit"`, "-c", provider, "--disable", "enable_request_compression")
	runtime, err := codexStartRuntime(ctx, executable, args, root, codexEnvironment(home, root), 1<<20)
	if err != nil {
		// Diagnostics are safe here: this separate process has only synthetic
		// loopback-provider configuration and an empty authentication profile.
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir, cmd.Env = root, codexEnvironment(home, root)
		output, _ := cmd.CombinedOutput()
		t.Fatalf("%v; synthetic native startup diagnostic: %s", err, output)
	}
	defer runtime.stop()
	var metadata struct {
		Data []codexModel `json:"data"`
	}
	if err = runtime.call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": true}, &metadata, nil); err != nil {
		t.Fatalf("native catalog metadata: %v", err)
	}
	if len(metadata.Data) != 1 || metadata.Data[0].Model != m.Model || !strings.Contains(strings.Join(metadata.Data[0].Modalities, ","), "image") {
		t.Fatalf("native catalog metadata changed: %+v", metadata)
	}
	var thread struct {
		Thread struct {
			ID        string `json:"id"`
			Ephemeral bool   `json:"ephemeral"`
		} `json:"thread"`
		Instructions []string `json:"instructionSources"`
	}
	if err = runtime.call(ctx, "thread/start", map[string]any{"model": m.Model, "modelProvider": "audit", "allowProviderModelFallback": false, "ephemeral": true, "environments": []any{}, "selectedCapabilityRoots": []any{}, "cwd": root, "approvalPolicy": "never", "sandbox": "read-only", "baseInstructions": "Return only the requested answer.", "developerInstructions": "", "dynamicTools": []any{}}, &thread, nil); err != nil {
		t.Fatalf("thread: %v stderr=%s", err, runtime.stderr.bytes())
	}
	if !thread.Thread.Ephemeral || len(thread.Instructions) > 0 {
		t.Fatalf("thread isolation: %+v", thread)
	}
	state := codexTurnState{thread: thread.Thread.ID, limit: 1 << 20}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	images := codexTestImages(t)
	large := codexLargeImages(t)
	framing := &CodexProvider{root: root, options: CodexOptions{Options: Options{MaxOutputBytes: 1 << 20}}}
	paths, cleanupImages, err := framing.imagePaths(large, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupImages()
	schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}, "additionalProperties": false}
	if err = runtime.call(ctx, "turn/start", map[string]any{"threadId": thread.Thread.ID, "outputSchema": schema, "input": []any{map[string]any{"type": "text", "text": "Return OK. $audit-skill"}, map[string]any{"type": "image", "url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(images[0].Data)}, map[string]any{"type": "localImage", "path": paths[0]}, map[string]any{"type": "localImage", "path": paths[1]}}}, &turn, state.event); err != nil {
		t.Fatalf("turn: %v stderr=%s", err, runtime.stderr.bytes())
	}
	state.turn = turn.Turn.ID
	for !state.done {
		message, e := runtime.next(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if e = state.event(message); e != nil {
			t.Fatalf("%v message=%s params=%s", e, message.Method, message.Params)
		}
	}
	if text, e := state.result(); e != nil || text != `{"answer":"OK"}` {
		t.Fatalf("text=%q err=%v stderr=%s", text, e, runtime.stderr.bytes())
	}
	select {
	case data := <-captured:
		var req struct {
			Tools []json.RawMessage `json:"tools"`
			Model string            `json:"model"`
		}
		if json.Unmarshal(data, &req) != nil || req.Tools == nil || len(req.Tools) != 0 || req.Model != m.Model {
			t.Fatalf("not generation-only: %s", data)
		}
		if strings.Contains(string(data), canary) {
			t.Fatal("real-home skill content leaked into native request")
		}
		if !strings.Contains(string(data), `"input_image"`) || !strings.Contains(string(data), `"json_schema"`) || !strings.Contains(string(data), `"strict":true`) {
			t.Fatalf("image or strict output schema was lost (request bytes=%d)", len(data))
		}
		if bytes.Count(data, []byte(`"input_image"`)) != 3 {
			t.Fatalf("native runtime dropped an image (input bytes=%d,%d; request bytes=%d)", len(large[0].Data), len(large[1].Data), len(data))
		}
		t.Logf("native tools=[]; strict schema; 3 input_image items, including %d and %d byte local images; protocol output %d bytes (limit 1 MiB)", len(large[0].Data), len(large[1].Data), runtime.budget)
	case <-ctx.Done():
		t.Fatal("native runtime never reached loopback mock")
	}
	closed := false
	cleanup := func(message codexMessage) error {
		if message.Method == "thread/closed" {
			closed = true
		}
		return nil
	}
	if err = runtime.call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.Thread.ID}, nil, cleanup); err != nil {
		t.Fatal(err)
	}
	for !closed {
		message, err := runtime.next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = cleanup(message)
	}
	runtime.stop()
	if err = codexPreflight(ctx, home); err != nil {
		entries, _ := os.ReadDir(home)
		t.Fatalf("native runtime artifact allowlist: %v inventory=%v", err, entries)
	}
}
