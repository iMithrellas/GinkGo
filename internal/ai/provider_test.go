//go:build linux

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T, mode string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "provider")
	// The production adapter never uses a shell. This fixture launches this
	// test binary with Go's test flags while preserving the adapter's argv.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec \"$GINKGO_PROVIDER_TEST_BINARY\" -test.run=TestProviderHelper -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GINKGO_PROVIDER_TEST_BINARY", binary)
	t.Setenv("GINKGO_PROVIDER_HELPER", mode)
	return path
}

// TestProviderHelper is a subprocess fixture, never a live model/provider call.
func TestProviderHelper(t *testing.T) {
	mode := os.Getenv("GINKGO_PROVIDER_HELPER")
	if mode == "" {
		return
	}
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "--version" {
		if version := os.Getenv("GINKGO_PROVIDER_TEST_VERSION"); version != "" {
			fmt.Println(version)
		} else if mode == "server-v1" {
			fmt.Println("opencode v1.99.0")
		} else if mode == "server-old-saved-auth" {
			fmt.Println("opencode v2.0.19")
		} else {
			fmt.Println("opencode v2.0.30")
		}
		os.Exit(0)
	}
	if capture := os.Getenv("GINKGO_PROVIDER_CAPTURE"); capture != "" {
		config, _ := os.ReadFile(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "opencode.json"))
		data, _ := json.Marshal(map[string]any{"args": os.Args, "env": os.Environ(), "config": string(config)})
		_ = os.WriteFile(capture, data, 0600)
	}
	if mode == "cli-cancel" || mode == "server-cancel" {
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("GINKGO_PROVIDER_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
	}
	if strings.HasPrefix(mode, "cli-") {
		input, _ := io.ReadAll(os.Stdin)
		switch mode {
		case "cli-ok":
			var request Request
			if err := json.Unmarshal(input, &request); err != nil {
				os.Exit(2)
			}
			_ = json.NewEncoder(os.Stdout).Encode(Response{Text: request.System + "|" + request.Prompt})
		case "cli-events":
			fmt.Print("{\"type\":\"text\",\"text\":\"hello\"}\n{\"text\":\"done\"}\n")
		case "cli-missing":
			fmt.Print(`{}`)
		case "cli-null":
			fmt.Print(`{"text":null}`)
		case "cli-trailing":
			fmt.Print(`{"text":"ok"} {"text":"extra"}`)
		case "cli-exit":
			fmt.Fprint(os.Stderr, "secret-auth-and-note")
			os.Exit(9)
		case "cli-output":
			fmt.Print(strings.Repeat("x", 4096))
		case "cli-stderr":
			fmt.Fprint(os.Stderr, strings.Repeat("x", 4096))
		case "cli-cancel":
			time.Sleep(time.Hour)
		}
		os.Exit(0)
	}
	if mode == "server-bad-address" {
		fmt.Println("server listening on http://example.com:80")
		select {}
	}
	if mode == "server-stdout-overflow" || mode == "server-stderr-overflow" {
		var writer io.Writer = os.Stdout
		if mode == "server-stderr-overflow" {
			writer = os.Stderr
		}
		fmt.Fprint(writer, strings.Repeat("x", 4096))
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "opencode" || password != os.Getenv("OPENCODE_PASSWORD") {
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/credential" {
			if mode == "server-ephemeral" {
				// A fresh profile must not require saved credentials.
				os.Exit(8)
			}
			if mode == "server-no-credentials-api" {
				w.WriteHeader(404)
				return
			}
			if mode == "server-console" {
				fmt.Fprint(w, `{"data":[{"integrationID":"opencode","value":{"secret":"never log this"}}]}`)
			} else {
				fmt.Fprint(w, `{"data":[]}`)
			}
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/experimental/generate" {
			w.WriteHeader(404)
			return
		}
		if mode == "server-console" {
			// The credential safety check must prevent generation.
			os.Exit(7)
		}
		var body struct {
			Prompt string    `json:"prompt"`
			Model  *modelRef `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		switch mode {
		case "server-ephemeral":
			cwd, _ := os.Getwd()
			if os.Getenv("OPENCODE_DB") != filepath.Join(cwd, "private.db") || os.Getenv("XDG_DATA_HOME") != filepath.Join(cwd, "data") || os.Getenv("XDG_STATE_HOME") != filepath.Join(cwd, "state") || os.Getenv("OPENAI_API_KEY") != "synthetic-api-key" {
				w.WriteHeader(500)
				return
			}
			fmt.Fprint(w, `{"data":{"text":"isolated"}}`)
		case "server-ok":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"text": body.Prompt}})
		case "server-model":
			if body.Model == nil || body.Model.ProviderID != "openai" || body.Model.ID != "gpt-test" || body.Model.Variant != "low" {
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"data":{"text":" {\"action\":\"create_note\"} "}}`)
		case "server-auth":
			w.WriteHeader(401)
			fmt.Fprint(w, "secret-auth-and-note")
		case "server-unavailable":
			w.WriteHeader(503)
			fmt.Fprint(w, "secret-auth-and-note")
		case "server-unknown-model":
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"_tag": "InvalidRequestError", "message": "Model unavailable: " + body.Model.ProviderID + "/" + body.Model.ID})
		case "server-malformed":
			fmt.Fprint(w, `{"data":{"text":42}}`)
		case "server-events":
			fmt.Fprint(w, "data: {\"text\":\"unsafe fallback\"}\n\n")
		case "server-missing":
			fmt.Fprint(w, `{"data":{}}`)
		case "server-null":
			fmt.Fprint(w, `{"data":null}`)
		case "server-output":
			fmt.Fprint(w, strings.Repeat("x", 4096))
		case "server-cancel":
			<-r.Context().Done()
		case "server-redirect":
			http.Redirect(w, r, "http://example.com/", http.StatusTemporaryRedirect)
		}
	})}
	fmt.Printf("server listening on http://%s\n", listener.Addr())
	_ = server.Serve(listener)
	os.Exit(0)
}

func TestCLIContract(t *testing.T) {
	provider, err := NewCLI(CLIOptions{Executable: fixture(t, "cli-ok"), Args: []string{"--fixed"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(context.Background(), Request{System: "instructions", Prompt: "$(not a command)\n"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "instructions|$(not a command)\n" {
		t.Fatalf("unexpected text: %q", result.Text)
	}
}

func TestCLIRejectsMalformedOutputAndBounds(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{
		{"cli-events", ErrProtocol}, {"cli-missing", ErrProtocol}, {"cli-null", ErrProtocol},
		{"cli-trailing", ErrProtocol}, {"cli-output", ErrOutputLimit}, {"cli-stderr", ErrOutputLimit},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			provider, err := NewCLI(CLIOptions{Options: Options{MaxOutputBytes: 256}, Executable: fixture(t, tc.mode)})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Complete(context.Background(), Request{Prompt: "input"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCLIErrorsDoNotDiscloseStderr(t *testing.T) {
	provider, _ := NewCLI(CLIOptions{Executable: fixture(t, "cli-exit")})
	_, err := provider.Complete(context.Background(), Request{})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestOpenCodeContractAndIsolation(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("GINKGO_PROVIDER_CAPTURE", capture)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"plugins":["unsafe"]}`)
	t.Setenv("OPENCODE_SERVER", "http://unsafe.invalid")
	t.Setenv("OPENCODE_SIMULATE", "true")
	t.Setenv("OPENCODE_DB", "/saved-auth-data/custom.db")
	t.Setenv("XDG_DATA_HOME", "/saved-auth-data")
	provider, err := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-model"), Model: "openai/gpt-test#low"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(context.Background(), Request{System: "system", Prompt: "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != ` {"action":"create_note"} ` {
		t.Fatalf("model text changed: %q", result.Text)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var invocation struct {
		Args   []string `json:"args"`
		Env    []string `json:"env"`
		Config string   `json:"config"`
	}
	if err := json.Unmarshal(data, &invocation); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(invocation.Args, " ")
	if !strings.Contains(args, "-- serve --hostname 127.0.0.1 --port 0") || strings.Contains(args, "prompt") {
		t.Fatalf("unexpected argv: %s", args)
	}
	env := strings.Join(invocation.Env, "\n")
	for _, forbidden := range []string{"OPENCODE_CONFIG_CONTENT=", "OPENCODE_SERVER=", "OPENCODE_SIMULATE="} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("unsafe inherited variable: %s", forbidden)
		}
	}
	if !strings.Contains(env, "XDG_DATA_HOME=/saved-auth-data") {
		t.Fatal("saved auth path lost")
	}
	if !strings.Contains(env, "OPENCODE_DB=/saved-auth-data/custom.db") {
		t.Fatal("custom saved auth database lost")
	}
	var config struct {
		Plugins  []string            `json:"plugins"`
		Policies []map[string]string `json:"policies"`
	}
	if err := json.Unmarshal([]byte(invocation.Config), &config); err != nil {
		t.Fatal(err)
	}
	if strings.Join(config.Plugins, ",") != "-*,opencode.models.dev,opencode.provider.*" || len(config.Policies) != 4 {
		t.Fatalf("unsafe configuration: %s", invocation.Config)
	}
}

func TestOpenCodeRefusesConsoleBeforeGeneration(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-console")})
	_, err := provider.Complete(context.Background(), Request{Prompt: "must not generate"})
	if err == nil || !strings.Contains(err.Error(), "does not support saved Console credentials") || strings.Contains(err.Error(), "never log") {
		t.Fatalf("unsafe console behavior: %v", err)
	}
}

func TestOpenCodeRefusesV1BeforeServerLaunch(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-v1")})
	_, err := provider.Complete(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "requires a V2 executable") {
		t.Fatalf("unsafe version behavior: %v", err)
	}
}

func TestProviderDeadline(t *testing.T) {
	for _, mode := range []string{"cli-cancel", "server-cancel"} {
		t.Run(mode, func(t *testing.T) {
			var provider Provider
			executable := fixture(t, mode)
			limits := Options{Timeout: 50 * time.Millisecond}
			if mode == "cli-cancel" {
				provider, _ = NewCLI(CLIOptions{Executable: executable, Options: limits})
			} else {
				provider, _ = NewOpenCode(OpenCodeOptions{Executable: executable, Options: limits})
			}
			_, err := provider.Complete(context.Background(), Request{})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOpenCodeLabelsInstructions(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-ok")})
	result, err := provider.Complete(context.Background(), Request{System: "rules", Prompt: "data"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "System instructions:\nrules\n\nUser input:\ndata" {
		t.Fatalf("wrong prompt: %q", result.Text)
	}
}

func TestOpenCodeProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{
		{"server-auth", ErrAuthentication}, {"server-malformed", ErrProtocol}, {"server-events", ErrProtocol},
		{"server-missing", ErrProtocol}, {"server-null", ErrProtocol}, {"server-bad-address", ErrProtocol},
		{"server-output", ErrOutputLimit},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			provider, _ := NewOpenCode(OpenCodeOptions{Options: Options{MaxOutputBytes: 256, Timeout: 3 * time.Second}, Executable: fixture(t, tc.mode)})
			_, err := provider.Complete(context.Background(), Request{Prompt: "input"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	for _, mode := range []string{"server-unavailable", "server-redirect"} {
		t.Run(mode, func(t *testing.T) {
			provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, mode)})
			_, err := provider.Complete(context.Background(), Request{})
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestProviderInputLimitsBeforeLaunch(t *testing.T) {
	cli, _ := NewCLI(CLIOptions{Executable: "nonexistent-ai-executable", Options: Options{MaxInputBytes: 16}})
	opencode, _ := NewOpenCode(OpenCodeOptions{Executable: "nonexistent-ai-executable", Options: Options{MaxInputBytes: 16}})
	for _, provider := range []Provider{cli, opencode} {
		_, err := provider.Complete(context.Background(), Request{Prompt: strings.Repeat("x", 17)})
		if !errors.Is(err, ErrInputLimit) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestProviderCancellationKillsProcessGroup(t *testing.T) {
	for _, mode := range []string{"cli-cancel", "server-cancel"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("GINKGO_PROVIDER_CHILD_PID", pidFile)
			executable := fixture(t, mode)
			var provider Provider
			if mode == "cli-cancel" {
				provider, _ = NewCLI(CLIOptions{Executable: executable})
			} else {
				provider, _ = NewOpenCode(OpenCodeOptions{Executable: executable})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := provider.Complete(ctx, Request{Prompt: "wait"}); done <- err }()
			var childPID int
			deadline := time.After(5 * time.Second)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for childPID == 0 {
				select {
				case <-deadline:
					t.Fatal("helper did not spawn child")
				case <-ticker.C:
					if data, err := os.ReadFile(pidFile); err == nil {
						childPID, _ = strconv.Atoi(string(data))
					}
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled provider did not stop")
			}
			// A child may be a zombie until PID 1 reaps it, but must not run.
			for deadline := time.Now().Add(time.Second); ; {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", childPID))
				if err != nil || strings.Contains(string(data), ") Z ") {
					break
				}
				if time.Now().After(deadline) {
					_ = syscall.Kill(childPID, syscall.SIGKILL)
					t.Fatalf("child still alive: %s", data)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestProviderOptions(t *testing.T) {
	if _, err := NewCLI(CLIOptions{}); err == nil {
		t.Fatal("missing executable accepted")
	}
	if _, err := NewOpenCode(OpenCodeOptions{Options: Options{Timeout: -1}}); err == nil {
		t.Fatal("negative timeout accepted")
	}
	if _, err := NewOpenCode(OpenCodeOptions{Model: "not-a-reference"}); err == nil {
		t.Fatal("invalid model accepted")
	}
}
