//go:build linux

package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// TestAntigravityProcess is a real pipe/process fixture, never a provider call.
func TestAntigravityProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	logPath := os.Args[index+1]
	args := os.Args[index+2:]
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(3)
	}
	defer log.Close()
	enc := json.NewEncoder(log)
	_ = enc.Encode(map[string]any{"start": args, "pid": os.Getpid(), "home": os.Getenv("HOME"), "api_key": os.Getenv("GEMINI_API_KEY")})
	id := fmt.Sprintf("fixture-%d", os.Getpid())
	for i, arg := range args {
		if arg == "--conversation" {
			id = args[i+1]
		}
		if arg == "--json-schema" {
			os.Exit(4)
		}
	}
	output := json.NewEncoder(os.Stdout)
	_ = output.Encode(map[string]any{"event": "init", "conversation_id": id})
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var input struct {
			Event   string `json:"event"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &input) != nil || input.Event != "user" {
			os.Exit(5)
		}
		_ = enc.Encode(map[string]any{"input": input.Message.Content})
		prompt := input.Message.Content
		_, prompt, _ = strings.Cut(prompt, "\n\nInput:\n")
		switch prompt {
		case "crash":
			os.Exit(7)
		case "auth-stderr":
			fmt.Fprintln(os.Stderr, "authentication required: secret-token")
			os.Exit(1)
		case "hang":
			for {
				time.Sleep(time.Hour)
			}
		case "malformed":
			fmt.Println("{broken")
			continue
		case "flood":
			fmt.Println(strings.Repeat("x", 2<<20))
			continue
		case "stderr-flood":
			fmt.Fprintln(os.Stderr, strings.Repeat("x", 2<<20))
			continue
		}
		if prompt == "quota" || prompt == "auth" || prompt == "model" {
			message := map[string]string{"quota": "RESOURCE_EXHAUSTED quota secret-token", "auth": "OAuth refresh failed secret-token", "model": "invalid model selection secret-token"}[prompt]
			_ = output.Encode(map[string]any{"event": "result", "result": map[string]any{"status": "ERROR", "error": message}})
			continue
		}
		if prompt == "tool-success" || prompt == "tool-denied" {
			step := map[string]any{"conversation_id": id, "step_type": "tool", "state": "DONE", "tool_name": "run_command"}
			if prompt == "tool-denied" {
				step["state"] = "ERROR"
				step["tool_info"] = map[string]any{"error": map[string]string{"message": "tool call denied by pre-tool hook: Ginkgo generation-only tool policy"}}
			}
			_ = output.Encode(map[string]any{"event": "step_update", "step_update": step})
		}
		for _, part := range []string{"hello", " world"} {
			_ = output.Encode(map[string]any{"event": "step_update", "step_update": map[string]any{"conversation_id": id, "step_type": "agent_response", "text_delta": part}})
		}
		if prompt == "wait-result" {
			for {
				time.Sleep(time.Hour)
			}
		}
		result := map[string]any{"conversation_id": id, "status": "SUCCESS", "response": "hello world"}
		if prompt == "wrong-id" {
			result["conversation_id"] = "unrelated"
		}
		if strings.Contains(prompt, "Return exactly one JSON value matching this schema") {
			result["response"] = `{"answer":"hello world"}`
		}
		if strings.Contains(prompt, `"input":"note-fixture"`) {
			result["response"] = `{"action":"create_note","title":"Fixture","body":"Clean note","tags":["fixture"]}`
		}
		_ = output.Encode(map[string]any{"event": "result", "result": result})
	}
	os.Exit(0)
}

func antigravityFixture(t *testing.T) (*AntigravityProvider, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.jsonl")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "agy")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+quote(exe)+" -test.run=^TestAntigravityProcess$ -- "+quote(log)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := NewAntigravity(AntigravityOptions{Executable: wrapper, TempDir: dir, Options: Options{Timeout: 3 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p, log
}

func antigravityCalls(t *testing.T, path string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func TestAntigravityWarmStreamAndScope(t *testing.T) {
	p, log := antigravityFixture(t)
	if len(antigravityCalls(t, log)) != 0 {
		t.Fatal("constructor launched CLI")
	}
	request := Request{SessionKey: "work", System: "instructions", Prompt: "first"}
	var deltas []string
	for i := 0; i < 2; i++ {
		response, err := p.CompleteStream(context.Background(), request, func(text string) error { deltas = append(deltas, text); return nil })
		if err != nil || response.Text != "hello world" {
			t.Fatalf("turn: %+v %v", response, err)
		}
	}
	if strings.Join(deltas, "") != "hello worldhello world" {
		t.Fatal(deltas)
	}
	calls := antigravityCalls(t, log)
	if len(calls) != 3 {
		t.Fatalf("expected one process and two turns: %s", calls)
	}
	var args []string
	_ = json.Unmarshal(calls[0]["start"], &args)
	joined := strings.Join(args, " ")
	for _, required := range []string{"--input-format stream-json", "--output-format stream-json", "--model " + DefaultAntigravityModel, "--disable-slash-commands"} {
		if !strings.Contains(joined, required) {
			t.Fatal(args)
		}
	}
	if strings.Contains(joined, "skip-permissions") || strings.Contains(joined, "instructions") {
		t.Fatal(args)
	}
	request.SessionKey = "personal"
	if _, err := p.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(antigravityCalls(t, log)) != 5 {
		t.Fatal("scope reused another conversation")
	}
	request.System = "different instructions"
	if _, err := p.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(antigravityCalls(t, log)) != 7 {
		t.Fatal("changed instructions reused conversation")
	}
}

func TestAntigravityUnscopedAndSchema(t *testing.T) {
	p, log := antigravityFixture(t)
	for i := 0; i < 2; i++ {
		response, err := p.Complete(context.Background(), Request{Prompt: "hello", OutputSchema: json.RawMessage(`{"type":"object"}`)})
		if err != nil || response.Text != `{"answer":"hello world"}` {
			t.Fatalf("%+v %v", response, err)
		}
		if p.session != nil {
			t.Fatal("unscoped session retained")
		}
	}
	if len(antigravityCalls(t, log)) != 4 {
		t.Fatal("unscoped requests shared history")
	}
}

func TestAntigravityFailuresRetireWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   error
	}{
		{"crash", ErrProviderUnavailable}, {"auth", ErrAuthentication}, {"auth-stderr", ErrAuthentication},
		{"quota", ErrRateLimited}, {"model", ErrModelUnavailable}, {"malformed", ErrProtocol},
		{"wrong-id", ErrProtocol}, {"flood", ErrOutputLimit}, {"stderr-flood", ErrOutputLimit},
		{"tool-success", ErrIsolationUnavailable},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			p, log := antigravityFixture(t)
			_, err := p.Complete(context.Background(), Request{SessionKey: "work", Prompt: tc.prompt})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatal("leaked native diagnostics")
			}
			if len(antigravityCalls(t, log)) != 2 || p.session != nil {
				t.Fatal("failure replayed or retained")
			}
			if _, err := p.Complete(context.Background(), Request{SessionKey: "work", Prompt: "recovered"}); err != nil {
				t.Fatal(err)
			}
			calls := antigravityCalls(t, log)
			if len(calls) != 4 || strings.Contains(string(calls[2]["start"]), "--conversation") {
				t.Fatal("ambiguous turn resumed")
			}
		})
	}
}

func TestAntigravityIdleCrashResumes(t *testing.T) {
	p, log := antigravityFixture(t)
	r := Request{SessionKey: "work", Prompt: "hello"}
	if _, err := p.Complete(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	id := p.session.id
	if err := syscall.Kill(p.session.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-p.session.done
	if _, err := p.Complete(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	calls := antigravityCalls(t, log)
	if len(calls) != 4 || !strings.Contains(string(calls[2]["start"]), id) {
		t.Fatal("idle crash did not resume successful history")
	}
}

func TestAntigravityCancellationAndClose(t *testing.T) {
	p, _ := antigravityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Complete(ctx, Request{SessionKey: "work", Prompt: "hang"})
	if !errors.Is(err, context.DeadlineExceeded) || p.session != nil {
		t.Fatalf("%v", err)
	}
	if _, err := p.Complete(context.Background(), Request{SessionKey: "work", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	s := p.session
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("Close did not reap process")
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Fatal("workspace not removed")
	}
	if _, err := p.Complete(context.Background(), Request{Prompt: "hello"}); err == nil {
		t.Fatal("closed provider accepted a request")
	}
}

func TestAntigravityCallbackFailure(t *testing.T) {
	p, _ := antigravityFixture(t)
	want := errors.New("consumer stopped")
	_, err := p.CompleteStream(context.Background(), Request{SessionKey: "work", Prompt: "hello"}, func(string) error { return want })
	if !errors.Is(err, want) || p.session != nil {
		t.Fatal(err)
	}
}

func TestAntigravityQueuedCancellationAndActiveClose(t *testing.T) {
	p, log := antigravityFixture(t)
	started := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		_, err := p.CompleteStream(context.Background(), Request{SessionKey: "work", Prompt: "wait-result"}, func(string) error {
			select {
			case started <- struct{}{}:
			default:
			}
			return nil
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := p.Complete(ctx, Request{SessionKey: "personal", Prompt: "hello"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		t.Fatalf("queued cancellation killed active turn: %v", err)
	default:
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(antigravityCalls(t, log)) != 2 {
		t.Fatal("queued request submitted input")
	}
}

func TestAntigravityNoteRetainsUnsupportedImages(t *testing.T) {
	p, log := antigravityFixture(t)
	store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
	service := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: store})
	im := aiImage(t)
	result, err := service.Note(context.Background(), NoteRequest{Namespace: "work", Text: "note-fixture", Images: []imagecontext.Image{im}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FallbackReason != "" || result.Entry.Body != "Clean note" || len(result.Entry.Images()) != 1 || !strings.Contains(result.ImageStatus, "unsupported") {
		t.Fatalf("unexpected note/image outcome: %+v", result)
	}
	retained, err := store.Load(context.Background(), "work", result.Entry.Images()[0])
	if err != nil || !bytes.Equal(retained.Data, im.Data) {
		t.Fatalf("original image not retained: %v", err)
	}
	calls := antigravityCalls(t, log)
	if len(calls) != 2 || strings.Contains(string(calls[1]["input"]), "base64") {
		t.Fatal("unexpected image submission")
	}
}

func TestAntigravityRegistryStreaming(t *testing.T) {
	p, _ := antigravityFixture(t)
	registry := NewRegistry(time.Minute)
	defer registry.Close()
	managed, err := registry.Resolve("agy", func() (Provider, error) { return p, nil })
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	response, err := managed.(StreamingProvider).CompleteStream(context.Background(), Request{SessionKey: "work", Prompt: "hello"}, func(text string) error { streamed.WriteString(text); return nil })
	if err != nil || streamed.String() != "hello world" || response.Text != streamed.String() {
		t.Fatalf("%+v %v", response, err)
	}
}

func TestAntigravityImagesNeverSubmitted(t *testing.T) {
	p, log := antigravityFixture(t)
	p.options.ImageInput = "supported"
	caps, err := p.Capabilities(context.Background())
	if err != nil || caps.ImageInput != Unsupported {
		t.Fatalf("%+v %v", caps, err)
	}
	response, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{{}}})
	if !errors.Is(err, ErrImagePreparation) || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("%+v %v", response, err)
	}
	if len(antigravityCalls(t, log)) != 0 {
		t.Fatal("image rejection launched inference")
	}
}

func TestAntigravityScopedPolicyAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := antigravityPolicy(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".agents", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]struct {
		PreToolUse []struct {
			Matcher string
			Hooks   []struct{ Command string }
		}
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	hooks := policy["ginkgo-generation-only"].PreToolUse
	if len(hooks) != 1 || hooks[0].Matcher != "*" {
		t.Fatal("policy does not cover all tools")
	}
	result, err := exec.Command("sh", "-c", hooks[0].Hooks[0].Command).Output()
	var decision struct{ Decision string }
	if err != nil || json.Unmarshal(result, &decision) != nil || decision.Decision != "deny" {
		t.Fatalf("%s %v", result, err)
	}
	t.Setenv("GEMINI_API_KEY", "do-not-forward")
	t.Setenv("GOOGLE_API_KEY", "do-not-forward")
	t.Setenv("HOME", "/original-home")
	env := strings.Join(antigravityEnvironment("/dedicated-home"), "\n")
	if strings.Contains(env, "do-not-forward") || strings.Contains(env, "/original-home") || !strings.Contains(env, "HOME=/dedicated-home") {
		t.Fatal("incorrect OAuth environment")
	}
}

func TestAntigravityDeniedToolAndBounds(t *testing.T) {
	p, log := antigravityFixture(t)
	if response, err := p.Complete(context.Background(), Request{Prompt: "tool-denied"}); err != nil || response.Text != "hello world" {
		t.Fatalf("%+v %v", response, err)
	}
	for _, request := range []Request{
		{Prompt: strings.Repeat("x", p.options.MaxInputBytes)},
		{Prompt: "small", OutputSchema: json.RawMessage(`null`)},
		{Prompt: "small", Variant: "invalid"},
		{Prompt: "small", Model: "--bad"},
	} {
		if _, err := p.Complete(context.Background(), request); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if len(antigravityCalls(t, log)) != 2 {
		t.Fatal("invalid input launched inference")
	}
}

func TestAntigravityNativeImageRejection(t *testing.T) {
	if os.Getenv("GINKGO_AGY_PROTOCOL_SMOKE") != "1" {
		t.Skip("set GINKGO_AGY_PROTOCOL_SMOKE=1 to check native image-block rejection without inference")
	}
	p, err := NewAntigravity(AntigravityOptions{Executable: os.Getenv("GINKGO_AGY_EXECUTABLE")})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s := &antigravitySession{dir: t.TempDir()}
	p.session = s
	if err := p.start(s, p.options.Model, ""); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]any{"content": []any{map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(aiImage(t).Data)}}}}})
	if _, err := s.stdin.Write(append(input, '\n')); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case line, ok := <-s.lines:
			if !ok || line.err != nil {
				t.Fatal("native parser exited before its error result")
			}
			var event antigravityEvent
			if err := json.Unmarshal(line.data, &event); err != nil {
				t.Fatal("invalid native JSON")
			}
			if event.Event == "result" {
				if event.Result == nil || event.Result.Status != "ERROR" || !strings.Contains(event.Result.Error, `content block type "image" is not supported`) {
					t.Fatal("native image contract changed; recheck transport capabilities")
				}
				return
			}
		}
	}
}

func TestAntigravityLiveStreamSmoke(t *testing.T) {
	if os.Getenv("GINKGO_AGY_LIVE_SMOKE") != "1" {
		t.Skip("set GINKGO_AGY_LIVE_SMOKE=1 for two synthetic authenticated turns")
	}
	p, err := NewAntigravity(AntigravityOptions{Executable: os.Getenv("GINKGO_AGY_EXECUTABLE"), TempDir: t.TempDir(), Options: Options{Timeout: time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	request := Request{SessionKey: "synthetic-test", System: "Return the requested JSON only.", Prompt: `Return {"answer":"ginkgo"}.`, OutputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
	for i := 0; i < 2; i++ {
		response, err := p.Complete(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		var value struct{ Answer string }
		if json.Unmarshal([]byte(response.Text), &value) != nil || value.Answer != "ginkgo" {
			t.Fatalf("unexpected synthetic response: %s", response.Text)
		}
	}
	if p.session.turns != 2 {
		t.Fatal("did not retain warm session")
	}
}
