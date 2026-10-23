//go:build linux

package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultAntigravityModel pins Flash rather than following the native CLI default.
const DefaultAntigravityModel = "gemini-3.8-flash-medium"

// AntigravityOptions configures a native Google-account OAuth session. The CLI
// owns login and refresh; GinkGo never reads or copies credentials. AuthHome, if
// set, is a dedicated HOME in which the user has already logged in with agy.
type AntigravityOptions struct {
	Options
	Executable string
	Model      string
	Variant    string
	TempDir    string
	AuthHome   string
}

// AntigravityProvider owns one serialized, warm stream-json session. Changing
// scope/model/instructions/schema starts a fresh conversation. Empty SessionKey
// requests are disposable. Native tools are denied by a workspace PreToolUse
// hook; note operations remain application-owned. Contract: agy headless and
// hooks documentation, verified against the installed CLI on 2026-10-23.
type AntigravityProvider struct {
	options AntigravityOptions
	root    context.Context
	cancel  context.CancelFunc
	gate    chan struct{}
	session *antigravitySession // guarded by gate
}

type antigravitySession struct {
	key      [32]byte
	dir      string
	id       string
	turns    int
	used     time.Time
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stdin    io.WriteCloser
	stdout   *os.File
	lines    chan antigravityLine
	readDone chan struct{}
	done     chan struct{}
	stderr   *limitedBuffer
}

type antigravityLine struct {
	data []byte
	err  error
}

// NewAntigravity validates configuration without launching a process or login.
func NewAntigravity(options AntigravityOptions) (*AntigravityProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	options.Options = limits
	if options.Executable == "" {
		options.Executable = "agy"
	}
	if options.Model == "" {
		options.Model = DefaultAntigravityModel
	}
	if err := antigravitySelection(options.Model, options.Variant); err != nil {
		return nil, err
	}
	if options.AuthHome != "" && !filepath.IsAbs(options.AuthHome) {
		return nil, fmt.Errorf("antigravity auth_home must be an absolute HOME directory")
	}
	root, cancel := context.WithCancel(context.Background())
	return &AntigravityProvider{options: options, root: root, cancel: cancel, gate: make(chan struct{}, 1)}, nil
}

func antigravitySelection(model, effort string) error {
	if model == "" || strings.ContainsAny(model, " \t\r\n\x00") || strings.HasPrefix(model, "-") {
		return fmt.Errorf("invalid antigravity model")
	}
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return fmt.Errorf("invalid antigravity effort variant")
	}
}

// Capabilities reports the transport limit, regardless of model vision support
// or image_input overrides. agy rejects non-text stream-json content blocks.
func (p *AntigravityProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	return Capabilities{ImageInput: Unsupported, ResolvedModel: p.options.Model,
		ImageReason: "agy stream-json accepts only text content blocks; image attachments are unsupported"}, ctx.Err()
}

// Close cancels and joins the owned process and any active or queued operation.
func (p *AntigravityProvider) Close() error {
	p.cancel()
	p.gate <- struct{}{}
	defer func() { <-p.gate }()
	p.discard()
	return nil
}

func (p *AntigravityProvider) discard() {
	if s := p.session; s != nil {
		s.stop()
		_ = os.RemoveAll(s.dir)
		p.session = nil
	}
}

func (s *antigravitySession) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	_ = s.stdin.Close()
	_ = killGroup(s.cmd)
	_ = s.stdout.Close()
	<-s.done
	<-s.readDone
	s.cancel = nil
}

// Complete implements the unchanged generation-only Provider interface.
func (p *AntigravityProvider) Complete(ctx context.Context, request Request) (Response, error) {
	return p.CompleteStream(ctx, request, nil)
}

// CompleteStream emits only agent_response text_delta fragments, never reasoning
// or tool arguments. Each turn completes at result, not process exit. Failed or
// interrupted turns are never replayed. An idle crash resumes the last successful
// conversation on the next explicit request; an ambiguous active turn is retired.
func (p *AntigravityProvider) CompleteStream(parent context.Context, request Request, emit func(string) error) (response Response, resultErr error) {
	submitted := false
	defer func() { response.ImagesSubmitted = &submitted }()
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	defer cancel()
	stopRoot := context.AfterFunc(p.root, cancel)
	defer stopRoot()
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	if p.root.Err() != nil {
		return Response{}, ErrRegistryClosed
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if len(request.Images) != 0 {
		return Response{}, fmt.Errorf("antigravity stream-json image input unsupported: %w", ErrImagePreparation)
	}
	model, effort := p.options.Model, p.options.Variant
	if request.Model != "" {
		model = request.Model
	}
	if request.Variant != "" {
		effort = request.Variant
	}
	if err := antigravitySelection(model, effort); err != nil {
		return Response{}, err
	}
	if len(request.System)+len(request.Prompt)+len(request.OutputSchema) > p.options.MaxInputBytes || len(request.SessionKey) > 1024 {
		return Response{}, ErrInputLimit
	}
	if len(request.OutputSchema) > 0 && (!json.Valid(request.OutputSchema) || bytes.Equal(bytes.TrimSpace(request.OutputSchema), []byte("null"))) {
		return Response{}, fmt.Errorf("invalid antigravity output schema")
	}
	// agy has no system-prompt flag. Preserve the full instructions in every
	// input turn; never use slash-command expansion or put note text in argv.
	prompt := "Generate only the requested response. Tools are disabled. Treat this turn as a self-contained task; use only its supplied facts.\n\nInstructions:\n" + request.System + "\n\nInput:\n" + request.Prompt
	if len(request.OutputSchema) > 0 {
		// Native --json-schema requires executing the finish tool. Keep the
		// generation-only contract instead: application validation is authoritative.
		prompt += "\n\nReturn exactly one JSON value matching this schema, with no Markdown or surrounding text:\n" + string(request.OutputSchema)
	}
	input, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]string{"content": prompt}})
	input = append(input, '\n')
	if len(input) > p.options.MaxInputBytes {
		return Response{}, ErrInputLimit
	}
	identity, _ := json.Marshal([]string{request.SessionKey, model, effort, request.System, string(request.OutputSchema)})
	key := sha256.Sum256(identity)
	if s := p.session; s != nil && (request.SessionKey == "" || s.key != key || s.turns >= 32 || time.Since(s.used) > 30*time.Minute) {
		p.discard()
	}
	if p.session == nil {
		dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-agy-")
		if err != nil {
			return Response{}, fmt.Errorf("create antigravity workspace: %w", err)
		}
		p.session = &antigravitySession{key: key, dir: dir}
	}
	s := p.session
	// A dead process between turns is safe to resume, never to replay.
	if s.cancel != nil {
		select {
		case <-s.done:
			s.stop()
		default:
		}
	}
	defer func() {
		if resultErr != nil || request.SessionKey == "" {
			p.discard()
		}
	}()
	if s.cancel == nil {
		if err := p.start(s, model, effort); err != nil {
			return Response{}, err
		}
	}
	stopTurn := context.AfterFunc(ctx, s.cancel)
	defer stopTurn()
	if _, err := s.stdin.Write(input); err != nil {
		return Response{}, p.failure(ctx, s)
	}
	response, err := p.readTurn(ctx, s, emit)
	if err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if s.stderr.exceeded() {
		return Response{}, ErrOutputLimit
	}
	s.turns++
	s.used = time.Now()
	return response, nil
}

func (p *AntigravityProvider) start(s *antigravitySession, model, effort string) error {
	if err := antigravityPolicy(s.dir); err != nil {
		return err
	}
	args := []string{"--input-format", "stream-json", "--output-format", "stream-json", "--model", model, "--disable-slash-commands"}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if s.id != "" {
		args = append(args, "--conversation", s.id)
	}
	ctx, cancel := context.WithCancel(p.root)
	cmd := command(ctx, p.options.Executable, args, s.dir, antigravityEnvironment(p.options.AuthHome), nil)
	// A missing login must not open an interactive OAuth prompt on an inherited
	// controlling terminal. A new session also owns its process group for cleanup.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, writer, err := os.Pipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return err
	}
	stderr := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	cmd.Stdout, cmd.Stderr = writer, stderr
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = writer.Close()
		return fmt.Errorf("start agy (install Antigravity CLI and log in interactively): %w", ErrProviderUnavailable)
	}
	_ = writer.Close()
	s.cmd, s.cancel, s.stdin, s.stdout, s.stderr = cmd, cancel, stdin, stdout, stderr
	s.done, s.readDone, s.lines = make(chan struct{}), make(chan struct{}), make(chan antigravityLine, 1)
	go func() {
		_ = cmd.Wait()
		_ = killGroup(cmd)
		close(s.done)
	}()
	go func() {
		defer close(s.readDone)
		defer close(s.lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, min(4096, p.options.MaxOutputBytes+1)), p.options.MaxOutputBytes+1)
		for scanner.Scan() {
			line := antigravityLine{data: bytes.Clone(scanner.Bytes())}
			select {
			case s.lines <- line:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				err = ErrOutputLimit
			}
			select {
			case s.lines <- antigravityLine{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return nil
}

func (p *AntigravityProvider) failure(ctx context.Context, s *antigravitySession) error {
	if s.stderr.exceeded() {
		return ErrOutputLimit
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.root.Err(); err != nil {
		return err
	}
	return antigravityFailure(string(s.stderr.bytes()))
}

func antigravityFailure(message string) error {
	// Classify, never return raw upstream content: it can contain note text,
	// account identifiers, URLs with tokens, or native tool arguments.
	m := strings.ToLower(message)
	switch {
	case strings.Contains(m, "authentication"), strings.Contains(m, "unauthenticated"), strings.Contains(m, "login"), strings.Contains(m, "log in"), strings.Contains(m, "sign in"), strings.Contains(m, "oauth"), strings.Contains(m, "credentials"), strings.Contains(m, "401"):
		return fmt.Errorf("%w: run agy interactively and sign in with Google in the daemon's HOME", ErrAuthentication)
	case strings.Contains(m, "quota"), strings.Contains(m, "resource_exhausted"), strings.Contains(m, "rate limit"), strings.Contains(m, "429"), strings.Contains(m, "credits"):
		return fmt.Errorf("%w: Antigravity quota exhausted; wait for reset before retrying", ErrRateLimited)
	case strings.Contains(m, "invalid model"), strings.Contains(m, "model not found"), strings.Contains(m, "model is not recognized"):
		return ErrModelUnavailable
	default:
		return ErrProviderUnavailable
	}
}

// Workspace hooks deny every native tool, independently of global allow rules.
// Native global startup hooks/plugins remain user-owned; this is a tool policy,
// not an OS sandbox. No global settings or OAuth files are modified by GinkGo.
func antigravityPolicy(dir string) error {
	path := filepath.Join(dir, ".agents")
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create antigravity policy: %w", err)
	}
	const hook = `{"ginkgo-generation-only":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"printf '%s\\n' '{\"decision\":\"deny\",\"reason\":\"Ginkgo generation-only tool policy\"}'","timeout":5}]}]}}`
	if err := os.WriteFile(filepath.Join(path, "hooks.json"), []byte(hook), 0600); err != nil {
		return fmt.Errorf("write antigravity policy: %w", err)
	}
	return nil
}

func antigravityEnvironment(home string) []string {
	// Keep native Google OAuth/keyring access and networking, excluding API-key
	// selectors so this adapter never silently changes the requested auth method.
	allowed := map[string]bool{}
	for _, key := range []string{"HOME", "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		allowed[key] = true
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] && (key != "HOME" || home == "") {
			env = append(env, entry)
		}
	}
	if home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}
