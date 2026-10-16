//go:build linux

package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenCodeRealProtocol is explicitly opt-in. It uses a disposable profile,
// not saved credentials, and an impossible model: no real inference is allowed.
// Run: GINKGO_OPENCODE_SMOKE_EXECUTABLE=/absolute/path/opencode go test
// ./internal/ai -run '^TestOpenCodeRealProtocol$' -v -count=1
func TestOpenCodeRealProtocol(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	t.Setenv("OPENAI_API_KEY", "synthetic-not-a-real-api-key")
	provider, err := NewOpenCode(OpenCodeOptions{Executable: executable, Model: "ginkgo-invalid-provider/ginkgo-invalid-model", TempDir: dir, EphemeralAuth: true, Options: Options{Timeout: 45 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), Request{Prompt: "Synthetic protocol smoke. This model must not exist."})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("expected explicit unknown-model rejection after private startup/authentication, got: %v", err)
	}
	t.Log("private V2 startup/authentication and stateless generation protocol passed; nonexistent model rejected before inference")
}

func openCodeSmokeProfile(t *testing.T) (string, string) {
	t.Helper()
	executable := os.Getenv("GINKGO_OPENCODE_SMOKE_EXECUTABLE")
	if executable == "" {
		t.Skip("set GINKGO_OPENCODE_SMOKE_EXECUTABLE to opt into a no-inference real binary smoke")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("smoke executable must be an absolute path")
	}
	dir, err := os.MkdirTemp(".", ".opencode-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, variable := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "TMPDIR"} {
		path := filepath.Join(dir, variable)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(variable, path)
	}
	t.Setenv("OPENCODE_DB", filepath.Join(dir, "private.db"))
	return executable, dir
}

// TestOpenCodeRealSavedAuthProtocol requires >=2.0.25. It creates synthetic
// saved provider credentials via the supported API, never reading actual user
// accounts. This verifies persistence and isolation, not provider login validity.
func TestOpenCodeRealSavedAuthProtocol(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	seedOpenCodeSmokeCredential(t, executable, dir, "openai")
	provider, err := NewOpenCode(OpenCodeOptions{Executable: executable, TempDir: dir, Model: "ginkgo-invalid-provider/ginkgo-invalid-model", Options: Options{Timeout: 45 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), Request{Prompt: "Synthetic saved-credential protocol smoke; never infer."})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("saved provider profile did not reach model rejection: %v", err)
	}
	seedOpenCodeSmokeCredential(t, executable, dir, "opencode")
	_, err = provider.Complete(context.Background(), Request{Prompt: "Console must be rejected before initialization."})
	if err == nil || !strings.Contains(err.Error(), "does not support saved Console credentials") {
		t.Fatalf("Console credential isolation failed: %v", err)
	}
	t.Log("synthetic saved ordinary provider credential retained; synthetic Console credential refused before generation; no inference or real-login verification")
}

func seedOpenCodeSmokeCredential(t *testing.T, executable, dir, integration string) {
	t.Helper()
	configDir := filepath.Join(dir, "seed-config")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(`{"plugins":["-*"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ready := make(chan string, 1)
	stdout := &limitedBuffer{limit: 1 << 20, cancel: cancel}
	var pending []byte
	stdout.onWrite = func(chunk []byte) {
		pending = append(pending, chunk...)
		for {
			index := bytes.IndexByte(pending, '\n')
			if index < 0 {
				return
			}
			line := strings.TrimSpace(string(pending[:index]))
			pending = pending[index+1:]
			if endpoint, ok := strings.CutPrefix(line, "server listening on http://127.0.0.1:"); ok {
				select {
				case ready <- "http://127.0.0.1:" + endpoint:
				default:
				}
			}
		}
	}
	const password = "synthetic-local-smoke-password"
	cmd := command(ctx, executable, []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}, dir, openCodeEnvironment(configDir, password), nil)
	cmd.Stdout = stdout
	cmd.Stderr = &limitedBuffer{limit: 1 << 20, cancel: cancel}
	if err := cmd.Start(); err != nil {
		t.Fatal("start synthetic credential seed server:", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = killGroup(cmd); <-done }()
	var endpoint string
	select {
	case endpoint = <-ready:
	case err := <-done:
		done <- err
		t.Fatal("synthetic credential seed server exited before readiness")
	case <-ctx.Done():
		t.Fatal("synthetic credential seed server readiness timeout")
	}
	// Only the global credential route is used: it does not activate Location
	// plugins, including Console when seeding the rejection case.
	input := `{"integrationID":"` + integration + `","label":"synthetic smoke only","value":{"type":"key","key":"synthetic-not-a-provider-key"}}`
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/credential", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("opencode", password)
	request.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("synthetic credential seed transport failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("synthetic credential seed returned HTTP %d (requires credential API)", response.StatusCode)
	}
}

func TestOpenCodeOldSavedAuthFailsBeforeServerStartup(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-old-saved-auth")})
	_, err := provider.Complete(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "requires version >=2.0.25") {
		t.Fatalf("unsafe older saved-auth version accepted: %v", err)
	}
}

func TestOpenCodeSavedAuthReleaseVersions(t *testing.T) {
	for _, tc := range []struct {
		version   string
		savedAuth bool
		wantError bool
	}{
		{"2.0.25-alpha", true, true},
		{"2.0.25-rc.1", true, true},
		{"2.0.30-rc.1+build.1", true, true},
		{"2.0.25", true, false},
		{"2.0.30+build.1", true, false},
		{"2.0.19", false, false},
		{"2.0.25-alpha", false, false},
	} {
		t.Run(tc.version+fmt.Sprint(tc.savedAuth), func(t *testing.T) {
			executable := fixture(t, "version-only")
			t.Setenv("GINKGO_PROVIDER_TEST_VERSION", "opencode v"+tc.version)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := checkOpenCodeVersion(ctx, executable, t.TempDir(), tc.savedAuth)
			if (err != nil) != tc.wantError {
				t.Fatalf("savedAuth=%v version=%s: unexpected error: %v", tc.savedAuth, tc.version, err)
			}
		})
	}
}

func TestOpenCodeServerLogLimits(t *testing.T) {
	for _, mode := range []string{"server-stdout-overflow", "server-stderr-overflow"} {
		t.Run(mode, func(t *testing.T) {
			provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, mode), Options: Options{MaxOutputBytes: 256, Timeout: 3 * time.Second}})
			_, err := provider.Complete(context.Background(), Request{})
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("expected bounded server log failure, got %v", err)
			}
		})
	}
}

func TestOpenCodeUnknownModel(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-unknown-model"), Model: "ginkgo-invalid-provider/ginkgo-invalid-model"})
	_, err := provider.Complete(context.Background(), Request{})
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestOpenCodeEphemeralAuth(t *testing.T) {
	t.Setenv("OPENCODE_DB", "/must-not-open/saved.db")
	t.Setenv("XDG_DATA_HOME", "/must-not-open/data")
	t.Setenv("XDG_STATE_HOME", "/must-not-open/state")
	t.Setenv("OPENAI_API_KEY", "synthetic-api-key")
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-ephemeral"), EphemeralAuth: true})
	response, err := provider.Complete(context.Background(), Request{})
	if err != nil || response.Text != "isolated" {
		t.Fatalf("ephemeral profile failed: %v", err)
	}
}

func TestOpenCodeMissingCredentialAPIFailsClosed(t *testing.T) {
	provider, _ := NewOpenCode(OpenCodeOptions{Executable: fixture(t, "server-no-credentials-api")})
	_, err := provider.Complete(context.Background(), Request{})
	if err == nil || !strings.Contains(err.Error(), "saved-login isolation requires /api/credential") {
		t.Fatalf("missing credential safety API must fail closed: %v", err)
	}
}
