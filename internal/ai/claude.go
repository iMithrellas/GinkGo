//go:build linux

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
	"golang.org/x/sys/unix"
)

// ClaudeCodeOptions configures the native Claude Code stream-json transport.
// Variant is the native effort level. Model accepts exact IDs or CLI aliases.
// ImageInput must be explicitly supported: supportedModels has no vision field.
type ClaudeCodeOptions struct {
	Options
	Executable string
	Model      string
	Variant    string
	TempDir    string
}

// ErrClaudeCodeIsolation indicates a conflicting policy or unsupported credential
// selector, not an unsupported subscription. Never fall back to another identity.
var ErrClaudeCodeIsolation = fmt.Errorf("%w: claude-code generation-only controls conflict with native policy or credential selection", ErrIsolationUnavailable)

// ClaudeCodeProvider runs a disposable process per request, never a conversation
// shared with another request. Close cancels and reaps in-flight processes.
// Contract: Claude Code 2.1.299; code.claude.com/docs/en/{headless,cli-reference,
// agent-sdk/streaming-vs-single-mode,agent-sdk/typescript,managed-settings,
// server-managed-settings,settings,env-vars}. Safe mode preserves saved OAuth,
// unlike bare mode. Managed policy still applies and can execute startup hooks:
// model-tool conflicts are rejected, but policy startup effects are native-owned.
// This is a model tool boundary, NOT a sandbox or a zero-startup-effects promise.
// User customizations are disabled, not built-in plugin metadata or native
// policy hooks. The audited CLI reports built-in agents and policy-dependent
// built-in plugins even with an empty model toolset.
type ClaudeCodeProvider struct {
	options         ClaudeCodeOptions
	mu              sync.Mutex
	closed          bool
	next            uint64
	active          map[uint64]context.CancelFunc
	wg              sync.WaitGroup
	probeGate       chan struct{}
	processGate     chan struct{}
	generationGate  chan struct{}
	versionUntil    time.Time
	versionErr      error
	probeUntil      time.Time
	probeErr        error
	models          []claudeModel
	cacheExecutable claudeExecutable
	cacheIdentity   [sha256.Size]byte
}

// Cache identity includes Linux change time so same-size in-place rewrites with
// restored mtime still invalidate the audit. Resolve PATH and all symlinks once
// per operation, then execute that absolute path (never re-resolve in exec).
type claudeExecutable struct {
	Path              string
	Device, Inode     uint64
	Size              int64
	Mode              os.FileMode
	Modified, Changed syscall.Timespec
}

type claudeExecutableKey struct{}

func (p *ClaudeCodeProvider) resolveExecutable(ctx context.Context) (claudeExecutable, error) {
	if err := ctx.Err(); err != nil {
		return claudeExecutable{}, err
	}
	path, err := exec.LookPath(p.options.Executable)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return claudeExecutable{}, fmt.Errorf("%w: cannot resolve claude-code executable", ErrIsolationUnavailable)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return claudeExecutable{}, fmt.Errorf("%w: cannot identify claude-code executable", ErrIsolationUnavailable)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return claudeExecutable{}, ErrIsolationUnavailable
	}
	return claudeExecutable{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Size: info.Size(), Mode: info.Mode(), Modified: stat.Mtim, Changed: stat.Ctim}, nil
}

func (p *ClaudeCodeProvider) pinExecutable(ctx context.Context) (context.Context, error) {
	if _, ok := ctx.Value(claudeExecutableKey{}).(claudeExecutable); ok {
		return ctx, nil
	}
	executable, err := p.resolveExecutable(ctx)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, claudeExecutableKey{}, executable), nil
}

type claudeModel struct {
	Value         string `json:"value"`
	ResolvedModel string `json:"resolvedModel"`
}

// NewClaudeCode validates options without launching Claude Code or reading auth.
// Authentication is later discovered exclusively through native auth status.
func NewClaudeCode(options ClaudeCodeOptions) (*ClaudeCodeProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	options.Options = limits
	if options.Executable == "" {
		options.Executable = "claude"
	}
	if err := claudeSelection(options.Model, options.Variant); err != nil {
		return nil, err
	}
	return &ClaudeCodeProvider{options: options, active: make(map[uint64]context.CancelFunc), probeGate: make(chan struct{}, 1), processGate: make(chan struct{}, 1), generationGate: make(chan struct{}, 1)}, nil
}

func claudeSelection(model, variant string) error {
	if strings.ContainsAny(model, " \t\r\n\x00") || strings.HasPrefix(model, "-") {
		return fmt.Errorf("invalid claude-code model")
	}
	switch variant {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("invalid claude-code effort variant")
	}
	return nil
}

func (p *ClaudeCodeProvider) begin(parent context.Context) (context.Context, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, nil, errors.New("claude-code provider is closed")
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	p.next++
	id := p.next
	p.active[id] = cancel
	p.wg.Add(1)
	return ctx, func() { cancel(); p.mu.Lock(); delete(p.active, id); p.mu.Unlock(); p.wg.Done() }, nil
}

// Close prevents new operations, cancels queued and active requests/probes, and
// waits until their request-owned process groups and temporary files are reaped.
// It is safe to call repeatedly or concurrently with Complete and Capabilities.
func (p *ClaudeCodeProvider) Close() error {
	p.mu.Lock()
	p.closed = true
	for _, cancel := range p.active {
		cancel()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return nil
}

// claudeEnvironment is an allowlist, not a list of known dangerous toggles.
// Keep the original credential location; do not copy, export or translate OAuth.
func claudeEnvironment(dir string) []string {
	allowed := map[string]bool{}
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"} {
		allowed[key] = true
	}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	return append(env, "TMPDIR="+dir, "CLAUDE_CODE_TMPDIR="+dir, "CLAUDE_CODE_SAFE_MODE=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "CLAUDE_CODE_AUTO_CONNECT_IDE=false", "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1", "CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS=1", "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1", "CLAUDE_CODE_DISABLE_ARTIFACT=1", "CLAUDE_CODE_DISABLE_CRON=1")
}

func claudeConfigDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// Check local policy BEFORE launching even auth status. No credential store is
// opened. Native status, unlike general initialize, does not start a session.
func claudeLocalIsolation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Unsupported credential selectors must be refused, not silently stripped
	// followed by a fallback to a different saved subscription. Active profiles
	// discovered by native status must likewise match the selected credential.
	for _, key := range []string{"ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"} {
		if os.Getenv(key) != "" {
			return fmt.Errorf("%w: unsupported credential selector %s; configure authentication in the native CLI", ErrClaudeCodeIsolation, key)
		}
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CLAUDE_CODE_USE_") && value != "" {
			return fmt.Errorf("%w: unsupported provider selector %s", ErrClaudeCodeIsolation, key)
		}
	}
	inspection := &claudePolicyInspection{ctx: ctx}
	if err := inspection.endpoint("/etc/claude-code"); err != nil {
		return err
	}
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); !os.IsNotExist(err) {
		return fmt.Errorf("%w: inherited Windows managed policy is not audited", ErrClaudeCodeIsolation)
	}
	dir, err := claudeConfigDir()
	if err != nil {
		return ErrClaudeCodeIsolation
	}
	return inspection.file(filepath.Join(dir, "remote-settings.json"))
}

const (
	claudePolicyMaxFileBytes = 2 << 20
	claudePolicyMaxBytes     = 4 << 20
	claudePolicyMaxFiles     = 64
	claudePolicyMaxEntries   = 256
)

type claudePolicyInspection struct {
	ctx     context.Context
	files   int
	entries int
	bytes   int
}

func claudePolicyError(reason string) error {
	return fmt.Errorf("%w: managed settings %s; ask the administrator for a compatible generation-only policy", ErrClaudeCodeIsolation, reason)
}

// Walk from a root descriptor, never through symlinked parent directories.
// The selected native profile is not relocated or rewritten on refusal. These
// are bounded regular-file operations with cancellation checkpoints, not a
// guarantee that the kernel can interrupt every individual filesystem syscall.
func claudePolicyDirectory(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, claudePolicyError("cannot be safely inspected")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, claudePolicyError("directory cannot be inspected")
	}
	for _, name := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if name == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			unix.Close(fd)
			return nil, err
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			if canceled := ctx.Err(); canceled != nil {
				return nil, canceled
			}
			if err == unix.ENOENT {
				return nil, nil
			}
			return nil, claudePolicyError("directory cannot be safely inspected")
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), abs), nil
}

func claudeEndpointPolicy(ctx context.Context, dir string) error {
	return (&claudePolicyInspection{ctx: ctx}).endpoint(dir)
}

func (p *claudePolicyInspection) endpoint(dir string) error {
	if err := p.file(filepath.Join(dir, "managed-settings.json")); err != nil {
		return err
	}
	dropins, err := claudePolicyDirectory(p.ctx, filepath.Join(dir, "managed-settings.d"))
	if err != nil {
		return err
	}
	if dropins != nil {
		defer dropins.Close()
		for {
			if err := p.ctx.Err(); err != nil {
				return err
			}
			entries, err := dropins.ReadDir(1)
			if canceled := p.ctx.Err(); canceled != nil {
				return canceled
			}
			if err != nil && err != io.EOF {
				return claudePolicyError("directory cannot be read")
			}
			if len(entries) == 0 {
				break
			}
			p.entries++
			if p.entries > claudePolicyMaxEntries {
				return claudePolicyError("exceed directory entry inspection budget")
			}
			entry := entries[0]
			// Match native non-dot .json file/symlink drop-ins, not hidden
			// credential stores, subdirectories, sockets, or other file types.
			if strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") || (!entry.Type().IsRegular() && entry.Type()&os.ModeSymlink == 0) {
				continue
			}
			if err := p.fileAt(dropins, entry.Name()); err != nil {
				return err
			}
		}
	}
	// The system MCP file can supersede --strict-mcp-config. Do not start its
	// servers just to discover whether they expose a tool.
	return p.file(filepath.Join(dir, "managed-mcp.json"))
}

// Inspect model-tool policy, never credentials. Managed hooks/helpers/env are
// native-owned startup behavior, not model-callable action tools. Do not delete
// the cache or suppress provider policy delivery. Fresh policy may arrive AFTER
// this check; response validation cannot undo any native startup effects.
func claudePolicyFile(ctx context.Context, path string) error {
	return (&claudePolicyInspection{ctx: ctx}).file(path)
}

func (p *claudePolicyInspection) file(path string) error {
	dir, err := claudePolicyDirectory(p.ctx, filepath.Dir(path))
	if err != nil || dir == nil {
		return err
	}
	defer dir.Close()
	return p.fileAt(dir, filepath.Base(path))
}

func (p *claudePolicyInspection) fileAt(dir *os.File, name string) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	var before unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW)
	if canceled := p.ctx.Err(); canceled != nil {
		return canceled
	}
	if err == unix.ENOENT {
		return nil
	}
	// Reject hardlink aliases BEFORE opening/reading, and recheck the descriptor
	// before reading in case the directory entry was replaced between syscalls.
	if err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > claudePolicyMaxFileBytes {
		return claudePolicyError("cannot be safely inspected")
	}
	p.files++
	if p.files > claudePolicyMaxFiles || before.Size > int64(claudePolicyMaxBytes-p.bytes) {
		return claudePolicyError("exceed file or aggregate byte inspection budget")
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return claudePolicyError("cannot be read")
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil || current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 1 || current.Dev != before.Dev || current.Ino != before.Ino {
		return claudePolicyError("changed during inspection")
	}
	limit := min(claudePolicyMaxFileBytes, claudePolicyMaxBytes-p.bytes)
	var data bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(chunk[:min(len(chunk), limit-data.Len()+1)])
		p.bytes += n
		if data.Len()+n > limit {
			return claudePolicyError("exceed file or aggregate byte inspection budget")
		}
		data.Write(chunk[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return claudePolicyError("cannot be read")
		}
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	var policy map[string]json.RawMessage
	if json.Unmarshal(data.Bytes(), &policy) != nil || policy == nil {
		return claudePolicyError("are invalid")
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	for key, raw := range policy {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		// Native managed MCP can supersede the explicit empty configuration. Other
		// managed settings remain authoritative, including shell startup hooks.
		switch key {
		case "managedMcpServers", "mcpServers":
			value := strings.TrimSpace(string(raw))
			if value != "null" && value != "{}" && value != "[]" && value != `""` {
				return claudePolicyError("contain incompatible " + key)
			}
		}
	}
	return nil
}

type claudeAuthStatus struct {
	LoggedIn        bool   `json:"loggedIn"`
	AuthMethod      string `json:"authMethod"`
	APIProvider     string `json:"apiProvider"`
	APIKeySource    string `json:"apiKeySource"`
	ConfigDirectory string `json:"configDirectory"`
}

// authStatus invokes only the documented native auth subcommand, never opens,
// copies or decodes a credential store. This exact invocation was checked on
// 2.1.299 with hook and apiKeyHelper tripwires; neither executed. Official hook
// lifecycle docs limit SessionStart/End to sessions. The server-managed-settings
// docs exempt auth subcommands from fail-closed remote startup/gateway checks.
// https://code.claude.com/docs/en/{cli-reference,hooks,server-managed-settings}
// Never echo raw status output. A digest of native account metadata and the
// selected environment invalidates model discovery on account/profile changes;
// no token file is opened and native refresh remains entirely CLI-owned.
func (p *ClaudeCodeProvider) authStatus(ctx context.Context) error {
	data, err := p.run(ctx, []string{"--safe-mode", "--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "auth", "status", "--json"}, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrOutputLimit) {
			return err
		}
		return ErrAuthentication
	}
	var status claudeAuthStatus
	if json.Unmarshal(data, &status) != nil {
		return ErrProtocol
	}
	if !status.LoggedIn {
		return ErrAuthentication
	}
	dir, err := claudeConfigDir()
	if err != nil || !filepath.IsAbs(status.ConfigDirectory) || filepath.Clean(status.ConfigDirectory) != filepath.Clean(dir) {
		return ErrClaudeCodeIsolation
	}
	// A saved gateway or external profile must not supersede the intended native
	// credential. Subscription type is informational, never an authorization gate.
	if status.APIProvider != "firstParty" {
		return ErrClaudeCodeIsolation
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		endpoint, err := url.Parse(base)
		if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Hostname() == "" || endpoint.User != nil {
			return fmt.Errorf("%w: invalid native API endpoint", ErrClaudeCodeIsolation)
		}
		official := endpoint.Scheme == "https" && strings.EqualFold(endpoint.Hostname(), "api.anthropic.com") && (endpoint.Port() == "" || endpoint.Port() == "443") && (endpoint.Path == "" || endpoint.Path == "/") && endpoint.RawQuery == "" && endpoint.Fragment == ""
		if !official && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
			return fmt.Errorf("%w: a custom endpoint requires an explicit native environment credential; saved subscription credentials are not redirected", ErrClaudeCodeIsolation)
		}
	}
	expected := "claude.ai"
	switch {
	case os.Getenv("ANTHROPIC_AUTH_TOKEN") != "":
		expected = "oauth_token"
	case os.Getenv("ANTHROPIC_API_KEY") != "":
		expected = "api_key"
	case os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "":
		expected = "oauth_token"
	}
	if status.AuthMethod != expected || (expected == "api_key" && status.APIKeySource != "ANTHROPIC_API_KEY") {
		return fmt.Errorf("%w: native auth status did not select the requested credential", ErrClaudeCodeIsolation)
	}
	// Hash canonical metadata, not its formatting. Retain no account identifiers
	// or secrets in the cache, logs, errors, or returned capabilities.
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(data, &metadata)
	canonical, _ := json.Marshal(metadata)
	h := sha256.New()
	_, _ = h.Write(canonical)
	env := claudeEnvironment("")
	sort.Strings(env)
	for _, entry := range env {
		_, _ = h.Write([]byte("\x00" + entry))
	}
	var identity [sha256.Size]byte
	copy(identity[:], h.Sum(nil))
	if p.cacheIdentity != identity {
		p.cacheIdentity = identity
		p.probeUntil = time.Time{}
		p.probeErr = nil
		p.models = nil
	}
	return nil
}

func claudeArgs(schema json.RawMessage) []string {
	args := []string{"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--safe-mode", "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false,"syncClaudeAiSkills":false,"syncClaudeAiPlugins":false,"disableClaudeAiConnectors":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--tools", "", "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--permission-mode", "dontAsk", "--permission-prompts", "none"}
	if len(schema) > 0 {
		// Audited against 2.1.299 through a local fixture API: --tools "" removes
		// all action tools. --json-schema adds only the action-free final-response
		// formatter StructuredOutput. The wildcard deny would also deny that
		// formatter, potentially yielding success WITHOUT structured_output.
		args = append(args, "--json-schema", string(schema))
	} else {
		args = append(args, "--disallowedTools", "*")
	}
	return args
}

func (p *ClaudeCodeProvider) run(ctx context.Context, args []string, input []byte) ([]byte, error) {
	// Bound ALL request-owned processes, including status/version/control probes,
	// to one child at a time. Each waiter has its own cancellation context; it
	// never cancels an unrelated active generation to obtain the slot.
	select {
	case p.processGate <- struct{}{}:
		defer func() { <-p.processGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, err := p.pinExecutable(ctx)
	if err != nil {
		return nil, err
	}
	pinned := ctx.Value(claudeExecutableKey{}).(claudeExecutable)
	current, err := p.resolveExecutable(ctx)
	if err != nil {
		return nil, err
	}
	if current != pinned {
		return nil, fmt.Errorf("%w: claude-code executable changed during operation; retry to audit replacement", ErrIsolationUnavailable)
	}
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-claude-")
	if err != nil {
		return nil, fmt.Errorf("create claude-code work directory: %w", err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	stderr := &limitedBuffer{limit: p.options.MaxOutputBytes, cancel: cancel}
	cmd := command(ctx, pinned.Path, args, dir, claudeEnvironment(dir), bytes.NewReader(input))
	cmd.Stdout, cmd.Stderr = out, stderr
	err = cmd.Run()
	_ = killGroup(cmd)
	if out.exceeded() || stderr.exceeded() {
		return out.bytes(), ErrOutputLimit
	}
	if ctx.Err() != nil {
		return out.bytes(), ctx.Err()
	}
	if err != nil {
		if mapped := claudeFailure(out.bytes(), stderr.bytes()); mapped != nil {
			return out.bytes(), mapped
		}
		return out.bytes(), fmt.Errorf("claude-code process failed: %w", err)
	}
	return out.bytes(), nil
}

// probe caches successful metadata for five minutes, failures for five seconds.
// Initialize is a control request ONLY: no user message and no paid generation.
func (p *ClaudeCodeProvider) probe(ctx context.Context) ([]claudeModel, error) {
	select {
	case p.probeGate <- struct{}{}:
		defer func() { <-p.probeGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := claudeLocalIsolation(ctx); err != nil {
		return nil, err
	}
	var err error
	ctx, err = p.pinExecutable(ctx)
	if err != nil {
		return nil, err
	}
	executable := ctx.Value(claudeExecutableKey{}).(claudeExecutable)
	if p.cacheExecutable != executable {
		p.cacheExecutable = executable
		p.versionUntil = time.Time{}
		p.versionErr = nil
		p.probeUntil = time.Time{}
		p.probeErr = nil
		p.models = nil
	}
	if !time.Now().Before(p.versionUntil) {
		version, err := p.run(ctx, []string{"--version"}, nil)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if err == nil && strings.TrimSpace(string(version)) != "2.1.299 (Claude Code)" {
			err = fmt.Errorf("%w: claude-code requires audited version 2.1.299", ErrIsolationUnavailable)
		}
		p.versionErr = err
		p.versionUntil = time.Now().Add(5 * time.Minute)
		if err != nil {
			p.versionUntil = time.Now().Add(5 * time.Second)
		}
	}
	if p.versionErr != nil {
		return nil, p.versionErr
	}
	if err := p.authStatus(ctx); err != nil {
		return nil, err
	}
	if time.Now().Before(p.probeUntil) {
		return p.models, p.probeErr
	}
	data, err := p.run(ctx, claudeArgs(nil), []byte("{\"type\":\"control_request\",\"request_id\":\"ginkgo-initialize\",\"request\":{\"subtype\":\"initialize\"}}\n"))
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	if policyErr := claudeLocalIsolation(ctx); policyErr != nil {
		return nil, policyErr
	}
	p.models = nil
	if err == nil {
		p.models, err = claudeModels(data)
	}
	p.probeErr = err
	p.probeUntil = time.Now().Add(5 * time.Minute)
	if err != nil {
		p.probeUntil = time.Now().Add(5 * time.Second)
	}
	return p.models, err
}

func (p *ClaudeCodeProvider) capabilities(ctx context.Context, model string) (Capabilities, error) {
	caps := Capabilities{ImageInput: Unknown, ImageReason: "Claude Code supportedModels has no authoritative image-input metadata; configure image_input explicitly", ImageMediaTypes: []string{"image/png", "image/jpeg"}, MaxImageBytes: 5 << 20, MaxImages: imagecontext.MaxImages, MaxTotalImageBytes: 6 << 20, MaxImageWidth: 8000, MaxImageHeight: 8000, MaxRequestBytes: 10_000_000}
	if err := claudeLocalIsolation(ctx); err != nil {
		return caps, err
	}
	models, err := p.probe(ctx)
	if err != nil {
		return caps, err
	}
	selected := model
	if selected == "" {
		selected = os.Getenv("ANTHROPIC_MODEL")
	}
	if selected == "" {
		selected = os.Getenv("ANTHROPIC_DEFAULT_MODEL")
	}
	if selected == "" {
		selected = "default"
	}
	for _, m := range models {
		if m.Value == selected {
			caps.ResolvedModel = m.ResolvedModel
			break
		}
	}
	if caps.ResolvedModel == "" {
		// A full ID may be the resolvedModel of an alias instead of a separate
		// picker entry. Never infer exactness from a model-name prefix/family.
		for _, m := range models {
			if m.ResolvedModel == selected {
				caps.ResolvedModel = selected
				break
			}
		}
	}
	if caps.ResolvedModel == "" {
		caps.ImageReason = "selected Claude Code default/alias has no authoritative resolvedModel; images require an exact resolved model"
		if p.options.ImageInput == "unsupported" {
			caps.ImageInput = Unsupported
		}
		return caps, nil
	}
	caps.ImageInput = EffectiveImageSupport(Unknown, Supported, p.options.ImageInput)
	if caps.ImageInput == Supported {
		caps.ImageReason = "explicit image_input override; native base64 message-block transport"
	}
	if caps.ImageInput == Unsupported {
		caps.ImageReason = "image_input explicitly disabled"
	}
	return caps, nil
}

// Capabilities discovers native authentication and exact alias/default model
// metadata without a user turn or inference. Image support remains Unknown unless
// an exact model is resolved and ImageInput is explicitly supported. Model/version
// probes are cached for five minutes; native auth status is refreshed each call.
func (p *ClaudeCodeProvider) Capabilities(parent context.Context) (Capabilities, error) {
	ctx, done, err := p.begin(parent)
	if err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	defer done()
	return p.capabilities(ctx, p.options.Model)
}

// PreflightImages validates the complete ordered image batch and its exact
// stream-json encoding without starting a subprocess or transmitting content.
// The service can reject the whole image batch and continue with text only.
func (p *ClaudeCodeProvider) PreflightImages(ctx context.Context, request Request) error {
	ctx, done, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	select {
	case p.generationGate <- struct{}{}:
		defer func() { <-p.generationGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	_, _, err = p.imageInput(ctx, request)
	return err
}

func (p *ClaudeCodeProvider) imageInput(ctx context.Context, request Request) ([]byte, []imagecontext.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(request.System) > p.options.MaxInputBytes || len(request.Prompt) > p.options.MaxInputBytes-len(request.System) || len(request.OutputSchema) > p.options.MaxInputBytes-len(request.System)-len(request.Prompt) {
		return nil, nil, ErrInputLimit
	}
	images, err := imagecontext.Validate(ctx, request.Images)
	if err != nil {
		return nil, nil, err
	}
	total := 0
	for _, im := range images {
		total += len(im.Data)
		if len(im.Data) > 5<<20 || total > 6<<20 || im.Width > 8000 || im.Height > 8000 {
			return nil, nil, ErrInputLimit
		}
	}
	input, err := claudeInput(request.Prompt, images)
	if err != nil {
		return nil, nil, err
	}
	// The complete stdin frame, including base64, JSON escaping and its newline,
	// must fit the documented 10 MB CLI pipe limit; no image is silently removed.
	if len(input) > 10_000_000 || len(input) > encodedInputLimit(p.options.MaxInputBytes, images) {
		return nil, nil, ErrInputLimit
	}
	return input, images, nil
}

// Complete sends one verbatim user turn with ordered, validated image blocks.
// OutputSchema uses native validation and only the audited StructuredOutput
// final-response formatter, never action tools. Schema requests require a final
// structured_output value; a success result alone is insufficient.
func (p *ClaudeCodeProvider) Complete(parent context.Context, request Request) (response Response, resultErr error) {
	// Before generation starts, every failure is known not to have submitted
	// images. Once transport begins, nil means ambiguous until a complete native
	// success proves admission (not that the model used/understood the images).
	submitted := false
	var submission *bool = &submitted
	defer func() { response.ImagesSubmitted = submission }()
	ctx, done, err := p.begin(parent)
	if err != nil {
		return Response{}, err
	}
	defer done()
	// Queue before decoding/copying/base64-encoding images, not just before
	// spawning the child. A provider owns at most one generation at a time.
	select {
	case p.generationGate <- struct{}{}:
		defer func() { <-p.generationGate }()
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	ctx, err = p.pinExecutable(ctx)
	if err != nil {
		return Response{}, err
	}
	input, images, err := p.imageInput(ctx, request)
	if err != nil {
		return Response{}, err
	}
	model, variant := p.options.Model, p.options.Variant
	if request.Model != "" {
		model = request.Model
	}
	if request.Variant != "" {
		variant = request.Variant
	}
	if err := claudeSelection(model, variant); err != nil {
		return Response{}, err
	}
	if len(request.OutputSchema) > 0 && (!json.Valid(request.OutputSchema) || bytes.Equal(bytes.TrimSpace(request.OutputSchema), []byte("null"))) {
		return Response{}, fmt.Errorf("invalid claude-code output schema")
	}
	caps, err := p.capabilities(ctx, model)
	if err != nil {
		return Response{}, err
	}
	if len(images) > 0 && caps.ImageInput != Supported {
		if caps.ResolvedModel == "" {
			return Response{}, ErrModelUnavailable
		}
		return Response{}, fmt.Errorf("claude-code images require explicit supported image_input: %w", ErrGenerationRestricted)
	}
	args := claudeArgs(request.OutputSchema)
	// Pin aliases/default to the exact no-inference resolved model for this turn.
	if caps.ResolvedModel != "" {
		model = caps.ResolvedModel
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if variant != "" {
		args = append(args, "--effort", variant)
	}
	system := request.System
	if system == "" {
		system = "You generate text from the supplied user context. Do not perform actions."
	}
	// File form avoids argv/ps exposure of application system instructions.
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-claude-system-")
	if err != nil {
		return Response{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "system.txt")
	if err := os.WriteFile(path, []byte(system), 0600); err != nil {
		return Response{}, err
	}
	args = append(args, "--system-prompt-file", path)
	// Native initialization may have populated/refreshed the managed cache.
	// Recheck immediately before submitting context; never erase provider policy.
	if err := claudeLocalIsolation(ctx); err != nil {
		return Response{}, err
	}
	submission = nil
	output, err := p.run(ctx, args, input)
	parsed, parseErr := claudeResult(output, len(request.OutputSchema) > 0)
	if len(images) == 0 {
		submission = &submitted
	} else if parsed.ImagesSubmitted != nil && *parsed.ImagesSubmitted {
		submitted = true
		submission = &submitted
	}
	if err != nil {
		if errors.Is(err, ErrIsolationUnavailable) {
			submission = &submitted
		}
		return Response{}, err
	}
	return parsed, parseErr
}
