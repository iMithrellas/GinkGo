//go:build linux

package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// CodexOptions uses a dedicated native profile authenticated separately with
// CODEX_HOME=/absolute/private/profile codex login (ChatGPT), or login
// --with-api-key (key on stdin). Codex owns saved credentials and token refresh;
// Ginkgo never reads auth files or keys. Native managed-cloud startup can occur
// before policy verification; generation-only is a turn contract, not a promise
// of zero native startup activity. Incompatible managed policy is not bypassed.
type CodexOptions struct {
	Options
	Executable string
	Model      string
	Variant    string
	TempDir    string
	AuthHome   string
}

// CodexIsolationError reports an unavailable isolation prerequisite, not a login
// or inference failure. Diagnostics deliberately exclude paths and file content.
type CodexIsolationError struct{ Reason string }

func (e *CodexIsolationError) Error() string { return "codex isolation unavailable: " + e.Reason }
func (*CodexIsolationError) Unwrap() error   { return ErrIsolationUnavailable }

type codexModel struct {
	ID            string   `json:"id"`
	Model         string   `json:"model"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Default       bool     `json:"isDefault"`
	Modalities    []string `json:"inputModalities"`
	DefaultEffort string   `json:"defaultReasoningEffort"`
	Efforts       []struct {
		Effort      string `json:"reasoningEffort"`
		Description string `json:"description"`
	} `json:"supportedReasoningEfforts"`
}

// CodexProvider owns a serialized persistent native app-server. Each completion
// starts a new ephemeral thread; no conversation is resumed and no turn replayed.
// The audited runtime contract is pinned to codex-cli 0.163.0, upstream commit
// 83086188ae0614bd882818ff1aa23f3194962012. See codex_isolation.go.
type CodexProvider struct {
	options        CodexOptions
	gate           chan struct{}
	life           context.Context
	cancel         context.CancelFunc
	closeOnce      sync.Once
	closed         chan struct{}
	runtime        *codexRuntime
	root           string
	executable     string
	executableInfo os.FileInfo
	models         []codexModel
	expires        time.Time
	runtimeModel   string
	threads        int
	ownedHome      string
	seenThreads    map[string]bool
	closeErr       error
}

var codexProfileOwners sync.Map

// NewCodex is lazy: no lookup, filesystem access, process, or background work.
func NewCodex(options CodexOptions) (*CodexProvider, error) {
	normalized, err := options.Options.normalized()
	if err != nil {
		return nil, fmt.Errorf("codex options: %w", err)
	}
	options.Options = normalized
	if options.TempDir != "" && !filepath.IsAbs(options.TempDir) {
		return nil, fmt.Errorf("codex temp_dir must be absolute")
	}
	if options.Executable == "" {
		options.Executable = "codex"
	}
	life, cancel := context.WithCancel(context.Background())
	p := &CodexProvider{options: options, gate: make(chan struct{}, 1), life: life, cancel: cancel, closed: make(chan struct{})}
	p.gate <- struct{}{}
	return p, nil
}

func (p *CodexProvider) acquire(parent context.Context) (context.Context, func(), error) {
	if p == nil || p.life == nil {
		return nil, nil, &CodexIsolationError{Reason: "provider is not initialized"}
	}
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	stop := context.AfterFunc(p.life, cancel)
	select {
	case <-ctx.Done():
		stop()
		cancel()
		return nil, nil, ctx.Err()
	case <-p.gate:
	}
	if ctx.Err() != nil || p.life.Err() != nil {
		p.gate <- struct{}{}
		stop()
		cancel()
		if parent.Err() != nil {
			return nil, nil, parent.Err()
		}
		return nil, nil, context.Canceled
	}
	return ctx, func() { stop(); cancel(); p.gate <- struct{}{} }, nil
}

func (p *CodexProvider) dropRuntime() {
	if p.runtime != nil {
		p.runtime.stop()
		p.runtime = nil
	}
	p.runtimeModel = ""
	p.threads = 0
	p.seenThreads = nil
}

func (p *CodexProvider) prepare(ctx context.Context) error {
	if err := codexPreflight(ctx, p.options.AuthHome); err != nil {
		p.dropRuntime()
		return err
	}
	if owner, loaded := codexProfileOwners.LoadOrStore(p.options.AuthHome, p); loaded && owner != p {
		return &CodexIsolationError{Reason: "dedicated native profile is already owned by another Ginkgo provider"}
	}
	p.ownedHome = p.options.AuthHome
	if p.root == "" {
		root, err := os.MkdirTemp(p.options.TempDir, "ginkgo-codex-")
		if err != nil {
			return &CodexIsolationError{Reason: "cannot create private runtime directory"}
		}
		p.root = root
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			_ = os.RemoveAll(root)
			p.root = ""
			return ErrProviderUnavailable
		}
		p.root = canonical
		// A repository boundary prevents inherited project configuration/skills.
		if err := os.Mkdir(p.root+"/.git", 0700); err != nil {
			_ = os.RemoveAll(p.root)
			p.root = ""
			return ErrProviderUnavailable
		}
	}
	if p.executable == "" {
		executable, err := codexVerifyVersion(ctx, p.options.Executable, p.root, codexEnvironment(p.options.AuthHome, p.root))
		if err != nil {
			return err
		}
		info, err := os.Stat(executable)
		if err != nil {
			return ErrProviderUnavailable
		}
		p.executable = executable
		p.executableInfo = info
	}
	current, err := os.Stat(p.executable)
	if err != nil || !os.SameFile(current, p.executableInfo) || current.Size() != p.executableInfo.Size() || !current.ModTime().Equal(p.executableInfo.ModTime()) {
		p.dropRuntime()
		return &CodexIsolationError{Reason: "trusted executable identity changed; recreate the provider after auditing the installed release"}
	}
	// Trusted launchers (including the npm shim) can delegate to an upgraded
	// native binary without changing their own inode. Recheck the actual native
	// version under this caller's context before running any auth/startup work.
	if _, err := codexVerifyVersion(ctx, p.executable, p.root, codexEnvironment(p.options.AuthHome, p.root)); err != nil {
		p.dropRuntime()
		return err
	}
	for _, name := range []string{".codex", "AGENTS.md", "AGENTS.override.md", "config.toml", "hooks.json", ".agents", "skills", "plugins", "rules"} {
		if _, err := os.Lstat(filepath.Join(p.root, name)); !os.IsNotExist(err) {
			p.dropRuntime()
			return &CodexIsolationError{Reason: "private runtime directory contains ambient discovery content"}
		}
	}
	if err := codexVerifyNativeAuth(ctx, p.executable, p.root, codexEnvironment(p.options.AuthHome, p.root)); err != nil {
		p.dropRuntime()
		return err
	}
	return ctx.Err()
}

func (p *CodexProvider) discover(ctx context.Context) error {
	if time.Now().Before(p.expires) && len(p.models) > 0 {
		return nil
	}
	p.dropRuntime()
	r, err := codexStartRuntime(ctx, p.executable, codexArgs(""), p.root, codexEnvironment(p.options.AuthHome, p.root), p.options.MaxOutputBytes)
	if err != nil {
		return err
	}
	defer r.stop()
	var models []codexModel
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 32; page++ {
		var response struct {
			Data []codexModel `json:"data"`
			Next *string      `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100, "includeHidden": true}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := r.call(ctx, "model/list", params, &response, nil); err != nil {
			return err
		}
		for _, m := range response.Data {
			if m.Model == "" || m.ID == "" {
				return ErrProtocol
			}
			models = append(models, m)
		}
		if len(models) > 2048 {
			return ErrOutputLimit
		}
		if response.Next == nil || *response.Next == "" {
			if len(models) == 0 {
				return ErrModelUnavailable
			}
			p.models = models
			p.expires = time.Now().Add(5 * time.Minute)
			return nil
		}
		cursor = *response.Next
		if seen[cursor] {
			return ErrProtocol
		}
		seen[cursor] = true
	}
	return ErrOutputLimit
}

func (p *CodexProvider) selectModel(name string) (codexModel, error) {
	if name == "" {
		name = p.options.Model
	}
	var found *codexModel
	for i := range p.models {
		m := &p.models[i]
		if (name == "" && m.Default) || (name != "" && (m.ID == name || m.Model == name)) {
			if found != nil && found.Model != m.Model {
				return codexModel{}, ErrModelUnavailable
			}
			found = m
		}
	}
	if found == nil {
		return codexModel{}, ErrModelUnavailable
	}
	return *found, nil
}

func (p *CodexProvider) capabilities(m codexModel) Capabilities {
	support := Unknown
	if m.Modalities != nil {
		support = Unsupported
		for _, s := range m.Modalities {
			if s == "image" {
				support = Supported
			}
		}
	}
	effective := EffectiveImageSupport(support, Supported, p.options.ImageInput)
	reason := "selected model metadata does not advertise image input"
	if effective == Supported {
		reason = "native app-server data URLs; private local images when inline echo framing exceeds the output budget"
	}
	return Capabilities{ImageInput: effective, ResolvedModel: m.Model, ImageReason: reason, ImageMediaTypes: []string{"image/png", "image/jpeg"}, MaxImageBytes: imagecontext.MaxImageBytes, MaxImages: imagecontext.MaxImages, MaxTotalImageBytes: imagecontext.MaxTotalBytes, MaxRequestBytes: p.options.MaxInputBytes + imagecontext.MaxTotalBytes*4/3 + (64 << 10)}
}

// PreflightImages validates the complete ordered image batch and its exact data
// URL framing without submitting note content or starting a generation thread.
func (p *CodexProvider) PreflightImages(parent context.Context, request Request) error {
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return err
	}
	defer release()
	if len(request.System)+len(request.Prompt)+len(request.OutputSchema) > p.options.MaxInputBytes {
		return ErrInputLimit
	}
	images, err := imagecontext.Validate(ctx, request.Images)
	if err != nil {
		return err
	}
	if err = p.prepare(ctx); err != nil {
		return err
	}
	if err = p.discover(ctx); err != nil {
		return err
	}
	m, err := p.selectModel(request.Model)
	if err != nil {
		return err
	}
	if len(images) > 0 && p.capabilities(m).ImageInput != Supported {
		return ErrGenerationRestricted
	}
	paths, cleanup, err := p.imagePaths(images, false)
	if err != nil {
		return err
	}
	defer cleanup()
	_, err = p.turnParams(request, m, images, paths, strings.Repeat("t", 128), request.Variant)
	return err
}

func (p *CodexProvider) turnParams(request Request, m codexModel, images []imagecontext.Image, paths []string, thread, effort string) (map[string]any, error) {
	input := []any{map[string]any{"type": "text", "text": request.Prompt, "text_elements": []any{}}}
	for i, im := range images {
		if len(paths) > 0 {
			input = append(input, map[string]any{"type": "localImage", "path": paths[i]})
		} else {
			input = append(input, map[string]any{"type": "image", "url": "data:" + im.MediaType + ";base64," + base64.StdEncoding.EncodeToString(im.Data)})
		}
	}
	params := map[string]any{"threadId": thread, "input": input, "model": m.Model}
	if effort != "" {
		params["effort"] = effort
	}
	if len(request.OutputSchema) > 0 {
		params["outputSchema"] = request.OutputSchema
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, ErrProtocol
	}
	if len(encoded) > encodedInputLimit(p.options.MaxInputBytes, images) {
		return nil, ErrInputLimit
	}
	return params, nil
}

// Native userMessage events echo inline image URLs. For large batches use
// private localImage files to keep protocol output bounded without rejecting or
// silently dropping otherwise valid images. Preflight computes identical framing
// with conservative placeholder names and never writes note/image content.
func (p *CodexProvider) imagePaths(images []imagecontext.Image, write bool) ([]string, func(), error) {
	noop := func() {}
	total := 0
	for _, im := range images {
		total += len(im.Data)
	}
	if total <= p.options.MaxOutputBytes/8 {
		return nil, noop, nil
	}
	dir := filepath.Join(p.root, "input-xxxxxxxxxxxxxxxxxxxxxxxx")
	if write {
		var err error
		dir, err = os.MkdirTemp(p.root, "input-")
		if err != nil {
			return nil, noop, ErrProviderUnavailable
		}
	}
	cleanup := func() {
		if write {
			_ = os.RemoveAll(dir)
		}
	}
	paths := make([]string, len(images))
	for i, im := range images {
		extension := ".png"
		if im.MediaType == "image/jpeg" {
			extension = ".jpg"
		}
		paths[i] = filepath.Join(dir, fmt.Sprintf("%02d%s", i, extension))
		if write {
			if err := os.WriteFile(paths[i], im.Data, 0600); err != nil {
				cleanup()
				return nil, noop, ErrProviderUnavailable
			}
		}
	}
	return paths, cleanup, nil
}

func (p *CodexProvider) Capabilities(parent context.Context) (Capabilities, error) {
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return Capabilities{ImageInput: Unsupported}, err
	}
	defer release()
	if err = p.prepare(ctx); err != nil {
		return Capabilities{ImageInput: Unsupported}, err
	}
	if err = p.discover(ctx); err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	m, err := p.selectModel("")
	if err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	return p.capabilities(m), nil
}

func (p *CodexProvider) Complete(parent context.Context, request Request) (response Response, err error) {
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return response, err
	}
	defer release()
	if len(request.System)+len(request.Prompt)+len(request.OutputSchema) > p.options.MaxInputBytes {
		return response, ErrInputLimit
	}
	if len(request.OutputSchema) > 0 {
		var schema map[string]any
		if json.Unmarshal(request.OutputSchema, &schema) != nil || schema == nil {
			return response, fmt.Errorf("codex output schema must be a JSON object: %w", ErrProtocol)
		}
	}
	images, err := imagecontext.Validate(ctx, request.Images)
	if err != nil {
		return response, err
	}
	if err = p.prepare(ctx); err != nil {
		return response, err
	}
	if err = p.discover(ctx); err != nil {
		return response, err
	}
	m, err := p.selectModel(request.Model)
	if err != nil {
		return response, err
	}
	if len(images) > 0 && p.capabilities(m).ImageInput != Supported {
		return response, fmt.Errorf("codex selected model does not advertise image input: %w", ErrGenerationRestricted)
	}
	effort := request.Variant
	if effort == "" {
		effort = p.options.Variant
	}
	if effort == "" {
		effort = m.DefaultEffort
	}
	if effort != "" {
		valid := false
		for _, e := range m.Efforts {
			if e.Effort == effort {
				valid = true
			}
		}
		if !valid {
			return response, fmt.Errorf("codex reasoning variant unavailable: %w", ErrModelUnavailable)
		}
	}
	paths, cleanupImages, err := p.imagePaths(images, true)
	if err != nil {
		return response, err
	}
	defer cleanupImages()
	if _, err = p.turnParams(request, m, images, paths, strings.Repeat("t", 128), effort); err != nil {
		return response, err
	}
	if p.runtime == nil || p.runtimeModel != m.Model || p.threads >= 8 {
		p.dropRuntime()
		// An explicit user override can fill missing model metadata. Codex's
		// normalizer otherwise silently strips images from a text-only catalog.
		// Do not change the discovered metadata or override a known negative.
		runtimeModel := m
		if m.Modalities == nil && p.options.ImageInput == "supported" {
			runtimeModel.Modalities = []string{"text", "image"}
		}
		catalog, err := codexWriteCatalog(p.root, runtimeModel)
		if err != nil {
			return response, err
		}
		p.runtime, err = codexStartRuntime(ctx, p.executable, codexArgs(catalog), p.root, codexEnvironment(p.options.AuthHome, p.root), p.options.MaxOutputBytes)
		if err != nil {
			return response, err
		}
		p.runtimeModel = m.Model
	}
	r := p.runtime
	r.resetBudget()
	r.budgetLimit = p.options.MaxOutputBytes
	defer func() {
		if err != nil {
			p.dropRuntime()
		}
	}()
	var thread struct {
		Thread struct {
			ID           string            `json:"id"`
			Ephemeral    bool              `json:"ephemeral"`
			Path         *string           `json:"path"`
			Forked       *string           `json:"forkedFromId"`
			Parent       *string           `json:"parentThreadId"`
			Environments []json.RawMessage `json:"environments"`
		} `json:"thread"`
		Model    string `json:"model"`
		Provider string `json:"modelProvider"`
		Cwd      string `json:"cwd"`
		Approval string `json:"approvalPolicy"`
		Sandbox  struct {
			Type    string `json:"type"`
			Network bool   `json:"networkAccess"`
		} `json:"sandbox"`
		Instructions []string `json:"instructionSources"`
	}
	if err = r.verifyPolicy(ctx); err != nil {
		return response, err
	}
	err = r.call(ctx, "thread/start", map[string]any{"model": m.Model, "modelProvider": "openai", "allowProviderModelFallback": false, "ephemeral": true, "cwd": p.root, "approvalPolicy": "never", "sandbox": "read-only", "baseInstructions": request.System, "developerInstructions": "", "dynamicTools": []any{}, "environments": []any{}, "selectedCapabilityRoots": []any{}}, &thread, nil)
	if err != nil {
		return response, err
	}
	if thread.Thread.ID == "" || len(thread.Thread.ID) > 128 || !thread.Thread.Ephemeral || thread.Model != m.Model || thread.Provider != "openai" || thread.Cwd != p.root || thread.Approval != "never" || thread.Sandbox.Type != "readOnly" || thread.Sandbox.Network || len(thread.Instructions) > 0 || thread.Thread.Path != nil || thread.Thread.Forked != nil || thread.Thread.Parent != nil || len(thread.Thread.Environments) > 0 || p.seenThreads[thread.Thread.ID] {
		return response, &CodexIsolationError{Reason: "thread did not preserve ephemeral, model and instruction isolation"}
	}
	if p.seenThreads == nil {
		p.seenThreads = map[string]bool{}
	}
	p.seenThreads[thread.Thread.ID] = true
	p.threads++
	params, err := p.turnParams(request, m, images, paths, thread.Thread.ID, effort)
	if err != nil {
		return response, err
	}
	state := codexTurnState{thread: thread.Thread.ID, limit: p.options.MaxOutputBytes}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err = r.call(ctx, "turn/start", params, &turn, state.event)
	if state.turn != "" && turn.Turn.ID != "" && state.turn != turn.Turn.ID {
		return response, ErrProtocol
	}
	if turn.Turn.ID != "" {
		state.turn = turn.Turn.ID
	}
	if err == nil && state.turn == "" {
		err = ErrProtocol
	}
	for err == nil && !state.done {
		var message codexMessage
		message, err = r.next(ctx)
		if err == nil {
			err = state.event(message)
		}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = r.call(cleanup, "turn/interrupt", map[string]any{"threadId": state.thread, "turnId": state.turn}, nil, nil)
			cancel()
		}
		return response, err
	}
	text, err := state.result()
	if err != nil {
		return response, err
	}
	if len(request.OutputSchema) > 0 && !json.Valid([]byte(text)) {
		return response, fmt.Errorf("codex final schema result is not JSON: %w", ErrProtocol)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	var unsubscribe struct {
		Status string `json:"status"`
	}
	closed := false
	event := func(message codexMessage) error {
		if message.Method == "thread/closed" {
			var params struct {
				Thread string `json:"threadId"`
			}
			if json.Unmarshal(message.Params, &params) != nil {
				return ErrProtocol
			}
			if params.Thread == state.thread {
				closed = true
			}
		}
		return nil
	}
	err = r.call(cleanup, "thread/unsubscribe", map[string]any{"threadId": state.thread}, &unsubscribe, event)
	for err == nil && !closed && unsubscribe.Status != "notLoaded" {
		var message codexMessage
		message, err = r.next(cleanup)
		if err == nil {
			err = event(message)
		}
	}
	cancel()
	// thread_unload_delay_secs=0 plus thread/closed establishes thread cleanup.
	// Recycle after at most eight turns as an additional runtime retention bound.
	if err != nil || p.threads >= 8 {
		p.dropRuntime()
		err = nil
	}
	submitted := len(images) > 0
	return Response{Text: text, ImagesSubmitted: &submitted}, nil
}

// Close cancels active/queued callers, joins all native I/O and process work,
// then removes only this provider's private temporary directory.
func (p *CodexProvider) Close() error {
	if p == nil || p.life == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.cancel()
		<-p.gate
		p.dropRuntime()
		if p.root != "" {
			if err := os.RemoveAll(p.root); err != nil {
				p.closeErr = fmt.Errorf("codex private runtime cleanup failed: %w", ErrProviderUnavailable)
			}
		}
		if p.ownedHome != "" {
			codexProfileOwners.CompareAndDelete(p.ownedHome, p)
		}
		close(p.closed)
		p.gate <- struct{}{}
	})
	<-p.closed
	return p.closeErr
}

var _ Provider = (*CodexProvider)(nil)
var _ CapabilityProvider = (*CodexProvider)(nil)
