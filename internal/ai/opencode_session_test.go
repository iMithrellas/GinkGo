//go:build linux

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func sessionFixture(t *testing.T, mode string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "opencode-session-fixture")
	script := "#!/bin/sh\nexec \"$GINKGO_SESSION_TEST_BINARY\" -test.run='^TestOpenCodeSessionHelper$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GINKGO_SESSION_TEST_BINARY", binary)
	t.Setenv("GINKGO_SESSION_HELPER_MODE", mode)
	t.Setenv("GINKGO_SESSION_CAPTURE", filepath.Join(t.TempDir(), "capture.json"))
	return path
}

// TestOpenCodeSessionHelper runs only in a child process launched as a fake
// OpenCode executable. It never calls a model or accesses user auth.
func TestOpenCodeSessionHelper(t *testing.T) {
	mode := os.Getenv("GINKGO_SESSION_HELPER_MODE")
	if mode == "" {
		return
	}
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "--version" {
		fmt.Println("opencode v2.0.31")
		os.Exit(0)
	}
	if mode == "session-stdout-overflow" || mode == "session-stderr-overflow" {
		writer := os.Stdout
		if mode == "session-stderr-overflow" {
			writer = os.Stderr
		}
		_, _ = fmt.Fprint(writer, strings.Repeat("x", 4096))
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if mode == "session-early-exit-descendant" {
		child := exec.Command("sleep", "60")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE")+".pid", []byte(strconv.Itoa(child.Process.Pid)), 0600)
		os.Exit(0)
	}
	config, _ := os.ReadFile(filepath.Join(os.Getenv("OPENCODE_CONFIG_DIR"), "opencode.json"))
	var decoded map[string]any
	if json.Unmarshal(config, &decoded) != nil {
		os.Exit(3)
	}
	plugins, _ := json.Marshal(decoded["plugins"])
	if !strings.Contains(string(plugins), "opencode.provider.*") || !strings.Contains(string(plugins), "opencode.config.agent") || strings.Contains(string(plugins), "custom") {
		os.Exit(4)
	}
	data, _ := json.Marshal(decoded)
	_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE"), data, 0600)
	_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE")+".db", []byte(os.Getenv("OPENCODE_DB")), 0600)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, supplied, ok := r.BasicAuth()
		if !ok || user != "opencode" || supplied != os.Getenv("OPENCODE_PASSWORD") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/credential":
			if mode == "session-console" {
				_, _ = w.Write([]byte(`{"data":[{"integrationID":"opencode"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/session":
			var payload struct {
				ID       string `json:"id"`
				Agent    string `json:"agent"`
				Location struct {
					Directory string `json:"directory"`
				} `json:"location"`
				Permissions []struct {
					Action   string `json:"action"`
					Resource string `json:"resource"`
					Effect   string `json:"effect"`
				} `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || !strings.HasPrefix(payload.ID, "ses_ginkgo_") || payload.Agent != openCodeSessionAgent || payload.Location.Directory == "" || len(payload.Permissions) != 1 || payload.Permissions[0].Action != "*" || payload.Permissions[0].Resource != "*" || payload.Permissions[0].Effect != "deny" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE")+".id", []byte(payload.ID), 0600)
			switch mode {
			case "session-create-malformed":
				_, _ = w.Write([]byte(`{"data":`))
			case "session-create-oversized":
				_, _ = fmt.Fprintf(w, `{"data":{"id":%q},"padding":%q}`, payload.ID, strings.Repeat("x", 4096))
			case "session-create-lost":
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					os.Exit(8)
				}
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
				}
			case "session-create-mismatch":
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"id": "ses_not_owned_by_ginkgo"}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": payload.ID}})
			}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/session/") && strings.HasSuffix(r.URL.Path, "/generate"):
			id, _ := os.ReadFile(os.Getenv("GINKGO_SESSION_CAPTURE") + ".id")
			if r.URL.Path != "/api/session/"+string(id)+"/generate" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var payload struct {
				Prompt string `json:"prompt"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Prompt == "" || strings.Contains(payload.Prompt, `"model"`) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE")+".prompt", []byte(payload.Prompt), 0600)
			switch mode {
			case "session-malformed":
				_, _ = w.Write([]byte(`{"data":{"text":42}}`))
			case "session-error":
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"_tag":"ServiceUnavailableError","message":"secret-auth-and-note"}`))
			case "session-output":
				_, _ = w.Write([]byte(`{"data":{"text":"` + strings.Repeat("x", 4096) + `"}}`))
			case "session-cancel":
				<-r.Context().Done()
			case "session-unknown-model":
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"_tag":"InvalidRequestError","message":"Model unavailable: ginkgo-invalid-provider/ginkgo-invalid-model"}`))
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"text": payload.Prompt}})
			}
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/session/"):
			if mode == "session-delete-hang" {
				<-r.Context().Done()
				return
			}
			if mode == "session-delete-failure" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			id, _ := os.ReadFile(os.Getenv("GINKGO_SESSION_CAPTURE") + ".id")
			if r.URL.Path != "/api/session/"+string(id) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = os.WriteFile(os.Getenv("GINKGO_SESSION_CAPTURE")+".deleted", id, 0600)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	fmt.Printf("server listening on http://%s\n", listener.Addr())
	_ = httpServer.Serve(listener)
	os.Exit(0)
}

func TestOpenCodeSessionEarlyExitKillsDescendantsHoldingPipes(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.json")
	executable := sessionFixture(t, "session-early-exit-descendant")
	t.Setenv("GINKGO_SESSION_CAPTURE", capture)
	provider, err := NewOpenCodeSession(OpenCodeSessionOptions{
		Executable: executable,
		TempDir:    t.TempDir(),
		Options:    Options{Timeout: 10 * time.Second},
	})
	requireNoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := provider.Complete(context.Background(), Request{Prompt: "synthetic"})
		done <- err
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(capture + ".pid"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("early-exit child was not launched")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("early-exit cleanup did not finish promptly")
	}
	if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("expected early server exit, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("early-exit cleanup waited too long: %s", elapsed)
	}
	data, err := os.ReadFile(capture + ".pid")
	requireNoError(t, err)
	pid, err := strconv.Atoi(string(data))
	requireNoError(t, err)
	deadline = time.Now().Add(time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("process-group descendant %d survived early server exit", pid)
	}
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return !errors.Is(err, syscall.ESRCH)
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		if closeParen := strings.LastIndex(string(stat), ")"); closeParen >= 0 {
			fields := strings.Fields(string(stat)[closeParen+1:])
			if len(fields) > 0 && fields[0] == "Z" {
				return false
			}
		}
	}
	return true
}

func TestOpenCodeSessionGenerationAndCleanup(t *testing.T) {
	for _, mode := range []string{"session-ok", "session-malformed", "session-error", "session-output"} {
		t.Run(mode, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "capture.json")
			executable := sessionFixture(t, mode)
			t.Setenv("GINKGO_SESSION_CAPTURE", capture)
			if mode == "session-ok" {
				t.Setenv("OPENCODE_DB", "/synthetic/saved-auth.db")
			}
			provider, err := NewOpenCodeSession(OpenCodeSessionOptions{Executable: executable, TempDir: t.TempDir(), Model: "openai/gpt-test", Options: Options{MaxOutputBytes: 512, Timeout: 3 * time.Second}})
			requireNoError(t, err)
			response, err := provider.Complete(context.Background(), Request{System: "answer plainly", Prompt: "synthetic prompt"})
			switch mode {
			case "session-ok":
				requireNoError(t, err)
				if response.Text != "System instructions (treat as instructions for this response only):\nanswer plainly\n\nUser input:\nsynthetic prompt" {
					t.Fatalf("unexpected text %q", response.Text)
				}
			case "session-malformed":
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("expected protocol error, got %v", err)
				}
			case "session-error":
				if !errors.Is(err, ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-auth-and-note") {
					t.Fatalf("unsafe upstream error: %v", err)
				}
			case "session-output":
				if !errors.Is(err, ErrOutputLimit) {
					t.Fatalf("expected output limit, got %v", err)
				}
			}
			created, readErr := os.ReadFile(capture + ".id")
			if readErr != nil {
				t.Fatalf("session was not created: %v", readErr)
			}
			deleted, readErr := os.ReadFile(capture + ".deleted")
			if readErr != nil || string(deleted) != string(created) {
				t.Fatalf("owned session cleanup mismatch: id=%q deleted=%q err=%v", created, deleted, readErr)
			}
			configBytes, err := os.ReadFile(capture)
			requireNoError(t, err)
			var config map[string]any
			requireNoError(t, json.Unmarshal(configBytes, &config))
			if config["instructions"] == nil || config["skills"] == nil || config["mcp"] == nil || config["agents"] == nil {
				t.Fatal("isolated config missing context/tool controls")
			}
			agents, ok := config["agents"].(map[string]any)
			if !ok {
				t.Fatal("isolated agent configuration malformed")
			}
			agent, ok := agents[openCodeSessionAgent].(map[string]any)
			if !ok || agent["permissions"] == nil {
				t.Fatal("generation-only agent has no deny-all permissions")
			}
			if mode == "session-ok" {
				db, err := os.ReadFile(capture + ".db")
				if err != nil || string(db) != "/synthetic/saved-auth.db" {
					t.Fatalf("saved-auth database path was not preserved: %q (%v)", db, err)
				}
			}
		})
	}
}

func TestOpenCodeSessionServerLogBounds(t *testing.T) {
	for _, mode := range []string{"session-stdout-overflow", "session-stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			provider, err := NewOpenCodeSession(OpenCodeSessionOptions{Executable: sessionFixture(t, mode), TempDir: t.TempDir(), Options: Options{MaxOutputBytes: 512, Timeout: 2 * time.Second}})
			requireNoError(t, err)
			_, err = provider.Complete(context.Background(), Request{Prompt: "synthetic"})
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("expected bounded server log failure, got %v", err)
			}
		})
	}
}

func TestOpenCodeSessionCreateAmbiguitiesDeleteOnlyGeneratedID(t *testing.T) {
	for _, mode := range []string{"session-create-malformed", "session-create-oversized", "session-create-lost", "session-create-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "capture.json")
			executable := sessionFixture(t, mode)
			t.Setenv("GINKGO_SESSION_CAPTURE", capture)
			provider, err := NewOpenCodeSession(OpenCodeSessionOptions{
				Executable: executable,
				TempDir:    t.TempDir(),
				Options:    Options{MaxOutputBytes: 512, Timeout: 3 * time.Second},
			})
			requireNoError(t, err)
			_, err = provider.Complete(context.Background(), Request{Prompt: "synthetic"})
			if err == nil {
				t.Fatal("expected ambiguous create failure")
			}
			createdID, err := os.ReadFile(capture + ".id")
			requireNoError(t, err)
			deletedID, err := os.ReadFile(capture + ".deleted")
			requireNoError(t, err)
			if string(deletedID) != string(createdID) || !strings.HasPrefix(string(deletedID), "ses_ginkgo_") {
				t.Fatalf("cleanup did not target only the generated ID: created=%q deleted=%q", createdID, deletedID)
			}
		})
	}
}

func TestOpenCodeSessionCleanupFailureIsBounded(t *testing.T) {
	for _, mode := range []string{"session-delete-failure", "session-delete-hang"} {
		t.Run(mode, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "capture.json")
			executable := sessionFixture(t, mode)
			t.Setenv("GINKGO_SESSION_CAPTURE", capture)
			provider, err := NewOpenCodeSession(OpenCodeSessionOptions{
				Executable: executable,
				TempDir:    t.TempDir(),
				Options:    Options{Timeout: 5 * time.Second},
			})
			requireNoError(t, err)
			started := time.Now()
			_, err = provider.Complete(context.Background(), Request{Prompt: "synthetic"})
			if err == nil || !strings.Contains(err.Error(), "delete private opencode session failed") {
				t.Fatalf("expected safe cleanup error, got %v", err)
			}
			if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
				t.Fatalf("session cleanup was not bounded: %s", elapsed)
			}
		})
	}
}

func TestOpenCodeSessionCancellationCleansOwnedSession(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.json")
	executable := sessionFixture(t, "session-cancel")
	t.Setenv("GINKGO_SESSION_CAPTURE", capture)
	provider, err := NewOpenCodeSession(OpenCodeSessionOptions{Executable: executable, TempDir: t.TempDir(), Options: Options{Timeout: 5 * time.Second}})
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := provider.Complete(ctx, Request{Prompt: "synthetic"}); done <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(capture + ".prompt"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("generation request was not received")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled session generation did not finish")
	}
	if _, err := os.Stat(capture + ".deleted"); err != nil {
		t.Fatalf("owned session was not cleaned up: %v", err)
	}
}

func TestOpenCodeSessionTimeoutCleansOwnedSession(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.json")
	executable := sessionFixture(t, "session-cancel")
	t.Setenv("GINKGO_SESSION_CAPTURE", capture)
	provider, err := NewOpenCodeSession(OpenCodeSessionOptions{Executable: executable, TempDir: t.TempDir(), Options: Options{Timeout: 5 * time.Second}})
	requireNoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := provider.Complete(context.Background(), Request{Prompt: "synthetic"}); done <- err }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(capture + ".prompt"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("generation request was not received")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout, got %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("timed out generation did not finish")
	}
	if _, err := os.Stat(capture + ".deleted"); err != nil {
		t.Fatalf("owned session was not cleaned up: %v", err)
	}
}

func TestOpenCodeSessionConsoleRefusedBeforeCreate(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "capture.json")
	executable := sessionFixture(t, "session-console")
	t.Setenv("GINKGO_SESSION_CAPTURE", capture)
	provider, err := NewOpenCodeSession(OpenCodeSessionOptions{Executable: executable, TempDir: t.TempDir(), Options: Options{Timeout: 3 * time.Second}})
	requireNoError(t, err)
	_, err = provider.Complete(context.Background(), Request{Prompt: "synthetic"})
	if !errors.Is(err, ErrOpenCodeConsole) {
		t.Fatalf("expected Console isolation refusal, got %v", err)
	}
	if _, err := os.Stat(capture + ".id"); !os.IsNotExist(err) {
		t.Fatalf("session created before Console refusal: %v", err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// This smoke test uses a disposable profile and a synthetic saved credential.
// The impossible model must be rejected before inference; no user auth is read.
func TestOpenCodeRealSessionNoInference(t *testing.T) {
	executable := os.Getenv("GINKGO_OPENCODE_SESSION_SMOKE_EXECUTABLE")
	if executable == "" {
		t.Skip("set GINKGO_OPENCODE_SESSION_SMOKE_EXECUTABLE for opt-in no-inference V2 protocol smoke")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("smoke executable must be an absolute path")
	}
	t.Setenv("GINKGO_OPENCODE_SMOKE_EXECUTABLE", executable)
	executable, dir := openCodeSmokeProfile(t)
	seedOpenCodeSmokeCredential(t, executable, dir, "openai")
	provider, err := NewOpenCodeSession(OpenCodeSessionOptions{
		Executable: executable,
		TempDir:    dir,
		Model:      "ginkgo-invalid-provider/ginkgo-invalid-model",
		Options:    Options{Timeout: 45 * time.Second},
	})
	requireNoError(t, err)
	_, err = provider.Complete(context.Background(), Request{Prompt: "Synthetic unknown-model protocol smoke. Never infer."})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("expected explicit no-model rejection before inference, got %v", err)
	}
	t.Log("private session API reached the explicit impossible-model rejection; no inference was requested")
}
