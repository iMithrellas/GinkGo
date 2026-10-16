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
	"regexp"
	"strconv"
	"strings"
)

// OpenCodeOptions selects an installed OpenCode V2 executable and optional model.
// Model is provider/model or provider/model#variant. Empty uses OpenCode's base
// default (not the user's global configuration, which is intentionally isolated).
// Saved auth remains in the original HOME/XDG_DATA_HOME; no credentials are copied.
type OpenCodeOptions struct {
	Options
	Executable string
	Model      string
	TempDir    string
	// EphemeralAuth uses a disposable OpenCode data/state profile instead of
	// saved logins. Non-Console provider API-key environment variables remain
	// available (for example OPENAI_API_KEY).
	// This supports V2 releases (including 2.0.19) without /api/credential;
	// the default saved-login mode requires >=2.0.25, including pre-import
	// integration policy enforcement and the global credential safety API.
	EphemeralAuth bool
}

// OpenCodeProvider owns a private V2 server for each stateless completion.
// This deliberately never invokes `run`, agent sessions or a shared service.
// Linux process groups terminate the actual inference host on cancellation.
//
// Contract verified against https://opencode.ai/v2/openapi.json and V2 docs
// (cli, config, plugins, policies), plus anomalyco/opencode dev source on
// 2026-10-16 (b0d762154c31f5deca1945068e3bcf7bdfe7f800):
// cli/src/server-process.ts and core/src/generate.ts. Requires V2's
// 2.x semver from `--version`, `serve --hostname --port`,
// OPENCODE_CONFIG_DIR/OPENCODE_DB, private server
// Basic auth and POST /api/experimental/generate. There is no V1 fallback.
// The generation endpoint directly calls LLM.generate with no tools and creates
// no stored session. Only built-in provider/auth and mandatory policy/Console
// plugins and the model catalog remain; user hooks, MCP, skill and agent/config
// plugins are removed. Console credentials and managed system config are refused
// because organization policy can override local integration restrictions.
// Saved-auth mode additionally requires >=2.0.25: earlier releases either lack
// the global credential API or import external plugins before enforcing policy.
type OpenCodeProvider struct{ options OpenCodeOptions }

// NewOpenCode validates options without starting OpenCode or checking login.
func NewOpenCode(options OpenCodeOptions) (*OpenCodeProvider, error) {
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
	return &OpenCodeProvider{options: options}, nil
}

type modelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func openCodeModel(model string) (*modelRef, error) {
	provider, rest, ok := strings.Cut(model, "/")
	id, variant, _ := strings.Cut(rest, "#")
	if !ok || provider == "" || id == "" || strings.ContainsAny(model, "\r\n\t ") || strings.Contains(variant, "#") {
		return nil, fmt.Errorf("opencode model must be provider/model[#variant]")
	}
	return &modelRef{ID: id, ProviderID: provider, Variant: variant}, nil
}

// Complete sends instructions and prompt as text, not shell arguments. V2's
// stateless endpoint has one prompt field, so System is a labeled prefix, not
// a separate provider system role. Model output is returned verbatim.
func (p *OpenCodeProvider) Complete(parent context.Context, request Request) (Response, error) {
	if len(request.System) > p.options.MaxInputBytes || len(request.Prompt) > p.options.MaxInputBytes-len(request.System) {
		return Response{}, ErrInputLimit
	}
	body := struct {
		Prompt string    `json:"prompt"`
		Model  *modelRef `json:"model,omitempty"`
	}{Prompt: request.Prompt}
	if request.System != "" {
		body.Prompt = "System instructions:\n" + request.System + "\n\nUser input:\n" + request.Prompt
	}
	if p.options.Model != "" {
		body.Model, _ = openCodeModel(p.options.Model)
	}
	input, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("encode opencode request: %w", err)
	}
	if len(input) > p.options.MaxInputBytes {
		return Response{}, ErrInputLimit
	}
	if _, err := os.Lstat("/etc/opencode"); !os.IsNotExist(err) {
		return Response{}, fmt.Errorf("opencode generation isolation requires /etc/opencode to be absent; use an explicitly isolated wrapper instead")
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	defer cancel()
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-opencode-")
	if err != nil {
		return Response{}, fmt.Errorf("create opencode work directory: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := checkOpenCodeVersion(ctx, p.options.Executable, dir, !p.options.EphemeralAuth); err != nil {
		return Response{}, err
	}
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0700); err != nil {
		return Response{}, fmt.Errorf("create opencode config: %w", err)
	}
	// Disabling all plugins before restoring only built-in provider plugins is
	// stronger than permissions or an empty MCP map. In particular it removes
	// config hooks and any well-known custom plugin registrations loaded earlier.
	config := `{"$schema":"https://opencode.ai/config.json","plugins":["-*","opencode.models.dev","opencode.provider.*"],"policies":[{"action":"tool.use","resource":"*","effect":"deny"},{"action":"integration.use","resource":"plugin:*","effect":"deny"},{"action":"integration.use","resource":"mcp:*","effect":"deny"},{"action":"integration.use","resource":"skill:*","effect":"deny"}]}`
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(config), 0600); err != nil {
		return Response{}, fmt.Errorf("write isolated opencode config: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Response{}, fmt.Errorf("generate private server credential: %w", err)
	}
	password := hex.EncodeToString(secret)
	env := openCodeEnvironment(configDir, password)
	if p.options.EphemeralAuth {
		// Keep inherited OPENCODE_DB from reopening the user's credential store.
		// A fresh XDG data root also prevents legacy saved-auth migration.
		filtered := make([]string, 0, len(env)+3)
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if key == "OPENCODE_DB" || key == "XDG_DATA_HOME" || key == "XDG_STATE_HOME" {
				continue
			}
			filtered = append(filtered, entry)
		}
		env = append(filtered, "OPENCODE_DB="+filepath.Join(dir, "private.db"), "XDG_DATA_HOME="+filepath.Join(dir, "data"), "XDG_STATE_HOME="+filepath.Join(dir, "state"))
	}
	ready := make(chan string, 1)
	var pending []byte
	stdout := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
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
	stderr := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	cmd := command(ctx, p.options.Executable, []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}, dir, env, nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return Response{}, fmt.Errorf("start opencode v2 private server: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = killGroup(cmd)
		<-done
	}()
	var endpoint string
	select {
	case endpoint = <-ready:
	case err := <-done:
		done <- err // Preserve completion for the cleanup waiter.
		if stdout.exceeded() || stderr.exceeded() {
			return Response{}, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("opencode v2 private server exited before readiness (check installation and version)")
	case <-ctx.Done():
		if stdout.exceeded() || stderr.exceeded() {
			return Response{}, ErrOutputLimit
		}
		return Response{}, ctx.Err()
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Response{}, fmt.Errorf("%w: invalid private server address", ErrProtocol)
	}
	if _, _, err := net.SplitHostPort(parsed.Host); err != nil {
		return Response{}, ErrProtocol
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/experimental/generate", bytes.NewReader(input))
	if err != nil {
		return Response{}, fmt.Errorf("create opencode generation request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.SetBasicAuth("opencode", password)
	// No proxy or redirect may receive the private server credential or note.
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// The global credential endpoint does not activate a Location or its plugins.
	// Check it before generation initializes the mandatory Console plugin. A
	// connected Console can override integration policy and inject remote MCPs;
	// this POC refuses such accounts rather than claiming isolation it cannot give.
	if !p.options.EphemeralAuth {
		if err := checkOpenCodeCredentials(ctx, client, endpoint, password, p.options.MaxOutputBytes); err != nil {
			return Response{}, err
		}
	}
	result, err := client.Do(httpRequest)
	if err != nil {
		if stdout.exceeded() || stderr.exceeded() {
			return Response{}, ErrOutputLimit
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("opencode generation transport failed: %w", err)
	}
	defer result.Body.Close()
	output, err := io.ReadAll(io.LimitReader(result.Body, int64(p.options.MaxOutputBytes)+1))
	if len(output) > p.options.MaxOutputBytes {
		return Response{}, ErrOutputLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("read opencode generation: %w", err)
	}
	if result.StatusCode == 401 || result.StatusCode == 403 {
		return Response{}, ErrAuthentication
	}
	if result.StatusCode != http.StatusOK {
		// Never return upstream messages: they may contain prompts, credentials or
		// response bodies. Classify only known failures into fixed diagnostics.
		var failure struct {
			Tag     string `json:"_tag"`
			Message string `json:"message"`
		}
		if result.StatusCode == http.StatusServiceUnavailable && json.Unmarshal(output, &failure) == nil && failure.Tag == "ServiceUnavailableError" {
			// Observed with 2.0.31's stateless endpoint and the public free model.
			// Do not add tools, agent sessions or spoof attribution to evade this
			// upstream restriction. The generation-only contract remains intact.
			if strings.HasPrefix(failure.Message, "OpenCode's free tier can only be used from within OpenCode.") {
				return Response{}, ErrGenerationRestricted
			}
			if failure.Message == "Generation credentials are unavailable" {
				return Response{}, ErrAuthentication
			}
		}
		if result.StatusCode == http.StatusBadRequest && body.Model != nil {
			var rejection struct {
				Tag     string `json:"_tag"`
				Message string `json:"message"`
			}
			if json.Unmarshal(output, &rejection) == nil && rejection.Tag == "InvalidRequestError" && rejection.Message == "Model unavailable: "+body.Model.ProviderID+"/"+body.Model.ID {
				return Response{}, ErrModelUnavailable
			}
		}
		switch result.StatusCode {
		case http.StatusTooManyRequests:
			return Response{}, ErrRateLimited
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return Response{}, ErrProviderUnavailable
		}
		return Response{}, fmt.Errorf("opencode v2 generation returned HTTP %d (verify model, saved login and endpoint compatibility)", result.StatusCode)
	}
	var response struct {
		Data *struct {
			Text *string `json:"text"`
		} `json:"data"`
	}
	if err := decodeObject(output, &response); err != nil || response.Data == nil || response.Data.Text == nil {
		return Response{}, ErrProtocol
	}
	if stdout.exceeded() || stderr.exceeded() {
		return Response{}, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	return Response{Text: *response.Data.Text}, nil
}

func checkOpenCodeVersion(parent context.Context, executable, dir string, savedAuth bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stdout := &limitedBuffer{limit: 4096, cancel: cancel}
	stderr := &limitedBuffer{limit: 4096, cancel: cancel}
	cmd := command(ctx, executable, []string{"--version"}, dir, openCodeEnvironment(dir, ""), nil)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	_ = killGroup(cmd)
	if stdout.exceeded() || stderr.exceeded() {
		return ErrOutputLimit
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if err != nil {
		return fmt.Errorf("check opencode version: %w", err)
	}
	// Never start a V1 server: V1 configuration/isolation semantics differ even
	// when command names happen to overlap. Unknown dev version formats fail closed.
	version := regexp.MustCompile(`^(?:opencode v)?2\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`).FindStringSubmatch(strings.TrimSpace(string(stdout.bytes())))
	if version == nil {
		return fmt.Errorf("opencode generation requires a V2 executable reporting a 2.x semantic version")
	}
	minor, minorErr := strconv.Atoi(version[1])
	patch, patchErr := strconv.Atoi(version[2])
	if minorErr != nil || patchErr != nil {
		return ErrProtocol
	}
	// Prereleases are not evidence that the released isolation guarantees are
	// present, even when their numeric version exceeds the minimum.
	if savedAuth && version[3] != "" {
		return fmt.Errorf("opencode saved-login isolation requires a stable release >=2.0.25; prerelease builds are unsupported")
	}
	// The credential API appeared in 2.0.20, but 2.0.20–24 can import
	// well-known plugins before applying removals: plugin:* policy only gates
	// module imports starting in 2.0.25 (verified against the release tags).
	if savedAuth && minor == 0 && patch < 25 {
		return fmt.Errorf("opencode saved-login/session isolation requires version >=2.0.25; upgrade OpenCode")
	}
	return nil
}

func checkOpenCodeCredentials(ctx context.Context, client *http.Client, endpoint, password string, limit int) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/credential", nil)
	if err != nil {
		return ErrProtocol
	}
	request.SetBasicAuth("opencode", password)
	result, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("opencode credential isolation check failed")
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, int64(limit)+1))
	if len(body) > limit {
		return ErrOutputLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read opencode credential isolation check failed")
	}
	if result.StatusCode == 401 || result.StatusCode == 403 {
		return ErrAuthentication
	}
	if result.StatusCode != http.StatusOK {
		if result.StatusCode == http.StatusNotFound {
			return fmt.Errorf("opencode saved-login isolation requires /api/credential; upgrade OpenCode or explicitly use EphemeralAuth with provider API-key environment variables")
		}
		return fmt.Errorf("opencode credential isolation check returned HTTP %d", result.StatusCode)
	}
	var response struct {
		Data *[]struct {
			IntegrationID string `json:"integrationID"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Data == nil {
		return ErrProtocol
	}
	for _, credential := range *response.Data {
		if credential.IntegrationID == "" {
			return ErrProtocol
		}
		if credential.IntegrationID == "opencode" {
			return ErrOpenCodeConsole
		}
	}
	return nil
}

func openCodeEnvironment(configDir, password string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		// Keep saved provider auth (HOME/XDG_DATA_HOME) and provider API-key envs,
		// but never inherit OpenCode service/config/simulation overrides.
		if strings.HasPrefix(key, "OPENCODE_") && key != "OPENCODE_DB" {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"OPENCODE_CONFIG_DIR="+configDir,
		"OPENCODE_CONFIG_PROJECT_DISABLE=true",
		"OPENCODE_DISABLE_CLAUDE_CODE=true",
		"OPENCODE_FILEWATCHER_DISABLE=true",
		"OPENCODE_PASSWORD="+password,
	)
}
