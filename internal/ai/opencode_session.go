//go:build linux

package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const openCodeSessionAgent = "ginkgo-generation-only"

// OpenCodeSessionOptions configures OpenCode V2's private session-generation
// route. Saved provider auth is read from the ordinary OpenCode profile; the
// request creates and deletes only its own uniquely identified session.
type OpenCodeSessionOptions struct {
	Options
	Executable string
	Model      string
	TempDir    string
	// EphemeralAuth uses a disposable OpenCode data/state profile. Provider API
	// key environment variables remain available.
	EphemeralAuth bool
}

// OpenCodeSessionProvider generates text via a private V2 server and the
// session generation-only API. It never calls session.prompt or CLI run. The
// tool-free snapshot and pre-import plugin controls are verified for stable
// releases >=2.0.25.
// Source checked at v2.0.25 (b44eea9) and v2.0.31 (22728d6):
// packages/core/src/session/generate.ts describes transient, non-mutating text
// generation; packages/core/src/tool.ts filters definitions/catalogs before
// request construction; packages/core/src/instance.ts disables project/global
// config and instruction discovery. The isolated config explicitly enables
// only the built-in agent loader plus model catalogs/providers.
type OpenCodeSessionProvider struct{ options OpenCodeSessionOptions }

// Capabilities reports the session generation endpoint's text-only transport contract.
func (*OpenCodeSessionProvider) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{ImageInput: Unsupported}, nil
}

// NewOpenCodeSession validates options without starting OpenCode or checking login.
func NewOpenCodeSession(options OpenCodeSessionOptions) (*OpenCodeSessionProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	options.Options = limits
	if options.Executable == "" {
		options.Executable = "opencode"
	}
	if options.Model != "" {
		if _, err := openCodeModel(options.Model); err != nil {
			return nil, err
		}
	}
	return &OpenCodeSessionProvider{options: options}, nil
}

func (p *OpenCodeSessionProvider) Complete(parent context.Context, request Request) (response Response, completionErr error) {
	if len(request.System) > p.options.MaxInputBytes || len(request.Prompt) > p.options.MaxInputBytes-len(request.System) {
		return Response{}, ErrInputLimit
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	defer cancel()
	if _, err := os.Lstat("/etc/opencode"); !os.IsNotExist(err) {
		return Response{}, fmt.Errorf("opencode generation isolation requires /etc/opencode to be absent; use an explicitly isolated wrapper instead")
	}
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-opencode-session-")
	if err != nil {
		return Response{}, fmt.Errorf("create opencode session work directory: %w", err)
	}
	defer os.RemoveAll(dir)
	// The session-generation isolation contract is verified starting at 2.0.25,
	// including ephemeral-auth mode; do not extend the range based on API presence.
	if err := checkOpenCodeVersion(ctx, p.options.Executable, dir, true); err != nil {
		return Response{}, err
	}
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		return Response{}, fmt.Errorf("create opencode session config: %w", err)
	}
	if err := writeOpenCodeSessionConfig(configDir); err != nil {
		return Response{}, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Response{}, fmt.Errorf("generate private server credential: %w", err)
	}
	password := hex.EncodeToString(secret)
	env := openCodeEnvironment(configDir, password)
	if p.options.EphemeralAuth {
		env = privateOpenCodeDataEnvironment(env, dir)
	}
	server, err := startOpenCodeSessionServer(ctx, p.options, dir, env)
	if err != nil {
		return Response{}, err
	}
	defer server.close()
	client := privateOpenCodeHTTPClient()
	defer client.CloseIdleConnections()
	if !p.options.EphemeralAuth {
		if err := checkOpenCodeCredentials(ctx, client, server.endpoint, password, p.options.MaxOutputBytes); err != nil {
			return Response{}, err
		}
	}

	sessionID, err := newOpenCodeSessionID()
	if err != nil {
		return Response{}, err
	}
	cleanupSession, err := createOpenCodeSession(ctx, client, server.endpoint, password, dir, sessionID, p.options)
	if cleanupSession {
		// Use an independent short deadline so request cancellation and ambiguous
		// create outcomes still attempt cleanup before the private server exits.
		defer func() {
			if err := deleteOpenCodeSession(server.endpoint, password, sessionID); err != nil && completionErr == nil {
				response = Response{}
				completionErr = fmt.Errorf("delete private opencode session failed")
			}
		}()
	}
	if err != nil {
		return Response{}, err
	}

	prompt := request.Prompt
	if request.System != "" {
		prompt = "System instructions (treat as instructions for this response only):\n" + request.System + "\n\nUser input:\n" + request.Prompt
	}
	input, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return Response{}, fmt.Errorf("encode opencode session generation request: %w", err)
	}
	if len(input) > p.options.MaxInputBytes {
		return Response{}, ErrInputLimit
	}
	result, err := privateOpenCodeRequest(ctx, client, http.MethodPost, server.endpoint+"/api/session/"+url.PathEscape(sessionID)+"/generate", password, input)
	if err != nil {
		if server.logsExceeded() {
			return Response{}, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("opencode session generation transport failed")
	}
	defer result.Body.Close()
	output, err := io.ReadAll(io.LimitReader(result.Body, int64(p.options.MaxOutputBytes)+1))
	if len(output) > p.options.MaxOutputBytes {
		return Response{}, ErrOutputLimit
	}
	if err != nil {
		if server.logsExceeded() {
			return Response{}, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("read opencode session generation failed")
	}
	if server.logsExceeded() {
		return Response{}, ErrOutputLimit
	}
	if result.StatusCode == http.StatusUnauthorized || result.StatusCode == http.StatusForbidden {
		return Response{}, ErrAuthentication
	}
	if result.StatusCode != http.StatusOK {
		return Response{}, classifyOpenCodeSessionFailure(result.StatusCode, output, p.options.Model)
	}
	var generation struct {
		Data *struct {
			Text *string `json:"text"`
		} `json:"data"`
	}
	if decodeObject(output, &generation) != nil || generation.Data == nil || generation.Data.Text == nil {
		return Response{}, ErrProtocol
	}
	if server.logsExceeded() {
		return Response{}, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	return Response{Text: *generation.Data.Text}, nil
}

func writeOpenCodeSessionConfig(configDir string) error {
	// The deny-all rules are applied by Tool.snapshot before model request
	// construction; this is stronger than relying on tool-call permission checks.
	config := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"plugins":      []string{"-*", "opencode.config.agent", "opencode.models.dev", "opencode.provider.*"},
		"instructions": []string{},
		"skills":       []string{},
		"mcp":          map[string]any{"servers": map[string]any{}},
		"agents": map[string]any{openCodeSessionAgent: map[string]any{
			"description": "Generation-only agent with no tools or external context",
			"mode":        "primary",
			"system":      "Generate only the requested text. Do not use tools or external integrations.",
			"permissions": denyAllOpenCodePermissions(),
		}},
		"policies": []map[string]string{
			{"action": "tool.use", "resource": "*", "effect": "deny"},
			{"action": "integration.use", "resource": "plugin:*", "effect": "deny"},
			{"action": "integration.use", "resource": "mcp:*", "effect": "deny"},
			{"action": "integration.use", "resource": "skill:*", "effect": "deny"},
		},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode isolated opencode session config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), data, 0600); err != nil {
		return fmt.Errorf("write isolated opencode session config: %w", err)
	}
	return nil
}

func denyAllOpenCodePermissions() []map[string]string {
	return []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}
}

func privateOpenCodeDataEnvironment(env []string, dir string) []string {
	filtered := make([]string, 0, len(env)+3)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "OPENCODE_DB" || key == "XDG_DATA_HOME" || key == "XDG_STATE_HOME" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "OPENCODE_DB="+filepath.Join(dir, "private.db"), "XDG_DATA_HOME="+filepath.Join(dir, "data"), "XDG_STATE_HOME="+filepath.Join(dir, "state"))
}

type openCodeSessionServer struct {
	endpoint     string
	closeFn      func()
	logsExceeded func() bool
}

func (s *openCodeSessionServer) close() { s.closeFn() }

func startOpenCodeSessionServer(ctx context.Context, options OpenCodeSessionOptions, dir string, env []string) (*openCodeSessionServer, error) {
	ready := make(chan string, 1)
	var pending []byte
	stdout := &limitedBuffer{limit: options.MaxOutputBytes, cancel: func() {}}
	stdout.onWrite = func(chunk []byte) {
		pending = append(pending, chunk...)
		for {
			index := bytes.IndexByte(pending, '\n')
			if index < 0 {
				return
			}
			line := strings.TrimSpace(string(pending[:index]))
			pending = pending[index+1:]
			if endpoint, ok := strings.CutPrefix(line, "server listening on "); ok {
				select {
				case ready <- endpoint:
				default:
				}
			}
		}
	}
	stderr := &limitedBuffer{limit: options.MaxOutputBytes, cancel: func() {}}
	// Keep the private server alive through bounded cleanup even when the
	// request context is cancelled; startup and HTTP work still observe ctx.
	serverCtx, stopServer := context.WithCancel(context.Background())
	stdout.cancel = stopServer
	stderr.cancel = stopServer
	cmd := command(serverCtx, options.Executable, []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}, dir, env, nil)
	// A crashed leader may leave a descendant holding its output pipes. Let
	// Wait unblock promptly so the exited-before-ready path can kill the group.
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		stopServer()
		return nil, fmt.Errorf("start opencode v2 private session server: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	closeFn := func() {
		stopServer()
		_ = killGroup(cmd)
		<-done
	}
	select {
	case endpoint := <-ready:
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			closeFn()
			return nil, fmt.Errorf("%w: invalid private server address", ErrProtocol)
		}
		if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
			closeFn()
			return nil, ErrProtocol
		}
		return &openCodeSessionServer{
			endpoint:     endpoint,
			closeFn:      closeFn,
			logsExceeded: func() bool { return stdout.exceeded() || stderr.exceeded() },
		}, nil
	case <-done:
		_ = killGroup(cmd)
		stopServer()
		if stdout.exceeded() || stderr.exceeded() {
			return nil, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("opencode private session server exited before readiness")
	case <-ctx.Done():
		stopServer()
		_ = killGroup(cmd)
		<-done
		if stdout.exceeded() || stderr.exceeded() {
			return nil, ErrOutputLimit
		}
		return nil, ctx.Err()
	}
}

func privateOpenCodeHTTPClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func privateOpenCodeRequest(ctx context.Context, client *http.Client, method, endpoint, password string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth("opencode", password)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return client.Do(request)
}

func newOpenCodeSessionID() (string, error) {
	rawID := make([]byte, 24)
	if _, err := rand.Read(rawID); err != nil {
		return "", fmt.Errorf("generate private session id: %w", err)
	}
	return "ses_ginkgo_" + hex.EncodeToString(rawID), nil
}

func createOpenCodeSession(ctx context.Context, client *http.Client, endpoint, password, dir, sessionID string, options OpenCodeSessionOptions) (bool, error) {
	var model *modelRef
	if options.Model != "" {
		model, _ = openCodeModel(options.Model)
	}
	payload, err := json.Marshal(map[string]any{
		"id":          sessionID,
		"title":       "GinkGo ephemeral generation",
		"agent":       openCodeSessionAgent,
		"model":       model,
		"location":    map[string]string{"directory": dir},
		"permissions": denyAllOpenCodePermissions(),
	})
	if err != nil {
		return false, fmt.Errorf("encode private opencode session: %w", err)
	}
	result, err := privateOpenCodeRequest(ctx, client, http.MethodPost, endpoint+"/api/session", password, payload)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, fmt.Errorf("create private opencode session failed")
	}
	defer result.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(result.Body, int64(options.MaxOutputBytes)+1))
	if len(data) > options.MaxOutputBytes {
		return result.StatusCode == http.StatusOK || result.StatusCode >= http.StatusInternalServerError, ErrOutputLimit
	}
	var created struct {
		Data *struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &created)
	if result.StatusCode != http.StatusOK {
		return result.StatusCode >= http.StatusInternalServerError, classifyOpenCodeSessionFailure(result.StatusCode, data, options.Model)
	}
	if readErr != nil || created.Data == nil || created.Data.ID != sessionID || !strings.HasPrefix(created.Data.ID, "ses_ginkgo_") {
		return true, ErrProtocol
	}
	return true, nil
}

func deleteOpenCodeSession(endpoint, password, sessionID string) error {
	if !strings.HasPrefix(sessionID, "ses_ginkgo_") {
		return ErrProtocol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := privateOpenCodeHTTPClient()
	defer client.CloseIdleConnections()
	result, err := privateOpenCodeRequest(ctx, client, http.MethodDelete, endpoint+"/api/session/"+url.PathEscape(sessionID), password, nil)
	if err != nil {
		return fmt.Errorf("private session deletion transport failed")
	}
	defer result.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(result.Body, 4096))
	if result.StatusCode != http.StatusNoContent && result.StatusCode != http.StatusNotFound {
		return fmt.Errorf("private session deletion returned HTTP %d", result.StatusCode)
	}
	return nil
}

func classifyOpenCodeSessionFailure(status int, body []byte, model string) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrAuthentication
	}
	var failure struct {
		Tag     string `json:"_tag"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &failure) == nil {
		if status == http.StatusServiceUnavailable && failure.Tag == "ServiceUnavailableError" && strings.HasPrefix(failure.Message, "No model is available for session ses_ginkgo_") {
			return ErrModelUnavailable
		}
		if model != "" {
			ref, _ := openCodeModel(model)
			if ref != nil && failure.Message == "Model unavailable: "+ref.ProviderID+"/"+ref.ID {
				return ErrModelUnavailable
			}
		}
		if failure.Tag == "ServiceUnavailableError" {
			if failure.Message == "Generation credentials are unavailable" {
				return ErrAuthentication
			}
			if strings.HasPrefix(failure.Message, "OpenCode's free tier can only be used from within OpenCode.") {
				return ErrGenerationRestricted
			}
		}
	}
	switch status {
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return ErrProviderUnavailable
	default:
		return fmt.Errorf("opencode session generation returned HTTP %d (verify model, saved login and endpoint compatibility)", status)
	}
}

var _ Provider = (*OpenCodeSessionProvider)(nil)
