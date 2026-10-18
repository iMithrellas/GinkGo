//go:build linux

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

func codexTestProfile(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	return home
}
func codexFakeProvider(t *testing.T, mode string, options Options) (*CodexProvider, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "protocol.jsonl")
	wrapper := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nexport GINKGO_CODEX_HELPER=1\nexport GINKGO_CODEX_MODE=" + strconv.Quote(mode) + "\nexport GINKGO_CODEX_LOG=" + strconv.Quote(log) + "\nexport GORACE=atexit_sleep_ms=0\nexec " + strconv.Quote(exe) + " -test.run=^TestCodexProtocolHelper$ -- \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := NewCodex(CodexOptions{Options: options, Executable: wrapper, AuthHome: codexTestProfile(t), TempDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, log
}
func codexReadLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var values []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var value map[string]any
		if json.Unmarshal(line, &value) != nil {
			t.Fatalf("invalid fake log %q", line)
		}
		values = append(values, value)
	}
	return values
}
func codexTestImages(t *testing.T) []imagecontext.Image {
	t.Helper()
	var result []imagecontext.Image
	for i := 0; i < 2; i++ {
		var data bytes.Buffer
		im := image.NewRGBA(image.Rect(0, 0, 2+i, 2))
		var err error
		if i == 0 {
			err = png.Encode(&data, im)
		} else {
			err = jpeg.Encode(&data, im, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		validated, err := imagecontext.Decode(data.Bytes(), time.Time{}, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		validated.MediaType = "image/forged"
		result = append(result, validated)
	}
	return result
}

func codexLargeImages(t *testing.T) []imagecontext.Image {
	t.Helper()
	var images []imagecontext.Image
	for _, width := range []int{768, 1447} {
		im := image.NewNRGBA(image.Rect(0, 0, width, width))
		seed := uint32(123456789)
		for i := range im.Pix {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			im.Pix[i] = byte(seed)
		}
		var data bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.NoCompression}
		if err := encoder.Encode(&data, im); err != nil {
			t.Fatal(err)
		}
		validated, err := imagecontext.Decode(data.Bytes(), time.Time{}, "large")
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, validated)
	}
	if len(images[0].Data) < 2<<20 || len(images[1].Data) < 7<<20 {
		t.Fatal("large-image fixture no longer exercises protocol framing")
	}
	return images
}

// TestCodexProtocolHelper is the subprocess fake. It implements the actual
// JSONL contract, including final-before-ack ordering and immediate unloading.
func TestCodexProtocolHelper(t *testing.T) {
	if os.Getenv("GINKGO_CODEX_HELPER") != "1" {
		return
	}
	mode := os.Getenv("GINKGO_CODEX_MODE")
	if mode == "metadata-once" {
		if _, err := os.Stat(os.Getenv("GINKGO_CODEX_LOG") + ".failed"); err == nil {
			mode = "normal"
		}
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	log, _ := os.OpenFile(os.Getenv("GINKGO_CODEX_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	record := func(value any) { data, _ := json.Marshal(value); _, _ = log.Write(append(data, '\n')) }
	if len(args) == 1 && args[0] == "--version" {
		record(map[string]any{"probe": "version"})
		if mode == "version-hang" {
			time.Sleep(time.Hour)
		}
		if mode == "bad-version" {
			fmt.Println("codex-cli 0.164.0")
		} else {
			fmt.Println("codex-cli 0.163.0")
		}
		os.Exit(0)
	}
	if len(args) > 1 && args[0] == "login" {
		record(map[string]any{"probe": "auth"})
		if strings.HasPrefix(mode, "oauth") {
			fmt.Fprintln(os.Stderr, "Logged in using ChatGPT")
		} else if mode == "unknown-auth" {
			fmt.Fprintln(os.Stderr, "Logged in using unsupported auth")
		} else {
			fmt.Fprintln(os.Stderr, "Logged in using an API key - ***")
		}
		os.Exit(0)
	}
	expected, err := codexExpectedConfig(args)
	if err != nil {
		os.Exit(90)
	}
	record(map[string]any{"launch": expected, "home": os.Getenv("HOME"), "profile": os.Getenv("CODEX_HOME"), "hostile": os.Getenv("CODEX_INTERNAL_TEST_CONFIG")})
	write := func(value any) { data, _ := json.Marshal(value); fmt.Println(string(data)) }
	respond := func(id json.RawMessage, value any) { write(map[string]any{"id": id, "result": value}) }
	notify := func(method string, params any) { write(map[string]any{"method": method, "params": params}) }
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	thread := 0
	activeThread := ""
	activeTurn := ""
	initialized := false
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(91)
		}
		record(map[string]any{"method": request.Method, "params": request.Params})
		if request.Method != "initialize" && !initialized {
			os.Exit(92)
		}
		switch request.Method {
		case "initialize":
			initialized = true
			respond(request.ID, map[string]any{"userAgent": "codex_cli_rs/0.163.0", "codexHome": os.Getenv("CODEX_HOME"), "platformOs": "linux"})
		case "initialized":
		case "account/read":
			var account any = map[string]any{"type": "apiKey"}
			if strings.HasPrefix(mode, "oauth") {
				account = map[string]any{"type": "chatgpt", "email": "synthetic@example.invalid", "planType": strings.TrimPrefix(mode, "oauth-")}
			}
			if mode == "signed-out" {
				account = nil
			}
			requiresAuth := mode != "oauth-contradictory-auth" && mode != "contradictory-api-auth"
			respond(request.ID, map[string]any{"account": account, "requiresOpenaiAuth": requiresAuth})
		case "configRequirements/read":
			var requirements any
			if mode == "oauth-compatible-policy" {
				requirements = map[string]any{"allowedApprovalPolicies": []string{"never"}, "allowedSandboxModes": []string{"read-only"}, "allowedWebSearchModes": []string{"disabled"}, "allowedLoginMethods": []string{"chatgpt"}, "featureRequirements": map[string]any{"shell_tool": false, "plugins": false}}
			}
			if mode == "managed" || mode == "oauth-managed" {
				requirements = map[string]any{"features": true}
			}
			respond(request.ID, map[string]any{"requirements": requirements})
		case "config/read":
			var config map[string]any
			raw, _ := json.Marshal(expected)
			_ = json.Unmarshal(raw, &config)
			if mode == "tampered" {
				config["web_search"] = "live"
			}
			if mode == "endpoint-tamper" {
				config["model_providers"] = map[string]any{"openai": map[string]any{"base_url": "http://untrusted.invalid"}}
			}
			layers := []any{map[string]any{"name": map[string]any{"type": "sessionFlags"}, "config": expected}}
			if mode == "oauth-compatible-policy" || mode == "oauth-incompatible-policy" {
				managed := map[string]any{"web_search": "disabled"}
				if mode == "oauth-incompatible-policy" {
					managed["web_search"] = "live"
				}
				layers = append(layers, map[string]any{"name": map[string]any{"type": "enterpriseManaged"}, "config": managed})
			}
			respond(request.ID, map[string]any{"config": config, "layers": layers})
		case "model/list":
			if mode == "metadata-once" {
				_ = os.WriteFile(os.Getenv("GINKGO_CODEX_LOG")+".failed", []byte("failed"), 0600)
				os.Exit(17)
			}
			if mode == "metadata-crash" {
				os.Exit(17)
			}
			model := map[string]any{"id": "native-id", "model": "native-model", "displayName": "Native", "isDefault": true, "inputModalities": []string{"text", "image"}, "defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "medium", "description": "normal"}, map[string]any{"reasoningEffort": "high", "description": "high"}}}
			if mode == "text-only" {
				model["inputModalities"] = []string{"text"}
			}
			if mode == "unknown-modalities" {
				delete(model, "inputModalities")
			}
			if request.Params["cursor"] == nil {
				respond(request.ID, map[string]any{"data": []any{}, "nextCursor": "page2"})
			} else {
				if mode == "cursor-loop" {
					respond(request.ID, map[string]any{"data": []any{model}, "nextCursor": "page2"})
				} else {
					respond(request.ID, map[string]any{"data": []any{model}, "nextCursor": nil})
				}
			}
		case "thread/start":
			thread++
			activeThread = fmt.Sprintf("thread-%d", thread)
			ephemeral := true
			instructions := []string{}
			if mode == "persistent" {
				ephemeral = false
			}
			if mode == "instructions" {
				instructions = []string{"forbidden"}
			}
			cwd, _ := os.Getwd()
			respond(request.ID, map[string]any{"thread": map[string]any{"id": activeThread, "ephemeral": ephemeral}, "model": "native-model", "modelProvider": "openai", "cwd": cwd, "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "instructionSources": instructions})
		case "turn/start":
			if mode == "oauth-native-refresh" {
				// Emulate a native-owned refresh write. Deliberately opaque and not
				// valid JSON: the adapter must not inspect or copy this file.
				_ = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), []byte("native-owned-refreshed-secret"), 0600)
				notify("account/updated", map[string]any{"authMode": "chatgpt", "planType": "plus"})
			}
			if mode == "oauth-refresh-request" {
				write(map[string]any{"id": 900, "method": "account/chatgptAuthTokens/refresh", "params": map[string]any{}})
				continue
			}
			if inputs, ok := request.Params["input"].([]any); ok {
				for _, value := range inputs {
					item, _ := value.(map[string]any)
					if item["type"] == "localImage" {
						path, _ := item["path"].(string)
						info, err := os.Stat(path)
						if err != nil {
							os.Exit(94)
						}
						record(map[string]any{"localImagePath": path, "localImageBytes": info.Size()})
					}
				}
			}
			activeTurn = fmt.Sprintf("turn-%d", thread)
			if mode == "crash" {
				os.Exit(18)
			}
			if mode == "rpc-auth" {
				write(map[string]any{"id": request.ID, "error": map[string]any{"code": 401, "message": "unauthorized secret must not escape"}})
				continue
			}
			if mode == "malformed" {
				fmt.Println("not JSON")
				continue
			}
			notify("turn/started", map[string]any{"threadId": activeThread, "turn": map[string]any{"id": activeTurn}})
			if mode == "hang" {
				respond(request.ID, map[string]any{"turn": map[string]any{"id": activeTurn}})
				continue
			}
			if mode == "tool-request" {
				write(map[string]any{"id": 999, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
				continue
			}
			if mode == "tool-item" {
				notify("item/started", map[string]any{"threadId": activeThread, "turnId": activeTurn, "item": map[string]any{"type": "commandExecution", "id": "tool"}})
				continue
			}
			notify("item/agentMessage/delta", map[string]any{"threadId": activeThread, "turnId": activeTurn, "delta": "PARTIAL SECRET"})
			for _, item := range []map[string]any{{"type": "reasoning", "id": "reason", "text": "REASONING SECRET"}, {"type": "agentMessage", "id": "comment", "phase": "commentary", "text": "PROGRESS SECRET"}, {"type": "agentMessage", "id": "final", "phase": "final_answer", "text": "{\"answer\":\"OK\"}"}} {
				if mode == "output-limit" && item["id"] == "final" {
					item["text"] = strings.Repeat("x", 2<<20)
				}
				notify("item/completed", map[string]any{"threadId": activeThread, "turnId": activeTurn, "item": item})
			}
			status := "completed"
			if mode == "failed" {
				status = "failed"
			}
			notify("turn/completed", map[string]any{"threadId": activeThread, "turn": map[string]any{"id": activeTurn, "status": status, "items": []any{}}})
			respond(request.ID, map[string]any{"turn": map[string]any{"id": activeTurn}})
		case "turn/interrupt":
			respond(request.ID, map[string]any{})
			notify("turn/completed", map[string]any{"threadId": activeThread, "turn": map[string]any{"id": activeTurn, "status": "interrupted"}})
		case "thread/unsubscribe":
			notify("thread/closed", map[string]any{"threadId": activeThread})
			respond(request.ID, map[string]any{"status": "unsubscribed"})
		default:
			os.Exit(93)
		}
	}
	os.Exit(0)
}

func TestCodexLazyConstructorAndValidation(t *testing.T) {
	p, err := NewCodex(CodexOptions{Executable: "/missing/executable", AuthHome: "/missing/profile"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.root != "" || p.executable != "" || p.runtime != nil {
		t.Fatal("constructor performed startup")
	}
	for _, o := range []Options{{Timeout: -1}, {MaxInputBytes: -1}, {MaxOutputBytes: -1}, {ImageInput: "yes"}} {
		if _, err = NewCodex(CodexOptions{Options: o}); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err = p.Capabilities(context.Background()); !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("missing private profile: %v", err)
	}
}

func TestCodexUninitializedProviderHasNoTransport(t *testing.T) {
	for _, p := range []*CodexProvider{nil, {}} {
		caps, err := p.Capabilities(context.Background())
		if caps.ImageInput != Unsupported || !errors.Is(err, ErrIsolationUnavailable) {
			t.Fatalf("uninitialized transport advertised support: %+v %v", caps, err)
		}
		response, err := p.Complete(context.Background(), Request{Prompt: "private"})
		if response.Text != "" || !errors.Is(err, ErrIsolationUnavailable) {
			t.Fatalf("uninitialized provider generated: %+v %v", response, err)
		}
		if err = p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexFullMultimodalProtocol(t *testing.T) {
	for _, mode := range []string{"normal", "oauth-plus"} {
		t.Run(mode, func(t *testing.T) { codexFullMultimodalProtocol(t, mode) })
	}
}

func codexFullMultimodalProtocol(t *testing.T, mode string) {
	p, log := codexFakeProvider(t, mode, Options{})
	t.Setenv("CODEX_INTERNAL_TEST_CONFIG", "must-not-be-inherited")
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Supported || caps.ResolvedModel != "native-model" {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}
	request := Request{System: "SYSTEM SEPARATE", Prompt: "USER SEPARATE", Model: "native-id", Variant: "high", OutputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`), Images: codexTestImages(t)}
	if err = p.PreflightImages(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response, err := p.Complete(context.Background(), request)
		if err != nil || response.Text != `{"answer":"OK"}` || response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
			t.Fatalf("response=%+v err=%v", response, err)
		}
	}
	entries := codexReadLog(t, log)
	starts := 0
	metadata := 0
	turns := 0
	threads := 0
	for _, entry := range entries {
		if launch, ok := entry["launch"].(map[string]any); ok {
			starts++
			if entry["hostile"] != "" {
				t.Fatal("ambient config env inherited")
			}
			if entry["home"] == entry["profile"] {
				t.Fatal("profile used as HOME")
			}
			if launch["web_search"] != "disabled" {
				t.Fatal("web search enabled")
			}
		}
		params, _ := entry["params"].(map[string]any)
		switch entry["method"] {
		case "model/list":
			metadata++
		case "thread/start":
			threads++
			if params["ephemeral"] != true || params["baseInstructions"] != request.System || params["developerInstructions"] != "" || params["allowProviderModelFallback"] != false {
				t.Fatalf("unsafe thread params: %+v", params)
			}
			for _, key := range []string{"environments", "selectedCapabilityRoots", "dynamicTools"} {
				if len(params[key].([]any)) != 0 {
					t.Fatal("ambient runtime roots")
				}
			}
		case "turn/start":
			turns++
			if params["model"] != "native-model" || params["effort"] != "high" || params["outputSchema"] == nil {
				t.Fatalf("request fields lost: %+v", params)
			}
			inputs := params["input"].([]any)
			if len(inputs) != 3 || inputs[0].(map[string]any)["text"] != request.Prompt {
				t.Fatal("input order or text lost")
			}
			for i, media := range []string{"image/png", "image/jpeg"} {
				if !strings.HasPrefix(inputs[i+1].(map[string]any)["url"].(string), "data:"+media+";base64,") {
					t.Fatal("image bytes were not revalidated in order")
				}
			}
		}
	}
	if starts != 2 || metadata != 2 || turns != 2 || threads != 2 {
		t.Fatalf("runtime not persistent/cached/fresh: starts=%d model pages=%d turns=%d threads=%d", starts, metadata, turns, threads)
	}
	data, err := os.ReadFile(filepath.Join(p.root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SYSTEM SEPARATE", "USER SEPARATE", "REASONING"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("catalog contains note data")
		}
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.Unmarshal(data, &catalog)
	m := catalog.Models[0]
	if m["shell_type"] != "disabled" || m["apply_patch_tool_type"] != nil || len(m["experimental_supported_tools"].([]any)) != 0 {
		t.Fatal("catalog retained tools")
	}
}

func TestCodexLargeImagesUseBoundedLocalFraming(t *testing.T) {
	p, log := codexFakeProvider(t, "normal", Options{})
	request := Request{Prompt: "inspect both", Images: codexLargeImages(t)}
	if err := p.PreflightImages(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	response, err := p.Complete(context.Background(), request)
	if err != nil || response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
		t.Fatalf("large images rejected or dropped: %+v %v", response, err)
	}
	count := 0
	for _, entry := range codexReadLog(t, log) {
		if path, ok := entry["localImagePath"].(string); ok {
			if int(entry["localImageBytes"].(float64)) != len(request.Images[count].Data) {
				t.Fatal("local image data/order changed")
			}
			count++
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("temporary local image retained after native completion")
			}
		}
	}
	if count != len(request.Images) {
		t.Fatalf("submitted %d of %d large images", count, len(request.Images))
	}
}

func TestCodexIsolationPreventsAppServerStartup(t *testing.T) {
	for _, artifact := range []string{"AGENTS.md", "AGENTS.override.md", "config.toml", "other.config.toml", "skills", ".agents", "hooks.json", "plugins", "rules", "environments", "unknown"} {
		t.Run(artifact, func(t *testing.T) {
			p, log := codexFakeProvider(t, "normal", Options{ImageInput: "supported"})
			if err := os.WriteFile(filepath.Join(p.options.AuthHome, artifact), []byte("DO NOT READ OR RETURN THIS CANARY"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := p.Capabilities(context.Background())
			if !errors.Is(err, ErrIsolationUnavailable) || strings.Contains(err.Error(), "CANARY") {
				t.Fatalf("dirty profile accepted or leaked: %v", err)
			}
			if len(codexReadLog(t, log)) != 0 {
				t.Fatal("started executable before profile preflight")
			}
		})
	}
}

// These are subprocess protocol fixtures, NOT evidence of live OAuth transport
// or a real refresh grant. The opt-in native loopback test is separate.
func TestCodexSavedChatGPTLogin(t *testing.T) {
	for _, mode := range []string{"oauth-free", "oauth-plus", "oauth-pro", "oauth-business", "oauth-enterprise", "oauth-unknown", "oauth-compatible-policy", "oauth-native-refresh"} {
		t.Run(mode, func(t *testing.T) {
			p, log := codexFakeProvider(t, mode, Options{})
			if err := os.WriteFile(filepath.Join(p.options.AuthHome, "auth.json"), []byte("opaque-native-secret-not-json"), 0600); err != nil {
				t.Fatal(err)
			}
			caps, err := p.Capabilities(context.Background())
			if err != nil || caps.ResolvedModel != "native-model" || caps.ImageInput != Supported {
				t.Fatalf("saved ChatGPT metadata: %+v %v", caps, err)
			}
			for i := 0; i < 2; i++ {
				response, err := p.Complete(context.Background(), Request{Prompt: "inspect", Images: codexTestImages(t), OutputSchema: json.RawMessage(`{"type":"object"}`)})
				if err != nil || response.Text == "" || response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
					t.Fatalf("saved ChatGPT generation: %+v %v", response, err)
				}
			}
			accounts, turns := 0, 0
			for _, entry := range codexReadLog(t, log) {
				method, _ := entry["method"].(string)
				if method == "account/read" {
					accounts++
					if entry["params"].(map[string]any)["refreshToken"] != false {
						t.Fatal("requested client-managed refresh")
					}
				} else if strings.HasPrefix(method, "account/") {
					t.Fatalf("adapter attempted auth/token mutation: %s", method)
				}
				if method == "turn/start" {
					turns++
				}
			}
			if accounts != 2 || turns != 2 {
				t.Fatalf("discovery/inference lifecycle accounts=%d turns=%d", accounts, turns)
			}
			data, err := os.ReadFile(log)
			if err != nil || bytes.Contains(data, []byte("native-secret")) || bytes.Contains(data, []byte("refreshed-secret")) {
				t.Fatal("credentials escaped native profile")
			}
			data, err = os.ReadFile(filepath.Join(p.options.AuthHome, "auth.json"))
			want := "opaque-native-secret-not-json"
			if mode == "oauth-native-refresh" {
				want = "native-owned-refreshed-secret"
			}
			if err != nil || string(data) != want {
				t.Fatal("adapter interfered with native credential storage")
			}
		})
	}
}

func TestCodexOAuthPolicyIncompatibility(t *testing.T) {
	for _, mode := range []string{"oauth-managed", "oauth-incompatible-policy"} {
		t.Run(mode, func(t *testing.T) {
			p, log := codexFakeProvider(t, mode, Options{})
			_, err := p.Complete(context.Background(), Request{Prompt: "x"})
			if !errors.Is(err, ErrIsolationUnavailable) || !strings.Contains(err.Error(), "workspace administrator") {
				t.Fatalf("expected policy diagnostic, not auth-family rejection: %v", err)
			}
			for _, entry := range codexReadLog(t, log) {
				if entry["method"] == "thread/start" || entry["method"] == "turn/start" {
					t.Fatal("incompatible managed policy reached generation")
				}
			}
		})
	}
}

func TestCodexNativeCloudCacheStaysOpaque(t *testing.T) {
	p, _ := codexFakeProvider(t, "oauth-plus", Options{})
	cache := filepath.Join(p.options.AuthHome, "cloud-config-bundle-cache.json")
	// Native Codex, not this adapter, owns signature/account/expiry validation.
	// This protocol fake deliberately does not pretend to verify a signed cache.
	if err := os.WriteFile(cache, []byte("opaque-native-cache-not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatalf("adapter tried to parse native cache: %v", err)
	}
	if err := os.Chmod(cache, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("public native policy cache accepted: %v", err)
	}
}

func TestCodexManagedRequirementCompatibility(t *testing.T) {
	expected, err := codexExpectedConfig(codexArgs("/private/catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := &codexRuntime{expected: expected, accountType: "chatgpt"}
	for _, raw := range []string{
		`null`, `{}`, `{"allowedLoginMethods":["chatgpt"]}`, `{"modelProvider":"openai","cliAuthCredentialsStore":"file"}`,
		`{"allowedApprovalPolicies":["never"],"allowedSandboxModes":["read-only"],"allowedWebSearchModes":["disabled"]}`,
		`{"featureRequirements":{"shell_tool":false,"plugins":false},"allowRemoteControl":false}`,
		`{"allowRemoteControl":true,"allowLoginShell":true,"allowManagedHooksOnly":true}`,
		`{"modelCatalogJson":"/private/catalog.json","checkForUpdateOnStartup":false,"hooks":null}`,
		`{"allowedApprovalPolicies":["never",{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true}}]}`,
	} {
		if !r.compatibleRequirements(json.RawMessage(raw)) {
			t.Errorf("compatible mandatory policy rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		`[]`, `false`, `{"unknownNewRequirement":true}`, `{"allowedLoginMethods":[]}`, `{"allowedLoginMethods":["api"]}`,
		`{"allowedApprovalPolicies":["on-request"]}`, `{"allowedSandboxModes":["workspace-write"]}`, `{"allowedWebSearchModes":["live"]}`,
		`{"featureRequirements":{"shell_tool":true}}`, `{"featureRequirements":{"unknown_feature":false}}`, `{"featureRequirements":[]}`,
		`{"modelProvider":"other"}`, `{"modelProviders":{"openai":{"base_url":"https://unexpected.invalid"}}}`,
		`{"chatgptBaseUrl":"https://unexpected.invalid"}`, `{"modelCatalogJson":"/managed/tool-catalog.json"}`,
		`{"additionalDeveloperInstructions":"managed instructions requiring separate audit"}`, `{"hooks":{}}`, `{"cliAuthCredentialsStore":"ephemeral"}`,
		`{"allowedSandboxModes":["read-only",{}]}`, `{"allowedSandboxModes":[[],"read-only"]}`,
		`{"allowedLoginMethods":["chatgpt",[]]}`, `{"allowedLoginMethods":[null,"chatgpt"]}`,
		`{"allowedWebSearchModes":["disabled",false]}`, `{"allowedWebSearchModes":[42,"disabled"]}`,
		`{"allowedApprovalPolicies":["never",{}]}`, `{"allowedApprovalPolicies":[[],"never"]}`,
		`{"allowedApprovalPolicies":["never",{"granular":{"sandbox_approval":true,"rules":false}}]}`,
		`{"allowedApprovalPolicies":[{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":null}},"never"]}`,
		`{"allowedApprovalPolicies":["never",{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true,"unknown":false}}]}`,
	} {
		if r.compatibleRequirements(json.RawMessage(raw)) {
			t.Errorf("incompatible or unverified mandatory policy accepted: %s", raw)
		}
	}
	r.accountType = "apiKey"
	if !r.compatibleRequirements(json.RawMessage(`{"allowedLoginMethods":["api"]}`)) {
		t.Fatal("stored native API-key method constraint rejected")
	}
}

func TestCodexAccountRequiresAuthConsistency(t *testing.T) {
	for _, mode := range []string{"oauth-contradictory-auth", "contradictory-api-auth"} {
		for _, route := range []struct {
			name   string
			config []string
			allow  bool
		}{
			{name: "production-openai"},
			{name: "explicit-auth-free", config: []string{"-c", `model_provider="audit"`, "-c", `model_providers.audit={name="Loopback",base_url="http://127.0.0.1:1/v1",wire_api="responses",requires_openai_auth=false}`}, allow: true},
			{name: "custom-not-explicitly-auth-free", config: []string{"-c", `model_provider="audit"`, "-c", `model_providers.audit={name="Loopback",base_url="http://127.0.0.1:1/v1",wire_api="responses"}`}},
			{name: "custom-requires-auth", config: []string{"-c", `model_provider="audit"`, "-c", `model_providers.audit={name="Loopback",base_url="http://127.0.0.1:1/v1",wire_api="responses",requires_openai_auth=true}`}},
		} {
			t.Run(mode+"/"+route.name, func(t *testing.T) {
				p, log := codexFakeProvider(t, mode, Options{})
				ctx := context.Background()
				if err := p.prepare(ctx); err != nil {
					t.Fatal(err)
				}
				args := append(codexArgs(""), route.config...)
				r, err := codexStartRuntime(ctx, p.executable, args, p.root, codexEnvironment(p.options.AuthHome, p.root), p.options.MaxOutputBytes)
				if r != nil {
					defer r.stop()
				}
				if route.allow {
					if err != nil {
						t.Fatalf("explicit auth-free provider rejected: %v", err)
					}
				} else if !errors.Is(err, ErrProtocol) {
					t.Fatalf("contradictory metadata accepted: %v", err)
				}
				for _, entry := range codexReadLog(t, log) {
					if entry["method"] == "thread/start" || entry["method"] == "turn/start" {
						t.Fatal("account check submitted generation")
					}
				}
			})
		}
	}
}

func TestCodexModelAndImagePreflight(t *testing.T) {
	for _, mode := range []string{"text-only", "unknown-modalities"} {
		p, log := codexFakeProvider(t, mode, Options{})
		caps, err := p.Capabilities(context.Background())
		if err != nil || caps.ImageInput == Supported {
			t.Fatalf("unsupported metadata: %+v %v", caps, err)
		}
		_, err = p.Complete(context.Background(), Request{Prompt: "x", Images: codexTestImages(t)})
		if err == nil {
			t.Fatal("unsupported images silently accepted")
		}
		for _, entry := range codexReadLog(t, log) {
			if entry["method"] == "turn/start" || entry["method"] == "thread/start" {
				t.Fatal("started image inference despite unsupported model")
			}
		}
	}
	p, log := codexFakeProvider(t, "normal", Options{})
	for _, req := range []Request{{Model: "missing"}, {Variant: "ultra"}, {Images: []imagecontext.Image{{Data: []byte("not an image")}}}, {OutputSchema: json.RawMessage(`[]`)}} {
		_, err := p.Complete(context.Background(), req)
		if err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	for _, entry := range codexReadLog(t, log) {
		if entry["method"] == "turn/start" {
			t.Fatal("invalid request reached generation")
		}
	}
}

func TestCodexMetadataRecoveryExpiryAndExplicitOverride(t *testing.T) {
	p, log := codexFakeProvider(t, "metadata-once", Options{})
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected transient metadata failure: %v", err)
	}
	if caps, err := p.Capabilities(context.Background()); err != nil || caps.ImageInput != Supported {
		t.Fatalf("transient failure was cached: %+v %v", caps, err)
	}
	p.expires = time.Now().Add(-time.Second)
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, entry := range codexReadLog(t, log) {
		if entry["launch"] != nil {
			starts++
		}
	}
	if starts != 3 {
		t.Fatalf("metadata cache expiration did not refresh: starts=%d", starts)
	}
	known, _ := codexFakeProvider(t, "text-only", Options{ImageInput: "supported"})
	caps, err := known.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Unsupported {
		t.Fatalf("explicit known-negative metadata was overridden: %+v %v", caps, err)
	}
	unknown, _ := codexFakeProvider(t, "unknown-modalities", Options{ImageInput: "supported"})
	response, err := unknown.Complete(context.Background(), Request{Prompt: "inspect", Images: codexTestImages(t)})
	if err != nil || response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
		t.Fatalf("explicit override not honored: %+v %v", response, err)
	}
	data, err := os.ReadFile(filepath.Join(unknown.root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"input_modalities":["text","image"]`)) {
		t.Fatal("native normalizer would silently strip override-authorized images")
	}
}

func TestCodexFailuresNeverReturnPartialOrReplay(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{{"bad-version", ErrIsolationUnavailable}, {"unknown-auth", ErrAuthentication}, {"signed-out", ErrAuthentication}, {"oauth-refresh-request", ErrIsolationUnavailable}, {"managed", ErrIsolationUnavailable}, {"tampered", ErrIsolationUnavailable}, {"endpoint-tamper", ErrIsolationUnavailable}, {"persistent", ErrIsolationUnavailable}, {"instructions", ErrIsolationUnavailable}, {"crash", ErrProviderUnavailable}, {"metadata-crash", ErrProviderUnavailable}, {"rpc-auth", ErrAuthentication}, {"tool-request", ErrIsolationUnavailable}, {"tool-item", ErrIsolationUnavailable}, {"malformed", ErrProtocol}, {"failed", ErrProviderUnavailable}, {"output-limit", ErrOutputLimit}, {"cursor-loop", ErrProtocol}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, log := codexFakeProvider(t, tc.mode, Options{})
			response, err := p.Complete(context.Background(), Request{Prompt: "x"})
			if response.Text != "" || !errors.Is(err, tc.want) {
				t.Fatalf("response=%+v err=%v want=%v", response, err, tc.want)
			}
			turns := 0
			for _, entry := range codexReadLog(t, log) {
				if entry["method"] == "turn/start" {
					turns++
				}
			}
			if turns > 1 {
				t.Fatal("ambiguous turn replayed")
			}
		})
	}
}

func TestCodexCancellationSerializesAndInterrupts(t *testing.T) {
	p, log := codexFakeProvider(t, "hang", Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := p.Complete(ctx, Request{Prompt: "x"}); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, entry := range codexReadLog(t, log) {
			if entry["method"] == "turn/start" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not start")
		}
		time.Sleep(time.Millisecond)
	}
	queued, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := p.Capabilities(queued); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation affected active request: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active cancellation did not join runtime")
	}
	found := false
	for _, entry := range codexReadLog(t, log) {
		if entry["method"] == "turn/interrupt" {
			found = true
		}
	}
	if !found {
		t.Fatal("native turn was not interrupted")
	}
	if p.runtime != nil {
		t.Fatal("canceled runtime retained")
	}
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Supported {
		t.Fatalf("cancellation poisoned subsequent caller: %+v %v", caps, err)
	}
}

func TestCodexConcurrentCompletionsAndClose(t *testing.T) {
	p, log := codexFakeProvider(t, "normal", Options{})
	var group sync.WaitGroup
	for i := 0; i < 6; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r, err := p.Complete(context.Background(), Request{Prompt: "x"})
			if err != nil || r.Text == "" {
				t.Errorf("concurrent completion: %+v %v", r, err)
			}
		}()
	}
	group.Wait()
	root := p.root
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("Close did not remove provider-owned runtime directory")
	}
	if _, err := p.Complete(context.Background(), Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion after Close: %v", err)
	}
	starts := 0
	for _, entry := range codexReadLog(t, log) {
		if entry["method"] == "thread/start" {
			starts++
		}
	}
	if starts != 6 {
		t.Fatalf("lost or replayed callers: %d", starts)
	}
}

func TestCodexCloseCancelsAndJoinsActiveWork(t *testing.T) {
	p, _ := codexFakeProvider(t, "version-hang", Options{Timeout: time.Minute})
	done := make(chan error, 1)
	go func() { _, err := p.Capabilities(context.Background()); done <- err }()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Close did not bound native process cleanup")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	default:
		t.Fatal("Close returned without joining active work")
	}
}

func TestCodexRuntimeResultSelectsOnlyCompletedFinal(t *testing.T) {
	phase := "final_answer"
	comment := "commentary"
	s := codexTurnState{done: true, status: "completed", limit: 1024}
	_ = s.add(codexAgentItem{ID: "a", Text: "progress", Phase: &comment})
	_ = s.add(codexAgentItem{ID: "b", Text: "final", Phase: &phase})
	if text, err := s.result(); err != nil || text != "final" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	s.status = "failed"
	if text, err := s.result(); err == nil || text != "" {
		t.Fatal("failed turn exposed completed-but-not-final text")
	}
	for _, code := range []int{-32600, -32601, -32602} {
		if !errors.Is(codexRPCError(code, "secret"), ErrProtocol) {
			t.Fatal("protocol errors misclassified")
		}
	}
	if !errors.Is(codexRPCError(0, "rate limit"), ErrRateLimited) || !errors.Is(codexRPCError(0, "model not found"), ErrModelUnavailable) {
		t.Fatal("upstream errors misclassified")
	}
}
