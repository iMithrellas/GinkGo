//go:build linux

package ai

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

const openCodeNativeBaseSystem = "Generate only the requested text. Do not use tools or external integrations."

// The audited releases validate every attachment with Prompt.Base64's grouped
// regular expression (packages/schema/src/prompt.ts). In the shipped Bun 1.4.2
// runtime it rejects valid ~5.3 MiB PNGs, producing an empty HTTP 500 before
// session.prompt admission or any model request. Keep a conservative per-image
// wire bound below that runtime limit. Oversized originals remain accepted and
// retained; deterministic smaller copies are prepared only for the AI request.
const openCodeNativeMaxImageBytes = 4 << 20

// OpenCodeNativeOptions configures the V2 session.prompt multimodal transport.
// Empty/default/auto Model selects the isolated runtime's default, not ambient
// user configuration. Saved credentials are reused without copying secrets.
type OpenCodeNativeOptions struct {
	Options
	Executable    string
	Model         string
	Variant       string
	TempDir       string
	EphemeralAuth bool
}

// OpenCodeNativeProvider owns a persistent private server. Call Close when the
// daemon releases it. Requests serialize, use fresh sessions, and never replay
// ambiguous admissions after a transport failure or server crash.
//
// Contract audited against official V2 API/client/agents/config/policies docs,
// openapi.json (2026-10-18), and release sources 2.0.25 (b44eea9) and
// 2.0.31 (22728d6): protocol/groups/session.ts, core/session/{context,prompt,
// session}.ts, core/tool.ts, cli/server-process.ts, config/plugin/{agent,image}.ts,
// image/photon.ts and location-service-map.ts. Request-specific instructions
// live only in private agent.system config: API instruction entries are NOT used
// because their content-addressed instruction_blob rows outlive session deletion.
// Tool.snapshot's whollyDisabled removes all definitions AND Code Mode when
// the last matching rule is {action:"*",resource:"*",effect:"deny"}.
// This is runtime tool removal, not a prompt-only restriction.
//
// Native structured output is deliberately omitted: session.prompt has no
// output-schema field; synthesizing a schema tool would violate tool-free use.
// Request.OutputSchema is supplied as a separate system instruction constraint;
// the caller remains responsible for strict output validation.
type OpenCodeNativeProvider struct {
	options        OpenCodeNativeOptions
	gate           chan struct{}
	life           context.Context
	cancel         context.CancelFunc
	closed         atomic.Bool
	closeOnce      sync.Once
	closeErr       error
	runtime        *openCodeNativeRuntime // protected by gate
	cache          map[string]openCodeNativeCached
	pendingCleanup map[string]struct{} // owned IDs only; never replay prompts
}

type openCodeNativeRuntime struct {
	server        *openCodeSessionServer
	client        *http.Client
	dir, password string
	agentSystem   string
}

type openCodeNativeModel struct {
	ID           string `json:"id"`
	ModelID      string `json:"modelID"`
	ProviderID   string `json:"providerID"`
	Enabled      bool   `json:"enabled"`
	Capabilities struct {
		Input []string `json:"input"`
	} `json:"capabilities"`
	Variants []struct {
		ID string `json:"id"`
	} `json:"variants"`
}

type openCodeNativeCached struct {
	ref   modelRef
	caps  Capabilities
	until time.Time
}

// NewOpenCodeNative validates options without starting a runtime or inference.
// The caller owns the returned provider and must call Close when done.
func NewOpenCodeNative(options OpenCodeNativeOptions) (*OpenCodeNativeProvider, error) {
	limits, err := options.Options.normalized()
	if err != nil {
		return nil, err
	}
	options.Options = limits
	if options.Executable == "" {
		options.Executable = "opencode"
	}
	if _, err := nativeModelRef(options.Model, options.Variant); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	p := &OpenCodeNativeProvider{options: options, gate: make(chan struct{}, 1), life: life, cancel: cancel, cache: make(map[string]openCodeNativeCached), pendingCleanup: make(map[string]struct{})}
	p.gate <- struct{}{}
	return p, nil
}

func nativeModelRef(model, variant string) (*modelRef, error) {
	if strings.ContainsAny(variant, "#/\r\n\t ") {
		return nil, fmt.Errorf("invalid opencode variant")
	}
	if model == "" || model == "default" || model == "auto" {
		return nil, nil
	}
	ref, err := openCodeModel(model)
	if err != nil {
		return nil, err
	}
	if variant != "" {
		ref.Variant = variant
	}
	return ref, nil
}

func (p *OpenCodeNativeProvider) acquire(parent context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(parent, p.options.Timeout)
	stop := context.AfterFunc(p.life, cancel)
	select {
	case <-ctx.Done():
		stop()
		cancel()
		return nil, nil, ctx.Err()
	case <-p.gate:
	}
	if p.closed.Load() || ctx.Err() != nil {
		p.gate <- struct{}{}
		stop()
		cancel()
		return nil, nil, context.Canceled
	}
	return ctx, func() { p.gate <- struct{}{}; stop(); cancel() }, nil
}

// Close cancels in-flight requests, kills only the owned private process group,
// and removes its private filesystem. It is safe to call more than once.
func (p *OpenCodeNativeProvider) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.cancel()
		<-p.gate
		p.stopRuntime()
		if len(p.pendingCleanup) != 0 {
			p.closeErr = fmt.Errorf("private opencode session cleanup could not be confirmed after runtime failure")
		}
		p.gate <- struct{}{}
	})
	return p.closeErr
}

func (p *OpenCodeNativeProvider) stopRuntime() {
	if r := p.runtime; r != nil {
		r.server.close()
		r.client.CloseIdleConnections()
		_ = os.RemoveAll(r.dir)
		p.runtime = nil
	}
	clear(p.cache)
	if p.options.EphemeralAuth {
		clear(p.pendingCleanup) // private database was removed with the runtime
	}
}

func (p *OpenCodeNativeProvider) start(ctx context.Context) (err error) {
	if p.runtime != nil {
		// Saved credentials can change while this daemon-owned runtime is alive.
		// Recheck before location work (even cached discovery), rather than
		// assuming that a once-safe account remains free of Console forever.
		if !p.options.EphemeralAuth {
			r := p.runtime
			if err := checkOpenCodeCredentials(ctx, r.client, r.server.endpoint, r.password, p.options.MaxOutputBytes); err != nil {
				p.stopRuntime()
				return err
			}
		}
		return nil
	}
	if _, err := os.Lstat("/etc/opencode"); !os.IsNotExist(err) {
		return fmt.Errorf("opencode native isolation requires /etc/opencode to be absent")
	}
	dir, err := os.MkdirTemp(p.options.TempDir, "ginkgo-ai-opencode-native-")
	if err != nil {
		return fmt.Errorf("create native runtime: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = checkOpenCodeNativeVersion(startup, p.options.Executable, dir); err != nil {
		return err
	}
	configDir := filepath.Join(dir, "config")
	if err = os.Mkdir(configDir, 0700); err != nil {
		return err
	}
	if err = writeOpenCodeNativeConfig(configDir, openCodeNativeBaseSystem); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	password := hex.EncodeToString(secret)
	env := openCodeEnvironment(configDir, password)
	if p.options.EphemeralAuth {
		env = privateOpenCodeDataEnvironment(env, dir)
	}
	server, err := startOpenCodeSessionServer(startup, OpenCodeSessionOptions{Options: p.options.Options, Executable: p.options.Executable}, dir, env)
	if err != nil {
		return err
	}
	r := &openCodeNativeRuntime{server: server, client: privateOpenCodeHTTPClient(), dir: dir, password: password, agentSystem: openCodeNativeBaseSystem}
	p.runtime = r
	defer func() {
		if err != nil {
			p.stopRuntime()
		}
	}()
	// Must precede ANY location-scoped call: Console is mandatory and could
	// import remote configuration/plugins despite our local deny policy.
	if !p.options.EphemeralAuth {
		if err = checkOpenCodeCredentials(startup, r.client, server.endpoint, password, p.options.MaxOutputBytes); err != nil {
			return err
		}
	}
	// A previous crash can prevent HTTP deletion from reaching the saved-auth
	// database. On the next explicit call, retry ONLY deletion of our owned IDs,
	// before admitting new work. Never replay creation or inference.
	for id := range p.pendingCleanup {
		if err = deleteOpenCodeSession(server.endpoint, password, id); err != nil {
			return fmt.Errorf("recover private opencode session cleanup failed")
		}
		delete(p.pendingCleanup, id)
	}
	return nil
}

// Native-only config: never change the legacy opencode-session adapter's plugin
// contract. ConfigImagePlugin is required for media.image settings to take
// effect. Photon returns original bytes unchanged when all bounds hold; with
// auto_resize:false it rejects rather than re-encoding an out-of-bounds image.
// MaxPixels bounds either axis as well as area in imagecontext.Validate. The
// validated 12 MiB raw batch stays below native boundImages' 25 MiB base64 trigger.
func writeOpenCodeNativeConfig(configDir, system string) error {
	// ConfigVariable substitutes raw {env:...}/{file:...} tokens BEFORE JSON
	// decoding in both audited releases. Escape opening braces inside this JSON
	// string so arbitrary system/schema text remains literal and cannot read
	// ambient files or credentials. JSON decoding restores the original bytes.
	literalSystem, err := json.Marshal(system)
	if err != nil {
		return fmt.Errorf("encode private native instructions failed")
	}
	encodedSystem := json.RawMessage(strings.ReplaceAll(string(literalSystem), "{", `\u007b`))
	config := map[string]any{
		"$schema":      "https://opencode.ai/config.json",
		"plugins":      []string{"-*", "opencode.config.agent", "opencode.config.image", "opencode.models.dev", "opencode.provider.*"},
		"instructions": []string{}, "skills": []string{}, "mcp": map[string]any{"servers": map[string]any{}},
		"media": map[string]any{"image": map[string]any{
			"auto_resize": false, "max_width": imagecontext.MaxPixels, "max_height": imagecontext.MaxPixels,
			"max_base64_bytes": base64.StdEncoding.EncodedLen(openCodeNativeMaxImageBytes),
		}},
		"agents": map[string]any{openCodeSessionAgent: map[string]any{
			"description": "Generation-only agent with no tools or external context", "mode": "primary", "system": encodedSystem,
			"permissions": denyAllOpenCodePermissions(),
		}},
		"policies": []map[string]string{
			{"action": "tool.use", "resource": "*", "effect": "deny"},
			{"action": "integration.use", "resource": "plugin:*", "effect": "deny"},
			{"action": "integration.use", "resource": "mcp:*", "effect": "deny"},
			{"action": "integration.use", "resource": "skill:*", "effect": "deny"},
		},
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode private native config failed")
	}
	temporary := filepath.Join(configDir, "opencode.json.tmp")
	if err := os.WriteFile(temporary, encoded, 0600); err != nil {
		return fmt.Errorf("write private native config failed")
	}
	if err := os.Rename(temporary, filepath.Join(configDir, "opencode.json")); err != nil {
		return fmt.Errorf("replace private native config failed")
	}
	return nil
}

// location.reload is a private-server-wide, awaited location rebuild in both
// audited releases (POST /api/location/reload -> 204). Agent activation can
// settle asynchronously after the build, so verify the exact agent.system
// before session creation. Agent.system is passed directly to SystemPart by
// SessionModelRequest.baseTranscript, not to InstructionState's durable sources.
func (p *OpenCodeNativeProvider) setSystem(ctx context.Context, instructions string) error {
	system := openCodeNativeBaseSystem
	if instructions != "" {
		system += "\n\n" + instructions
	}
	r := p.runtime
	if r.agentSystem == system {
		return nil
	}
	if err := writeOpenCodeNativeConfig(filepath.Join(r.dir, "config"), system); err != nil {
		return err
	}
	if err := p.call(ctx, "POST", "/api/location/reload", nil, 204, nil); err != nil {
		return err
	}
	clear(p.cache)
	verify, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var agent struct {
			Data *struct {
				System string `json:"system"`
			} `json:"data"`
		}
		err := p.call(verify, "GET", "/api/agent/"+openCodeSessionAgent, nil, 200, &agent)
		if err == nil && agent.Data != nil && agent.Data.System == system {
			r.agentSystem = system
			return nil
		}
		if err != nil {
			var status *openCodeNativeHTTPError
			if !errors.As(err, &status) || status.status != 404 {
				return err
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-verify.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("private native agent configuration did not settle")
		case <-timer.C:
		}
	}
}

// Only the exact releases whose prompt/isolation contracts were source-audited
// are admitted. A newer V2 version is not proof of compatible isolation.
func checkOpenCodeNativeVersion(ctx context.Context, executable, dir string) error {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	out := &limitedBuffer{limit: 4096, cancel: cancel}
	errout := &limitedBuffer{limit: 4096, cancel: cancel}
	cmd := command(child, executable, []string{"--version"}, dir, openCodeEnvironment(dir, ""), nil)
	cmd.Stdout, cmd.Stderr = out, errout
	err := cmd.Run()
	_ = killGroup(cmd)
	if out.exceeded() || errout.exceeded() {
		return ErrOutputLimit
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("check opencode native version: %w", err)
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out.bytes())), "opencode v")
	if v != "2.0.25" && v != "2.0.31" {
		return fmt.Errorf("opencode native requires audited stable release 2.0.25 or 2.0.31")
	}
	return nil
}

func (p *OpenCodeNativeProvider) call(ctx context.Context, method, path string, payload any, status int, output any) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return ErrProtocol
		}
		if len(body) > encodedInputLimit(p.options.MaxInputBytes, []imagecontext.Image{{}}) {
			return ErrInputLimit
		}
	}
	r := p.runtime
	result, err := privateOpenCodeRequest(ctx, r.client, method, r.server.endpoint+path, r.password, body)
	if err != nil {
		if r.server.logsExceeded() {
			return ErrOutputLimit
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("opencode native transport failed (request not replayed): %w", ErrProviderUnavailable)
	}
	defer result.Body.Close()
	limit := int64(p.options.MaxOutputBytes)
	// Admission echoes the known user input, including all image base64 bytes;
	// this is not provider output. Likewise agent verification echoes our bounded
	// private system config. Keep those input echoes separately bounded instead
	// of rejecting valid batches under ordinary generated-output limits.
	if strings.HasSuffix(path, "/prompt") {
		limit += int64(len(body))
	}
	if path == "/api/agent/"+openCodeSessionAgent {
		limit += 257 << 10
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, limit+1))
	if int64(len(data)) > limit || r.server.logsExceeded() {
		return ErrOutputLimit
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProtocol
	}
	if result.StatusCode != status {
		return &openCodeNativeHTTPError{status: result.StatusCode, cause: classifyOpenCodeSessionFailure(result.StatusCode, data, p.options.Model)}
	}
	if output != nil {
		// API records are extensible; validate the fields used below explicitly.
		dec := json.NewDecoder(strings.NewReader(string(data)))
		if dec.Decode(output) != nil || dec.Decode(new(any)) != io.EOF {
			return ErrProtocol
		}
	}
	return ctx.Err()
}

// Preserve a known HTTP rejection separately from an ambiguous transport loss.
type openCodeNativeHTTPError struct {
	status int
	cause  error
}

func (e *openCodeNativeHTTPError) Error() string { return e.cause.Error() }
func (e *openCodeNativeHTTPError) Unwrap() error { return e.cause }

func (p *OpenCodeNativeProvider) resolve(ctx context.Context, model, variant string) (openCodeNativeCached, error) {
	key := model + "#" + variant
	if cached, ok := p.cache[key]; ok && time.Now().Before(cached.until) {
		return cached, nil
	}
	ref, err := nativeModelRef(model, variant)
	if err != nil {
		return openCodeNativeCached{}, err
	}
	var selected *openCodeNativeModel
	if ref == nil {
		var result struct {
			Data *openCodeNativeModel `json:"data"`
		}
		if err := p.call(ctx, "GET", "/api/model/default", nil, 200, &result); err != nil {
			return openCodeNativeCached{}, err
		}
		selected = result.Data
	} else {
		var result struct {
			Data []openCodeNativeModel `json:"data"`
		}
		if err := p.call(ctx, "GET", "/api/model", nil, 200, &result); err != nil {
			return openCodeNativeCached{}, err
		}
		for i := range result.Data {
			m := &result.Data[i]
			if m.ProviderID == ref.ProviderID && m.ID == ref.ID {
				selected = m
				break
			}
		}
	}
	if selected == nil {
		return openCodeNativeCached{}, ErrModelUnavailable
	}
	if selected.ID == "" || selected.ModelID == "" || selected.ProviderID == "" {
		return openCodeNativeCached{}, ErrProtocol
	}
	if !selected.Enabled {
		return openCodeNativeCached{}, ErrModelUnavailable
	}
	if ref == nil {
		ref = &modelRef{ID: selected.ID, ProviderID: selected.ProviderID, Variant: variant}
	}
	if ref.Variant == "default" {
		ref.Variant = ""
	}
	if ref.Variant != "" {
		found := false
		for _, v := range selected.Variants {
			if v.ID == ref.Variant {
				found = true
			}
		}
		if !found {
			return openCodeNativeCached{}, ErrModelUnavailable
		}
	}
	support := Unknown
	if selected.Capabilities.Input != nil {
		support = Unsupported
		for _, input := range selected.Capabilities.Input {
			if input == "image" {
				support = Supported
			}
		}
	}
	caps := Capabilities{
		ImageInput:      EffectiveImageSupport(support, Supported, p.options.ImageInput),
		ResolvedModel:   selected.ProviderID + "/" + selected.ModelID,
		ImageMediaTypes: []string{"image/png", "image/jpeg"},
		MaxImages:       imagecontext.MaxImages, MaxImageBytes: imagecontext.MaxImageBytes,
		MaxTotalImageBytes: imagecontext.MaxTotalBytes,
		// The tighter 32-megapixel AREA limit is enforced by Validate/preflight;
		// these axis bounds mirror the native no-resize processing configuration.
		MaxImageWidth: imagecontext.MaxPixels, MaxImageHeight: imagecontext.MaxPixels,
		MaxRequestBytes: encodedInputLimit(p.options.MaxInputBytes, []imagecontext.Image{{}}),
	}
	if caps.ImageInput != Supported {
		caps.ImageReason = "selected model does not advertise image input (or image input is disabled)"
	}
	value := openCodeNativeCached{ref: *ref, caps: caps, until: time.Now().Add(30 * time.Second)}
	// Cache successes only; plugin settlement, missing auth and network errors
	// must remain retryable. Bound cache growth from request model overrides.
	if len(p.cache) >= 16 {
		clear(p.cache)
	}
	p.cache[key] = value
	return value, nil
}

// Capabilities discovers the isolated runtime's configured/default model without
// inference. Successful metadata is cached for at most thirty seconds.
func (p *OpenCodeNativeProvider) Capabilities(parent context.Context) (Capabilities, error) {
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	defer release()
	if err := p.start(ctx); err != nil {
		return Capabilities{ImageInput: Unknown}, err
	}
	resolved, err := p.resolve(ctx, p.options.Model, p.options.Variant)
	if err != nil {
		p.stopRuntime()
		return Capabilities{ImageInput: Unknown}, err
	}
	return resolved.caps, nil
}

func (p *OpenCodeNativeProvider) requestModel(request Request) (string, string) {
	model, variant := p.options.Model, p.options.Variant
	if request.Model != "" {
		model = request.Model
		if strings.Contains(model, "#") {
			variant = ""
		}
	}
	if request.Variant != "" {
		variant = request.Variant
	}
	return model, variant
}

// nativeInstructions checks bounded JSON syntax and the JSON Schema root shape.
// It intentionally does not pretend to implement a complete JSON Schema engine.
func nativeInstructions(request Request, maxInput int) (string, error) {
	system := request.System
	if len(request.OutputSchema) != 0 {
		if len(request.OutputSchema) > 64<<10 {
			return "", ErrInputLimit
		}
		var schema any
		if json.Unmarshal(request.OutputSchema, &schema) != nil {
			return "", fmt.Errorf("invalid opencode output schema JSON")
		}
		if !nativeSchemaSyntax(schema, 0) {
			return "", fmt.Errorf("invalid opencode output schema syntax")
		}
		// Preserve the original schema bytes (not float64-decoded numbers).
		system += "\n\nReturn only JSON conforming to this JSON Schema. Do not use markdown fences or surrounding commentary.\n" + string(request.OutputSchema)
	}
	if len(system) > maxInput || len(request.Prompt) > maxInput-len(system) {
		return "", ErrInputLimit
	}
	encoded, err := json.Marshal(system)
	if err != nil || len(encoded) > 256<<10 {
		return "", ErrInputLimit
	}
	return system, nil
}

// Check schema-bearing keywords and common assertion keyword shapes without
// resolving references, fetching metaschemas, or rejecting extension keywords.
// This is bounded syntax checking, not semantic validation of generated output.
func nativeSchemaSyntax(value any, depth int) bool {
	if depth > 128 {
		return false
	}
	if _, ok := value.(bool); ok {
		return true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	stringsOnly := func(value any) bool {
		items, ok := value.([]any)
		if !ok {
			return false
		}
		seen := make(map[string]bool)
		for _, item := range items {
			text, ok := item.(string)
			if !ok || seen[text] {
				return false
			}
			seen[text] = true
		}
		return true
	}
	for key, value := range object {
		switch key {
		case "$schema", "$id", "$ref", "$anchor", "$dynamicRef", "$dynamicAnchor", "$comment", "title", "description", "format", "pattern", "contentEncoding", "contentMediaType":
			if _, ok := value.(string); !ok {
				return false
			}
		case "type":
			validType := func(value any) bool {
				text, ok := value.(string)
				return ok && (text == "null" || text == "boolean" || text == "object" || text == "array" || text == "number" || text == "integer" || text == "string")
			}
			if items, ok := value.([]any); ok {
				if len(items) == 0 || !stringsOnly(value) {
					return false
				}
				for _, item := range items {
					if !validType(item) {
						return false
					}
				}
			} else if !validType(value) {
				return false
			}
		case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
			children, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for _, child := range children {
				if !nativeSchemaSyntax(child, depth+1) {
					return false
				}
			}
		case "additionalProperties", "unevaluatedProperties", "propertyNames", "additionalItems", "contains", "unevaluatedItems", "not", "if", "then", "else", "contentSchema":
			if !nativeSchemaSyntax(value, depth+1) {
				return false
			}
		case "items":
			if items, ok := value.([]any); ok {
				for _, item := range items {
					if !nativeSchemaSyntax(item, depth+1) {
						return false
					}
				}
			} else if !nativeSchemaSyntax(value, depth+1) {
				return false
			}
		case "prefixItems", "allOf", "anyOf", "oneOf":
			items, ok := value.([]any)
			if !ok || len(items) == 0 {
				return false
			}
			for _, item := range items {
				if !nativeSchemaSyntax(item, depth+1) {
					return false
				}
			}
		case "required":
			if !stringsOnly(value) {
				return false
			}
		case "dependentRequired":
			children, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for _, child := range children {
				if !stringsOnly(child) {
					return false
				}
			}
		case "enum":
			items, ok := value.([]any)
			if !ok || len(items) == 0 {
				return false
			}
		case "minLength", "maxLength", "minItems", "maxItems", "minContains", "maxContains", "minProperties", "maxProperties":
			number, ok := value.(float64)
			if !ok || number < 0 || number != float64(int64(number)) {
				return false
			}
		case "minimum", "maximum", "multipleOf":
			number, ok := value.(float64)
			if !ok || key == "multipleOf" && number <= 0 {
				return false
			}
		case "exclusiveMinimum", "exclusiveMaximum":
			if _, ok := value.(float64); !ok {
				if _, legacy := value.(bool); !legacy {
					return false
				}
			}
		case "uniqueItems", "deprecated", "readOnly", "writeOnly":
			if _, ok := value.(bool); !ok {
				return false
			}
		}
	}
	return true
}

func nativePrompt(request Request, images []imagecontext.Image, messageID string, maxInput int) ([]byte, error) {
	files := make([]map[string]string, 0, len(images))
	for i, image := range images {
		files = append(files, map[string]string{"uri": "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data), "name": fmt.Sprintf("context-%d", i+1), "description": image.Label})
	}
	prompt, err := json.Marshal(map[string]any{"id": messageID, "text": request.Prompt, "files": files})
	if err != nil || len(prompt) > encodedInputLimit(maxInput, images) {
		return nil, ErrInputLimit
	}
	return prompt, nil
}

// PreflightImages validates the exact encoded batch and freshly queries the
// selected model's metadata without submitting note content or inference. Any
// rejection lets Service retain all images and continue with a text-only note.
// Complete still independently refuses unsupported images; it never drops them.
func (p *OpenCodeNativeProvider) PreflightImages(parent context.Context, request Request) (preflightErr error) {
	if len(request.Images) == 0 {
		return nil
	}
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return err
	}
	defer release()
	images, _, err := prepareNativeImages(ctx, request.Images)
	if err != nil {
		return err
	}
	if _, err := nativeInstructions(request, p.options.MaxInputBytes); err != nil {
		return err
	}
	// Actual IDs contain a 48-character random suffix; use precisely that size.
	if _, err := nativePrompt(request, images, "msg_ginkgo_"+strings.Repeat("0", 48), p.options.MaxInputBytes); err != nil {
		return err
	}
	if err := p.start(ctx); err != nil {
		return err
	}
	defer func() {
		if preflightErr != nil {
			p.stopRuntime()
		}
	}()
	model, variant := p.requestModel(request)
	// Do not trust the registry's five-minute cache or our thirty-second cache
	// at the image submission boundary: defaults and aliases can change.
	delete(p.cache, model+"#"+variant)
	resolved, err := p.resolve(ctx, model, variant)
	if err != nil {
		return err
	}
	if resolved.caps.ImageInput != Supported {
		return fmt.Errorf("opencode native image input is %s", resolved.caps.ImageInput)
	}
	return nil
}

// Complete submits text and the entire validated image batch to a fresh,
// tool-free V2 session. ImagesSubmitted distinguishes known non-submission,
// ambiguous prompt admission, and confirmed image-bearing prompt admission.
func (p *OpenCodeNativeProvider) Complete(parent context.Context, request Request) (response Response, completionErr error) {
	submitted := false
	transformed := false
	var submission *bool = &submitted
	// Run last, preserving attribution even if another defer clears response.
	defer func() {
		response.ImagesSubmitted = submission
		response.ImagesTransformed = transformed
	}()
	system, err := nativeInstructions(request, p.options.MaxInputBytes)
	if err != nil {
		return Response{}, err
	}
	ctx, release, err := p.acquire(parent)
	if err != nil {
		return Response{}, err
	}
	defer release()
	images, transformed, err := prepareNativeImages(ctx, request.Images)
	if err != nil {
		return Response{}, err
	}
	id, err := newOpenCodeSessionID()
	if err != nil {
		return Response{}, err
	}
	messageID := "msg_ginkgo_" + strings.TrimPrefix(id, "ses_ginkgo_")
	prompt, err := nativePrompt(request, images, messageID, p.options.MaxInputBytes)
	if err != nil {
		return Response{}, err
	}
	if err := p.start(ctx); err != nil {
		return Response{}, err
	}
	defer func() {
		if completionErr != nil {
			p.stopRuntime()
		}
	}()
	model, variant := p.requestModel(request)
	if len(images) > 0 {
		delete(p.cache, model+"#"+variant)
	}
	resolved, err := p.resolve(ctx, model, variant)
	if err != nil {
		return Response{}, err
	}
	if len(images) > 0 && resolved.caps.ImageInput != Supported {
		return Response{}, fmt.Errorf("opencode native image input is %s", resolved.caps.ImageInput)
	}
	if err := p.setSystem(ctx, system); err != nil {
		return Response{}, err
	}
	// Reset only after the session is deleted (the deletion defer below runs
	// first). Fail closed if reset/reload cannot be confirmed: the later runtime
	// teardown removes the private config rather than reusing stale instructions.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := p.setSystem(cleanup, ""); err != nil && completionErr == nil {
			response, completionErr = Response{}, fmt.Errorf("reset private native instructions failed")
		}
	}()
	// Cleanup even ambiguous creation, but never retry creation/admission.
	p.pendingCleanup[id] = struct{}{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if completionErr != nil {
			_ = p.call(cleanup, "POST", "/api/session/"+id+"/interrupt", nil, 200, nil)
		}
		if err := deleteOpenCodeSession(p.runtime.server.endpoint, p.runtime.password, id); err != nil {
			if completionErr == nil {
				response, completionErr = Response{}, fmt.Errorf("delete private native session failed")
			}
		} else {
			delete(p.pendingCleanup, id)
		}
	}()
	var created struct {
		Data *struct {
			ID          string `json:"id"`
			Agent       string `json:"agent"`
			Permissions []struct {
				Action   string `json:"action"`
				Resource string `json:"resource"`
				Effect   string `json:"effect"`
			} `json:"permissions"`
		} `json:"data"`
	}
	err = p.call(ctx, "POST", "/api/session", map[string]any{"id": id, "title": "GinkGo ephemeral generation", "agent": openCodeSessionAgent, "model": resolved.ref, "location": map[string]string{"directory": p.runtime.dir}, "permissions": denyAllOpenCodePermissions()}, 200, &created)
	if err != nil {
		return Response{}, err
	}
	if created.Data == nil || created.Data.ID != id || created.Data.Agent != openCodeSessionAgent || len(created.Data.Permissions) != 1 {
		return Response{}, ErrProtocol
	}
	rule := created.Data.Permissions[0]
	if rule.Action != "*" || rule.Resource != "*" || rule.Effect != "deny" {
		return Response{}, ErrProtocol
	}
	// Match durable admission to our client-owned input ID before waiting.
	var admitted struct {
		Data *struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Type      string `json:"type"`
		} `json:"data"`
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if len(images) > 0 {
		submission = nil
	}
	if err := p.call(ctx, "POST", "/api/session/"+id+"/prompt", json.RawMessage(prompt), 200, &admitted); err != nil {
		var rejection *openCodeNativeHTTPError
		if errors.As(err, &rejection) {
			if rejection.status >= 400 && rejection.status < 500 {
				submission = &submitted
			}
			return Response{}, &nativePromptHTTPFailure{status: rejection.status, cause: err}
		}
		return Response{}, err
	}
	if admitted.Data == nil || admitted.Data.ID != messageID || admitted.Data.SessionID != id || admitted.Data.Type != "user" {
		return Response{}, ErrProtocol
	}
	submitted = len(images) > 0
	submission = &submitted
	if err := p.call(ctx, "POST", "/api/experimental/session/"+id+"/wait", nil, 204, nil); err != nil {
		return Response{}, err
	}
	var settled struct {
		Data *struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"data"`
	}
	if err := p.call(ctx, "GET", "/api/session/"+id, nil, 200, &settled); err != nil {
		return Response{}, err
	}
	if settled.Data == nil || settled.Data.ID != id {
		return Response{}, ErrProtocol
	}
	var transcript struct {
		Data []openCodeNativeMessage `json:"data"`
	}
	// Never retrieve the user transcript: it includes all base64 image bytes.
	// Fresh private session + acknowledged client input + settled successful
	// outcome attributes this newest assistant to exactly our one admitted input.
	if err := p.call(ctx, "GET", "/api/session/"+id+"/message?type=assistant&order=desc&limit=1", nil, 200, &transcript); err != nil {
		return Response{}, err
	}
	if settled.Data.Outcome != "succeeded" {
		if len(transcript.Data) == 1 {
			m := transcript.Data[0]
			if m.Type != "assistant" || m.Agent != openCodeSessionAgent || m.Model != resolved.ref {
				return Response{}, ErrProtocol
			}
			if err := nativeAssistantError(m.Error); err != nil {
				return Response{}, err
			}
		}
		return Response{}, ErrProviderUnavailable
	}
	return nativeFinalResponse(transcript.Data, resolved.ref)
}

// V2 Session.StructuredError and core/session/to-session-error.ts (2.0.31)
// supply stable error types and optional HTTP status. Never return message or
// response.body: both may contain note content, credentials or provider output.
func nativeAssistantError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var failure struct {
		Type    string  `json:"type"`
		Message *string `json:"message"`
		Status  *int    `json:"status"`
	}
	if json.Unmarshal(raw, &failure) != nil || failure.Type == "" || failure.Message == nil || failure.Status != nil && (*failure.Status < 100 || *failure.Status > 599) {
		return ErrProtocol
	}
	switch failure.Type {
	case "provider.auth":
		return ErrAuthentication
	case "provider.rate-limit", "provider.quota":
		return ErrRateLimited
	case "provider.no-route":
		return ErrModelUnavailable
	case "provider.invalid-output":
		return ErrProtocol
	}
	if failure.Status != nil {
		switch *failure.Status {
		case 401, 403:
			return ErrAuthentication
		case 429:
			return ErrRateLimited
		}
	}
	return ErrProviderUnavailable
}

type openCodeNativeMessage struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Agent  string          `json:"agent"`
	Model  modelRef        `json:"model"`
	Finish string          `json:"finish"`
	Error  json.RawMessage `json:"error"`
	Time   struct {
		Completed *float64 `json:"completed"`
	} `json:"time"`
	Content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
}

// The endpoint applies type filtering before pagination. Accept exactly the
// latest completed assistant, never older retries, user text or tool output.
func nativeFinalResponse(messages []openCodeNativeMessage, model modelRef) (Response, error) {
	if len(messages) != 1 {
		return Response{}, ErrProtocol
	}
	final := &messages[0]
	if final.Type != "assistant" || final.Agent != openCodeSessionAgent || final.Model != model || final.Time.Completed == nil {
		return Response{}, ErrProtocol
	}
	if err := nativeAssistantError(final.Error); err != nil {
		return Response{}, err
	}
	for _, part := range final.Content {
		if part.Type != "text" && part.Type != "reasoning" {
			return Response{}, ErrProtocol
		}
	}
	if final.Finish != "stop" {
		return Response{}, ErrProtocol
	}
	var text strings.Builder
	for _, part := range final.Content {
		if part.Type == "text" {
			if part.Text == nil {
				return Response{}, ErrProtocol
			}
			text.WriteString(*part.Text)
		}
	}
	if text.Len() == 0 {
		return Response{}, ErrProtocol
	}
	return Response{Text: text.String()}, nil
}

var _ Provider = (*OpenCodeNativeProvider)(nil)
var _ CapabilityProvider = (*OpenCodeNativeProvider)(nil)
var _ ImageRequestPreflighter = (*OpenCodeNativeProvider)(nil)
var _ io.Closer = (*OpenCodeNativeProvider)(nil)
