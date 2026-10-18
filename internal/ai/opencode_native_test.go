//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

type nativeCapture struct {
	Path   string
	Method string
	Body   json.RawMessage
	Config json.RawMessage
}

func nativeFixture(t *testing.T, mode string) (*OpenCodeNativeProvider, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "opencode")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec \"$GINKGO_NATIVE_TEST_BINARY\" -test.run='^TestOpenCodeNativeHelper$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture")
	t.Setenv("GINKGO_NATIVE_TEST_BINARY", binary)
	t.Setenv("GINKGO_NATIVE_TEST_MODE", mode)
	t.Setenv("GINKGO_NATIVE_CAPTURE", capture)
	p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: executable, TempDir: dir, Options: Options{Timeout: 5 * time.Second, MaxOutputBytes: 8192}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, capture
}

// A fake executable exercises process startup, Basic auth, private config,
// durable admission/wait ordering, crash handling and cleanup without inference.
func TestOpenCodeNativeHelper(t *testing.T) {
	mode := os.Getenv("GINKGO_NATIVE_TEST_MODE")
	if mode == "" {
		return
	}
	if os.Args[len(os.Args)-1] == "--version" {
		version := "2.0.31"
		if mode == "old-version" {
			version = "1.2.0"
		}
		if mode == "future-version" {
			version = "2.1.0"
		}
		if mode == "prerelease" {
			version = "2.0.31-beta.1"
		}
		if mode == "release25" {
			version = "2.0.25"
		}
		fmt.Println("opencode v" + version)
		os.Exit(0)
	}
	capture := os.Getenv("GINKGO_NATIVE_CAPTURE")
	if mode == "early-exit" {
		os.Exit(1)
	}
	if mode == "startup-hang" {
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	if mode == "startup-output" {
		fmt.Println(strings.Repeat("x", 16384))
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	config, _ := os.ReadFile(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "opencode.json"))
	_ = os.WriteFile(capture+".config", config, 0600)
	env, _ := json.Marshal(os.Environ())
	_ = os.WriteFile(capture+".env", env, 0600)
	cwd, _ := os.Getwd()
	_ = os.WriteFile(capture+".dir", []byte(cwd), 0600)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	var mu sync.Mutex
	var sessionID, inputID string
	var model modelRef
	waited := false
	modelCalls := 0
	reloads := 0
	modelInfo := map[string]any{"id": "friendly-alias", "modelID": "actual-vision", "providerID": "test", "enabled": true, "capabilities": map[string]any{"input": []string{"text", "image"}}, "variants": []map[string]string{{"id": "high"}}}
	if mode == "text-model" {
		modelInfo["capabilities"] = map[string]any{"input": []string{"text"}}
	}
	if mode == "unknown-caps" {
		delete(modelInfo, "capabilities")
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass == "" || pass != os.Getenv("OPENCODE_PASSWORD") {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			body = nil
		}
		var configSnapshot json.RawMessage
		if r.URL.Path == "/api/location/reload" {
			configSnapshot, _ = os.ReadFile(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "opencode.json"))
		}
		item, _ := json.Marshal(nativeCapture{Path: r.URL.Path, Method: r.Method, Body: body, Config: configSnapshot})
		f, _ := os.OpenFile(capture, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		_, _ = f.Write(append(item, '\n'))
		_ = f.Close()
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/api/location/reload":
			reloads++
			if mode == "reload-error" || mode == "reset-error" && reloads == 2 {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
		case r.URL.Path == "/api/agent/"+openCodeSessionAgent:
			content, _ := os.ReadFile(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "opencode.json"))
			var cfg struct {
				Agents map[string]struct {
					System string `json:"system"`
				} `json:"agents"`
			}
			_ = json.Unmarshal(content, &cfg)
			write(map[string]any{"data": map[string]string{"system": cfg.Agents[openCodeSessionAgent].System}})
		case r.URL.Path == "/api/credential":
			if mode == "console" {
				write(map[string]any{"data": []map[string]string{{"integrationID": "opencode"}}})
				return
			}
			write(map[string]any{"data": []any{}})
		case r.URL.Path == "/api/model" || r.URL.Path == "/api/model/default":
			modelCalls++
			if changed, _ := os.ReadFile(capture + ".model-mode"); string(changed) == "text" {
				modelInfo["id"], modelInfo["modelID"] = "friendly-text", "actual-text"
				modelInfo["capabilities"] = map[string]any{"input": []string{"text"}}
			}
			if mode == "transient-model" && modelCalls == 1 {
				w.WriteHeader(503)
				return
			}
			if mode == "bad-model-json" {
				_, _ = w.Write([]byte(`{"data":`))
				return
			}
			if mode == "null-default" {
				write(map[string]any{"data": nil})
				return
			}
			if r.URL.Path == "/api/model" {
				write(map[string]any{"data": []any{modelInfo}})
			} else {
				write(map[string]any{"data": modelInfo})
			}
		case r.Method == "POST" && r.URL.Path == "/api/session":
			var create struct {
				ID          string              `json:"id"`
				Model       modelRef            `json:"model"`
				Agent       string              `json:"agent"`
				Permissions []map[string]string `json:"permissions"`
			}
			_ = json.Unmarshal(body, &create)
			sessionID, model, waited = create.ID, create.Model, false
			if mode == "create-lost" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			if mode == "permissions-missing" {
				create.Permissions = nil
			}
			write(map[string]any{"data": map[string]any{"id": sessionID, "agent": create.Agent, "permissions": create.Permissions}})
		case r.Method == "GET" && r.URL.Path == "/api/session/"+sessionID:
			outcome := "succeeded"
			if mode == "failed-outcome" || strings.HasPrefix(mode, "async-") {
				outcome = "failed"
			}
			write(map[string]any{"data": map[string]string{"id": sessionID, "outcome": outcome}})
		case strings.HasSuffix(r.URL.Path, "/interrupt"):
			write(map[string]bool{"interrupted": true})
		case strings.HasSuffix(r.URL.Path, "/prompt"):
			var prompt struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(body, &prompt)
			inputID = prompt.ID
			if mode == "prompt-crash" {
				os.Exit(7)
			}
			if mode == "prompt-error" {
				w.WriteHeader(429)
				_, _ = w.Write([]byte(`{"message":"secret-note-and-key"}`))
				return
			}
			if mode == "prompt-500" {
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`{"message":"secret-note-and-key","_tag":"untrusted-upstream-tag"}`))
				return
			}
			if mode == "admission-mismatch" {
				inputID = "msg_someone_else"
			}
			write(map[string]any{"data": map[string]string{"id": inputID, "sessionID": sessionID, "type": "user"}})
		case strings.HasSuffix(r.URL.Path, "/wait"):
			if mode == "wait-hang" {
				<-r.Context().Done()
				return
			}
			waited = true
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/message"):
			if !waited {
				w.WriteHeader(409)
				return
			}
			if r.URL.Query().Get("type") != "assistant" || r.URL.Query().Get("order") != "desc" || r.URL.Query().Get("limit") != "1" {
				w.WriteHeader(400)
				return
			}
			if mode == "output-limit" {
				_, _ = w.Write([]byte(strings.Repeat("x", 16384)))
				return
			}
			if mode == "bad-context" {
				_, _ = w.Write([]byte(`{"data":42}`))
				return
			}
			finish := "stop"
			if mode == "truncated" {
				finish = "length"
			}
			agent := openCodeSessionAgent
			if mode == "wrong-agent" {
				agent = "other"
			}
			if mode == "wrong-model" {
				model.ID = "other"
			}
			part := map[string]any{"type": "text", "text": "final answer"}
			if mode == "tool-output" {
				part = map[string]any{"type": "tool", "text": "not allowed"}
			}
			assistant := map[string]any{"type": "assistant", "agent": agent, "model": model, "time": map[string]int{"completed": 1}, "finish": finish, "content": []any{map[string]string{"type": "reasoning", "text": "do not return reasoning"}, part}}
			if strings.HasPrefix(mode, "async-") {
				typ, status := "unknown", 503
				switch mode {
				case "async-auth":
					typ = "provider.auth"
					status = 401
				case "async-rate":
					typ = "provider.rate-limit"
					status = 429
				case "async-quota":
					typ = "provider.quota"
				case "async-no-route":
					typ = "provider.no-route"
				case "async-status-auth":
					status = 403
				case "async-status-rate":
					status = 429
				}
				assistant["error"] = map[string]any{"type": typ, "message": "secret-note-and-key", "status": status, "response": map[string]string{"body": "secret-note-and-key"}}
				assistant["finish"] = "error"
			}
			write(map[string]any{"data": []any{assistant}})
		case r.Method == "DELETE":
			if mode == "delete-error" {
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	})}
	fmt.Printf("server listening on http://%s\n", listener.Addr())
	_ = server.Serve(listener)
	os.Exit(0)
}

func nativeCaptures(t *testing.T, path string) []nativeCapture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captures []nativeCapture
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var item nativeCapture
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatal(err)
		}
		captures = append(captures, item)
	}
	return captures
}

func nativeImage(t *testing.T, c color.RGBA) imagecontext.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, c)
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	im, err := imagecontext.Decode(data.Bytes(), time.Time{}, "test image")
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestOpenCodeNativeMultimodalLifecycle(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	p.options.Model, p.options.Variant = "test/friendly-alias", "high"
	caps, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.ImageInput != Supported || caps.ResolvedModel != "test/actual-vision" || caps.MaxImages != 8 {
		t.Fatalf("caps: %+v", caps)
	}
	images := []imagecontext.Image{nativeImage(t, color.RGBA{R: 255, A: 255}), nativeImage(t, color.RGBA{B: 255, A: 255})}
	// Persisted file attachments become exactly the same validated byte inputs.
	file := filepath.Join(t.TempDir(), "file.png")
	if err := os.WriteFile(file, images[1].Data, 0600); err != nil {
		t.Fatal(err)
	}
	images[1], err = imagecontext.ReadFile(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response, err := p.Complete(context.Background(), Request{System: "SYSTEM ONLY", Prompt: "USER ONLY", Images: images, OutputSchema: json.RawMessage(`{"type":"object"}`)})
		if err != nil || response.Text != "final answer" {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	}
	items := nativeCaptures(t, capture)
	var ids []string
	var sequence []string
	modelCalls := 0
	for _, item := range items {
		if item.Path == "/api/model" {
			modelCalls++
		}
		if item.Path == "/api/session" {
			var v map[string]any
			_ = json.Unmarshal(item.Body, &v)
			ids = append(ids, v["id"].(string))
			sequence = append(sequence, "create")
		}
		if strings.Contains(item.Path, "/instructions/entries/") {
			t.Fatal("request system persisted through instruction entries")
		}
		if item.Path == "/api/location/reload" {
			sequence = append(sequence, "reload")
			if len(sequence) == 1 || sequence[len(sequence)-2] == "reload" {
				if !bytes.Contains(item.Config, []byte("SYSTEM ONLY")) {
					t.Fatal("missing private agent system")
				}
			}
		}
		if strings.HasSuffix(item.Path, "/prompt") {
			sequence = append(sequence, "prompt")
			var v struct {
				Text  string `json:"text"`
				Files []struct {
					URI string `json:"uri"`
				} `json:"files"`
			}
			if json.Unmarshal(item.Body, &v) != nil || v.Text != "USER ONLY" || len(v.Files) != 2 || bytes.Contains(item.Body, []byte("SYSTEM ONLY")) || bytes.Contains(item.Body, []byte("schema")) {
				t.Fatalf("bad prompt: %s", item.Body)
			}
			for i, file := range v.Files {
				_, raw, _ := strings.Cut(file.URI, ",")
				data, err := base64.StdEncoding.DecodeString(raw)
				if err != nil || !bytes.Equal(data, images[i].Data) {
					t.Fatal("image order or bytes changed")
				}
			}
		}
		if strings.HasSuffix(item.Path, "/wait") {
			sequence = append(sequence, "wait")
		}
		if strings.HasSuffix(item.Path, "/message") {
			sequence = append(sequence, "message")
		}
		if item.Method == "DELETE" {
			sequence = append(sequence, "delete")
		}
	}
	if len(ids) != 2 || ids[0] == ids[1] || modelCalls != 3 {
		t.Fatalf("ids=%v modelCalls=%d", ids, modelCalls)
	}
	want := []string{"reload", "create", "prompt", "wait", "message", "delete", "reload", "reload", "create", "prompt", "wait", "message", "delete", "reload"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("sequence=%v", sequence)
	}
	dirBytes, _ := os.ReadFile(capture + ".dir")
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(dirBytes)); !os.IsNotExist(err) {
		t.Fatalf("runtime not cleaned: %v", err)
	}
	_ = p.Close()
	if _, err := p.Complete(context.Background(), Request{Prompt: "closed"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed=%v", err)
	}
}

func TestOpenCodeNativeImagesSubmitted(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want *bool
	}{
		{"ok", nativeBool(true)}, {"old-version", nativeBool(false)}, {"permissions-missing", nativeBool(false)},
		{"create-lost", nativeBool(false)}, {"text-model", nativeBool(false)}, {"prompt-error", nativeBool(false)},
		{"prompt-crash", nil}, {"admission-mismatch", nil}, {"bad-context", nativeBool(true)},
		{"failed-outcome", nativeBool(true)}, {"async-auth", nativeBool(true)}, {"delete-error", nativeBool(true)},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p, _ := nativeFixture(t, tc.mode)
			response, _ := p.Complete(context.Background(), Request{Prompt: "synthetic", Images: []imagecontext.Image{nativeImage(t, color.RGBA{R: 255, A: 255})}})
			if !reflect.DeepEqual(response.ImagesSubmitted, tc.want) {
				t.Fatalf("submission=%v want=%v", response.ImagesSubmitted, tc.want)
			}
		})
	}
}

func nativeBool(value bool) *bool { return &value }

func TestOpenCodeNativePreflightRefreshesDefaultAndSkipsGeneration(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Supported {
		t.Fatalf("initial=%+v %v", caps, err)
	}
	if caps.MaxTotalImageBytes != imagecontext.MaxTotalBytes || caps.MaxRequestBytes <= imagecontext.MaxTotalBytes {
		t.Fatalf("limits=%+v", caps)
	}
	if err := os.WriteFile(capture+".model-mode", []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	req := Request{Prompt: "synthetic", Images: []imagecontext.Image{nativeImage(t, color.RGBA{R: 255, A: 255})}}
	response, err := p.Complete(context.Background(), req)
	if err == nil || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("Complete silently dropped or submitted stale-model images: %+v %v", response, err)
	}
	if err := p.PreflightImages(context.Background(), req); err == nil {
		t.Fatal("preflight trusted stale supported default")
	}
	for _, it := range nativeCaptures(t, capture) {
		if it.Path == "/api/session" || strings.HasSuffix(it.Path, "/prompt") {
			t.Fatal("preflight started generation")
		}
	}
	// Service can now retain the whole batch and continue text generation.
	req.Images = nil
	response, err = p.Complete(context.Background(), req)
	if err != nil || response.Text != "final answer" || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("text fallback=%+v %v", response, err)
	}
}

func TestOpenCodeNativePreflightRequestModelVariantAndEncoding(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	p.options.Model, p.options.Variant = "test/missing", "missing"
	req := Request{Model: "test/friendly-alias#high", Prompt: "synthetic", Images: []imagecontext.Image{nativeImage(t, color.RGBA{B: 255, A: 255})}}
	if err := p.PreflightImages(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Variant = "missing"
	if err := p.PreflightImages(context.Background(), req); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("variant=%v", err)
	}
	for _, it := range nativeCaptures(t, capture) {
		if strings.HasSuffix(it.Path, "/prompt") {
			t.Fatal("preflight submitted content")
		}
	}
	p.options.MaxInputBytes = 200
	req.Variant = "high"
	req.Prompt = strings.Repeat("\x00", 40)
	if err := p.PreflightImages(context.Background(), req); err != nil {
		t.Fatal("image framing reserve should accommodate escaped text", err)
	}
	// The complete exact wire reserve can still be exhausted by JSON expansion.
	if _, err := nativePrompt(Request{Prompt: strings.Repeat("\x00", 100000)}, []imagecontext.Image{{Data: make([]byte, imagecontext.MaxTotalBytes)}}, "msg_ginkgo_"+strings.Repeat("0", 48), 100000); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("exact wire expansion=%v", err)
	}
}

func TestOpenCodeNativeSchemaConstraints(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	response, err := p.Complete(context.Background(), Request{System: "SYSTEM", Prompt: "USER", OutputSchema: json.RawMessage(`{"type":"object","required":["answer"]}`)})
	if err != nil || response.Text != "final answer" {
		t.Fatalf("schema=%+v %v", response, err)
	}
	found := false
	for _, it := range nativeCaptures(t, capture) {
		if strings.Contains(it.Path, "/instructions/entries/") {
			t.Fatal("schema persisted through instruction entries")
		}
		if it.Path == "/api/location/reload" && bytes.Contains(it.Config, []byte("JSON Schema")) {
			found = true
			if !bytes.Contains(it.Config, []byte("answer")) {
				t.Fatal("missing private agent schema constraint")
			}
		}
		if strings.HasSuffix(it.Path, "/prompt") && bytes.Contains(it.Body, []byte("JSON Schema")) {
			t.Fatal("schema put in user prompt")
		}
	}
	if !found {
		t.Fatal("schema instruction absent")
	}
	config, _ := os.ReadFile(filepath.Join(p.runtime.dir, "config", "opencode.json"))
	if bytes.Contains(config, []byte("JSON Schema")) || p.runtime.agentSystem != openCodeNativeBaseSystem {
		t.Fatal("private schema instructions were not reset")
	}
	for _, schema := range []json.RawMessage{json.RawMessage(`{"type":`), json.RawMessage(`[]`), json.RawMessage(`null`), json.RawMessage(`1`), json.RawMessage(`{"type":42}`), json.RawMessage(`{"properties":[]}`), json.RawMessage(`{"required":"answer"}`), json.RawMessage(`{"items":{"type":"not-a-type"}}`), json.RawMessage(strings.Repeat(" ", 64<<10) + `{}`)} {
		response, err := p.Complete(context.Background(), Request{Prompt: "synthetic", OutputSchema: schema})
		if err == nil || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
			t.Fatalf("schema rejection=%+v %v", response, err)
		}
	}
}

func TestOpenCodeNativePrivateConfigReloadFailure(t *testing.T) {
	for _, mode := range []string{"reload-error", "reset-error"} {
		t.Run(mode, func(t *testing.T) {
			p, capture := nativeFixture(t, mode)
			response, err := p.Complete(context.Background(), Request{System: "private request system", Prompt: "synthetic", Images: []imagecontext.Image{nativeImage(t, color.RGBA{R: 255, A: 255})}})
			if err == nil {
				t.Fatal("failed reload was silently ignored")
			}
			want := mode == "reset-error"
			if response.ImagesSubmitted == nil || *response.ImagesSubmitted != want {
				t.Fatalf("reload submission=%v want=%v", response.ImagesSubmitted, want)
			}
			if p.runtime != nil {
				t.Fatal("runtime retained unverified private instructions")
			}
			if mode == "reload-error" {
				for _, it := range nativeCaptures(t, capture) {
					if it.Path == "/api/session" {
						t.Fatal("created session before config verification")
					}
				}
			}
		})
	}
}

// Opt-in real normalization regression: resume:false materializes all images but
// cannot run a model. This covers an eight-image 2400x2400 PNG batch,
// previously expanded by default normalization beyond boundImages' 25 MiB
// pruning trigger. Original byte equality proves no resize/re-encode occurred.
func TestOpenCodeNativeRealImageBytesNoInference(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	im := image.NewPaletted(image.Rect(0, 0, 2400, 2400), color.Palette{color.RGBA{0, 0, 0, 255}, color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}})
	x := uint32(12345)
	for i := range im.Pix {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		im.Pix[i] = byte(x & 3)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, im); err != nil {
		t.Fatal(err)
	}
	var images []imagecontext.Image
	for i := 0; i < 8; i++ {
		images = append(images, imagecontext.Image{Data: encoded.Bytes(), Label: fmt.Sprint("synthetic image ", i)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	images, err := imagecontext.Validate(ctx, images)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: executable, TempDir: dir, EphemeralAuth: true, Options: Options{Timeout: 90 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := newOpenCodeSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.call(ctx, "POST", "/api/session", map[string]any{"id": id, "title": "synthetic attachment only", "agent": openCodeSessionAgent, "model": modelRef{ProviderID: "ginkgo-invalid-provider", ID: "ginkgo-invalid-model"}, "location": map[string]string{"directory": p.runtime.dir}, "permissions": denyAllOpenCodePermissions()}, 200, nil); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = deleteOpenCodeSession(p.runtime.server.endpoint, p.runtime.password, id) }()
	prompt, err := nativePrompt(Request{Prompt: "synthetic attachment materialization only"}, images, "msg_ginkgo_"+strings.Repeat("0", 48), p.options.MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(prompt, &payload)
	payload["resume"] = false
	var admitted struct {
		Data struct {
			Payload struct {
				Files []struct {
					Data string `json:"data"`
					Mime string `json:"mime"`
				} `json:"files"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := p.call(ctx, "POST", "/api/session/"+id+"/prompt", payload, 200, &admitted); err != nil {
		t.Fatal(err)
	}
	if len(admitted.Data.Payload.Files) != len(images) {
		t.Fatalf("image batch pruned: %d/%d", len(admitted.Data.Payload.Files), len(images))
	}
	total := 0
	for i, file := range admitted.Data.Payload.Files {
		data, err := base64.StdEncoding.DecodeString(file.Data)
		if err != nil || file.Mime != "image/png" || !bytes.Equal(data, images[i].Data) {
			t.Fatalf("image %d altered by native normalization", i)
		}
		total += len(file.Data)
	}
	if total > 25<<20 {
		t.Fatalf("batch crosses boundImages pruning trigger: %d", total)
	}
	t.Logf("all %d images preserved byte-for-byte: raw=%d base64=%d; no runner resumed", len(images), encoded.Len()*len(images), total)
}

// Real saved-profile regression: the impossible model fails before inference,
// but the runner does prepare durable instruction sources. Request-specific
// agent.system MUST NOT be among those sources or survive in any live DB row.
// Read-only inspection is confined to the disposable synthetic test database.
func TestOpenCodeNativeRealPrivateSystemNoRetention(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	seedOpenCodeSmokeCredential(t, executable, dir, "openai")
	t.Setenv("GINKGO_NATIVE_LITERAL_SECRET", "synthetic-env-value-must-not-expand")
	p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: executable, TempDir: dir, Options: Options{Timeout: 45 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := p.start(ctx); err != nil {
		t.Fatal(err)
	}
	const marker = "GINKGO_NATIVE_SYNTHETIC_PRIVATE_SYSTEM_MARKER"
	literalFile := filepath.Join(dir, "literal-file-token")
	if err := os.WriteFile(literalFile, []byte("synthetic-file-value-must-not-expand"), 0600); err != nil {
		t.Fatal(err)
	}
	privateInstructions := marker + "\nLiteral tokens: {env:GINKGO_NATIVE_LITERAL_SECRET} {file:" + literalFile + "}"
	system, err := nativeInstructions(Request{System: privateInstructions, OutputSchema: json.RawMessage(`{"type":"object","properties":{"synthetic_schema_marker":{"const":"GINKGO_NATIVE_SYNTHETIC_SCHEMA_MARKER"}}}`)}, p.options.MaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.setSystem(ctx, system); err != nil {
		t.Fatal(err)
	}
	id, err := newOpenCodeSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.call(ctx, "POST", "/api/session", map[string]any{"id": id, "title": "synthetic no-inference retention check", "agent": openCodeSessionAgent, "model": modelRef{ProviderID: "ginkgo-invalid-provider", ID: "ginkgo-invalid-model"}, "location": map[string]string{"directory": p.runtime.dir}, "permissions": denyAllOpenCodePermissions()}, 200, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.call(ctx, "POST", "/api/session/"+id+"/prompt", map[string]any{"text": "synthetic user text only"}, 200, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.call(ctx, "POST", "/api/experimental/session/"+id+"/wait", nil, 204, nil); err != nil {
		t.Fatal(err)
	}
	var settled struct {
		Data struct {
			Outcome string `json:"outcome"`
		} `json:"data"`
	}
	if err := p.call(ctx, "GET", "/api/session/"+id, nil, 200, &settled); err != nil || settled.Data.Outcome != "failed" {
		t.Fatalf("impossible model outcome=%q err=%v", settled.Data.Outcome, err)
	}
	var entries struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := p.call(ctx, "GET", "/api/experimental/session/"+id+"/instructions/entries", nil, 200, &entries); err != nil || len(entries.Data) != 0 {
		t.Fatalf("request-specific durable entries created: %v", err)
	}
	var transcript json.RawMessage
	if err := p.call(ctx, "GET", "/api/session/"+id+"/context", nil, 200, &transcript); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(transcript, []byte(marker)) || bytes.Contains(transcript, []byte("GINKGO_NATIVE_SYNTHETIC_SCHEMA_MARKER")) {
		t.Fatal("private system/schema concatenated into durable transcript")
	}
	if err := deleteOpenCodeSession(p.runtime.server.endpoint, p.runtime.password, id); err != nil {
		t.Fatal(err)
	}
	if err := p.setSystem(ctx, ""); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(filepath.Join(p.runtime.dir, "config", "opencode.json"))
	if bytes.Contains(config, []byte(marker)) || bytes.Contains(config, []byte("GINKGO_NATIVE_SYNTHETIC_SCHEMA_MARKER")) {
		t.Fatal("private request config was not reset")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-c", `import sqlite3,sys,json
from pathlib import Path
c=sqlite3.connect(Path(sys.argv[1]).resolve().as_uri()+"?mode=ro",uri=True)
dump="\n".join(c.iterdump())
markers=sys.argv[3:]
retained=sum(marker in dump or marker.encode().hex().lower() in dump.lower() for marker in markers)
print(json.dumps({"sessions":c.execute("select count(*) from session_v2 where id=?",(sys.argv[2],)).fetchone()[0],"retained":retained}))`, os.Getenv("OPENCODE_DB"), id, marker, "GINKGO_NATIVE_SYNTHETIC_SCHEMA_MARKER")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic read-only DB inspection failed: %v", err)
	}
	var result struct{ Sessions, Retained int }
	if json.Unmarshal(output, &result) != nil || result.Sessions != 0 || result.Retained != 0 {
		t.Fatalf("request-specific system/schema retained in saved-profile DB: %s", output)
	}
	t.Log("verified private agent.system activation/reset, separate user transcript, zero durable API instruction entries and zero retained system/schema DB markers; no inference")
}

func TestOpenCodeNativeAsyncFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{{"async-auth", ErrAuthentication}, {"async-rate", ErrRateLimited}, {"async-quota", ErrRateLimited}, {"async-no-route", ErrModelUnavailable}, {"async-status-auth", ErrAuthentication}, {"async-status-rate", ErrRateLimited}, {"async-unknown", ErrProviderUnavailable}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, _ := nativeFixture(t, tc.mode)
			_, err := p.Complete(context.Background(), Request{Prompt: "synthetic"})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("async=%v want=%v", err, tc.want)
			}
		})
	}
}

func TestOpenCodeNativeIsolation(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"plugins":["evil"]}`)
	t.Setenv("OPENCODE_SIMULATE", "true")
	t.Setenv("OPENCODE_DB", "saved-db-sentinel")
	_, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(capture + ".config")
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("bad config")
	}
	plugins, _ := json.Marshal(config["plugins"])
	if string(plugins) != `["-*","opencode.config.agent","opencode.config.image","opencode.models.dev","opencode.provider.*"]` {
		t.Fatalf("plugins=%s", plugins)
	}
	policies, _ := json.Marshal(config["policies"])
	for _, resource := range []string{`"resource":"*"`, `plugin:*`, `mcp:*`, `skill:*`} {
		if !bytes.Contains(policies, []byte(resource)) {
			t.Fatalf("missing %s", resource)
		}
	}
	data, _ = os.ReadFile(capture + ".env")
	var env []string
	_ = json.Unmarshal(data, &env)
	for _, v := range env {
		if strings.HasPrefix(v, "OPENCODE_CONFIG_CONTENT=") || strings.HasPrefix(v, "OPENCODE_SIMULATE=") {
			t.Fatal("inherited unsafe override")
		}
	}
	if !strings.Contains(string(data), "saved-db-sentinel") {
		t.Fatal("saved auth db not retained")
	}
	items := nativeCaptures(t, capture)
	if items[0].Path != "/api/credential" {
		t.Fatal("location activated before credential check")
	}
}

func TestOpenCodeNativeFailures(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{
		{"bad-model-json", ErrProtocol}, {"null-default", ErrModelUnavailable}, {"permissions-missing", ErrProtocol},
		{"admission-mismatch", ErrProtocol}, {"bad-context", ErrProtocol}, {"wrong-agent", ErrProtocol}, {"tool-output", ErrProtocol}, {"truncated", ErrProtocol},
		{"output-limit", ErrOutputLimit}, {"prompt-error", ErrRateLimited}, {"console", ErrOpenCodeConsole}, {"startup-output", ErrOutputLimit},
		{"wrong-model", ErrProtocol}, {"failed-outcome", ErrProviderUnavailable},
		{"create-lost", ErrProviderUnavailable}, {"prompt-crash", ErrProviderUnavailable}, {"early-exit", nil}, {"old-version", nil}, {"future-version", nil}, {"prerelease", nil}, {"delete-error", nil},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p, capture := nativeFixture(t, tc.mode)
			_, err := p.Complete(context.Background(), Request{Prompt: "synthetic only"})
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-note") {
				t.Fatal("leaked error body")
			}
			if tc.mode == "create-lost" || tc.mode == "admission-mismatch" {
				items := nativeCaptures(t, capture)
				deleted := false
				for _, it := range items {
					if it.Method == "DELETE" {
						deleted = true
					}
				}
				if !deleted {
					t.Fatal("ambiguous session not cleaned")
				}
			}
		})
	}
}

func TestOpenCodeNativeAuditedRelease25(t *testing.T) {
	p, _ := nativeFixture(t, "release25")
	response, err := p.Complete(context.Background(), Request{Prompt: "synthetic"})
	if err != nil || response.Text != "final answer" {
		t.Fatalf("release25=%+v %v", response, err)
	}
}

func TestOpenCodeNativeStartupCancellation(t *testing.T) {
	p, _ := nativeFixture(t, "startup-hang")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Capabilities(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation=%v", err)
	}
	if p.runtime != nil {
		t.Fatal("cancelled startup retained runtime")
	}
}

func TestOpenCodeNativeCrashNeverReplays(t *testing.T) {
	p, capture := nativeFixture(t, "prompt-crash")
	_, err := p.Complete(context.Background(), Request{Prompt: "synthetic"})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("crash=%v", err)
	}
	prompts := 0
	for _, it := range nativeCaptures(t, capture) {
		if strings.HasSuffix(it.Path, "/prompt") {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("ambiguous prompt replayed: %d", prompts)
	}
	if len(p.pendingCleanup) != 1 {
		t.Fatal("lost cleanup ownership after crash")
	}
	t.Setenv("GINKGO_NATIVE_TEST_MODE", "ok")
	response, err := p.Complete(context.Background(), Request{Prompt: "new explicit request"})
	if err != nil || response.Text != "final answer" {
		t.Fatalf("restart=%+v %v", response, err)
	}
	if len(p.pendingCleanup) != 0 {
		t.Fatal("restart did not recover owned session cleanup")
	}
}

func TestOpenCodeNativeCloseReportsUnconfirmedCleanup(t *testing.T) {
	p, _ := nativeFixture(t, "prompt-crash")
	_, _ = p.Complete(context.Background(), Request{Prompt: "synthetic"})
	if err := p.Close(); err == nil {
		t.Fatal("Close silently claimed cleanup after unreachable saved-auth runtime")
	}
	if err := p.Close(); err == nil {
		t.Fatal("idempotent Close lost cleanup error")
	}
}

func TestOpenCodeNativeFinalAssistantValidation(t *testing.T) {
	ref := modelRef{ID: "alias", ProviderID: "test"}
	valid := `{"id":"msg_final","type":"assistant","agent":"ginkgo-generation-only","model":{"id":"alias","providerID":"test"},"finish":"stop","time":{"completed":1},"content":[{"type":"text","text":"answer"}]}`
	for _, tc := range []struct{ name, body string }{
		{"empty", "[]"},
		{"multiple", "[" + valid + "," + valid + "]"},
		{"user", "[" + strings.Replace(valid, `"type":"assistant"`, `"type":"user"`, 1) + "]"},
		{"incomplete", "[" + strings.Replace(valid, `"completed":1`, `"created":1`, 1) + "]"},
		{"missing-text", "[" + strings.Replace(valid, `"text":"answer"`, `"ignored":"answer"`, 1) + "]"},
		{"error", "[" + strings.Replace(valid, `"finish":"stop"`, `"error":{"message":"secret"},"finish":"stop"`, 1) + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messages []openCodeNativeMessage
			if err := json.Unmarshal([]byte(tc.body), &messages); err != nil {
				t.Fatal(err)
			}
			if _, err := nativeFinalResponse(messages, ref); !errors.Is(err, ErrProtocol) {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func TestOpenCodeNativeCancellationAndClose(t *testing.T) {
	for _, closeProvider := range []bool{false, true} {
		t.Run(fmt.Sprint(closeProvider), func(t *testing.T) {
			p, capture := nativeFixture(t, "wait-hang")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			responses := make(chan Response, 1)
			images := []imagecontext.Image{nativeImage(t, color.RGBA{R: 255, A: 255})}
			go func() {
				response, err := p.Complete(ctx, Request{Prompt: "synthetic", Images: images})
				responses <- response
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				data, _ := os.ReadFile(capture)
				if bytes.Contains(data, []byte("/wait")) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("never reached wait")
				}
				time.Sleep(5 * time.Millisecond)
			}
			start := time.Now()
			if closeProvider {
				_ = p.Close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel hung")
			}
			if time.Since(start) > 4*time.Second {
				t.Fatal("cleanup exceeded bound")
			}
			response := <-responses
			if response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
				t.Fatal("cancellation lost confirmed image admission")
			}
			items := nativeCaptures(t, capture)
			interrupted, deleted := false, false
			for _, it := range items {
				if strings.HasSuffix(it.Path, "/interrupt") {
					interrupted = true
				}
				if it.Method == "DELETE" {
					deleted = true
				}
			}
			if !interrupted || !deleted {
				t.Fatalf("cleanup interrupt=%v delete=%v", interrupted, deleted)
			}
		})
	}
}

func TestOpenCodeNativeCapabilityCacheAndOverrides(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	for i := 0; i < 2; i++ {
		if _, err := p.Capabilities(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		n := 0
		for _, it := range nativeCaptures(t, capture) {
			if it.Path == "/api/model/default" {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatal("success not cached")
	}
	<-p.gate
	for key, value := range p.cache {
		value.until = time.Now().Add(-time.Second)
		p.cache[key] = value
	}
	p.gate <- struct{}{}
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatal("TTL not refreshed")
	}
	_, err := p.Complete(context.Background(), Request{Prompt: "test", Model: "test/friendly-alias", Variant: "high"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), Request{Prompt: "test", Model: "test/missing"})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("missing model=%v", err)
	}
}

func TestOpenCodeNativeCapabilitiesUnknownAndUnsupported(t *testing.T) {
	for _, tc := range []struct {
		mode, override string
		want           Support
	}{{"text-model", "supported", Unsupported}, {"unknown-caps", "auto", Unknown}, {"unknown-caps", "supported", Supported}, {"ok", "unsupported", Unsupported}} {
		t.Run(tc.mode+tc.override, func(t *testing.T) {
			p, _ := nativeFixture(t, tc.mode)
			p.options.ImageInput = tc.override
			caps, err := p.Capabilities(context.Background())
			if err != nil || caps.ImageInput != tc.want {
				t.Fatalf("caps=%+v err=%v", caps, err)
			}
		})
	}
}

func TestOpenCodeNativeNoPermanentTransientFailure(t *testing.T) {
	p, capture := nativeFixture(t, "transient-model")
	if _, err := p.Capabilities(context.Background()); err == nil {
		t.Fatal("expected transient failure")
	}
	// A restarted fake's first call fails again; switch the next process to a
	// recovered server. No error is permanently cached by the provider.
	t.Setenv("GINKGO_NATIVE_TEST_MODE", "ok")
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Supported {
		t.Fatalf("recovery=%+v %v", caps, err)
	}
	if len(nativeCaptures(t, capture)) < 4 {
		t.Fatal("discovery not retried")
	}
}

func TestOpenCodeNativeEphemeralAndValidation(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	p.options.EphemeralAuth = true
	t.Setenv("OPENCODE_DB", "must-not-inherit")
	_, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(capture + ".env")
	if bytes.Contains(env, []byte("must-not-inherit")) {
		t.Fatal("ephemeral mode inherited db")
	}
	items := nativeCaptures(t, capture)
	if items[0].Path == "/api/credential" {
		t.Fatal("ephemeral requested saved auth")
	}
	_, err = p.Complete(context.Background(), Request{Prompt: "test", Images: []imagecontext.Image{{Data: []byte("bad image")}}})
	if err == nil {
		t.Fatal("invalid image accepted")
	}
	p.options.MaxInputBytes = 8
	_, err = p.Complete(context.Background(), Request{Prompt: strings.Repeat("a", 9)})
	if !errors.Is(err, ErrInputLimit) {
		t.Fatalf("input=%v", err)
	}
}

// This opt-in smoke authenticates ONLY to our private local server, reusing
// synthetic saved credentials in a disposable profile. The nonexistent model
// is rejected by metadata discovery BEFORE session.prompt: never paid inference.
func TestOpenCodeNativeRealAuthenticatedNoInference(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	seedOpenCodeSmokeCredential(t, executable, dir, "openai")
	p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: executable, TempDir: dir, Model: "ginkgo-invalid-provider/ginkgo-invalid-model", Options: Options{Timeout: 45 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, err = p.Complete(context.Background(), Request{Prompt: "Synthetic no-inference protocol smoke"})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("no-inference metadata rejection=%v", err)
	}
}
