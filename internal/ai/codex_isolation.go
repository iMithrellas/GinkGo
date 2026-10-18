//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Runtime contract: upstream 83086188ae0614bd882818ff1aa23f3194962012.
// models-manager StaticModelsManager retains native AuthManager but never
// refreshes the supplied catalog. tools/spec_plan.rs derives apply_patch, clock,
// async-message and shell tools from catalog fields even with features disabled.
// Therefore both a sanitized static catalog AND explicit features are required.
// ext/skills/src/host_roots.rs discovers CODEX_HOME/skills, HOME/.agents/skills,
// /etc/codex/skills and repository .agents/skills. agents_md_manager.rs separately
// loads CODEX_HOME/AGENTS.md regardless of project_doc_max_bytes.
// app-server lacks exec's --ignore-user-config/--ignore-rules flags in 0.163.0;
// do not pass unsupported flags or use test-only loader overrides. Instead we
// require a clean dedicated profile, reject system configuration entirely, use a
// private HOME/cwd with a repository boundary and disable project discovery.
// Native ChatGPT auth intentionally retains provider-required managed-cloud
// loading. There is no supported pre-start disable in this pinned release.
// Account/config checks after initialization prevent incompatible generation,
// not earlier cloud requests or managed startup work. Plugin startup's normal
// native guard is PluginsConfigInput.plugins_enabled (core-plugins/manager.rs);
// our disabled feature is defense in depth, not proof against managed overrides.
// Official references: https://developers.openai.com/codex/app-server and
// https://developers.openai.com/codex/non-interactive-mode .

func codexPreflight(ctx context.Context, home string) error {
	fail := func(reason string) error { return &CodexIsolationError{Reason: reason} }
	if err := ctx.Err(); err != nil {
		return err
	}
	if home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return fail("auth_home must name an absolute private native profile; authenticate separately using CODEX_HOME=/absolute/private/profile codex login")
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil || real != home {
		return fail("auth_home must be an existing non-symlink private directory")
	}
	info, err := os.Lstat(home)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fail("auth_home must be a private directory (mode 0700)")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fail("auth_home must be owned by the current user")
	}
	if _, err := os.Lstat("/etc/codex"); !os.IsNotExist(err) {
		return fail("system /etc/codex discovery root is present or inaccessible; managed configuration and system skills cannot be isolated")
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		return fail("cannot inspect native profile discovery roots")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		// Only opaque auth and audited native runtime artifacts are allowed.
		// In particular NO config, instructions, skills, rules, plugins, hooks,
		// environments are accepted. The exact native signed cloud cache is an
		// exception: Codex verifies its account binding/signature/expiry; Ginkgo
		// never parses it and verifies delivered effective policy before a turn.
		allowed := name == "auth.json" || name == "cloud-config-bundle-cache.json" || name == "models_cache.json" || name == "version.json" || name == "log" || name == "tmp" || name == ".tmp" || name == ".sandbox_migration" || name == "installation_id"
		for _, base := range []string{"state_5.sqlite", "logs_2.sqlite", "goals_1.sqlite", "memories_1.sqlite", "queue_1.sqlite"} {
			if name == base || name == base+"-wal" || name == base+"-shm" {
				allowed = true
			}
		}
		if !allowed {
			return fail("auth_home contains non-authentication discovery content; use a dedicated clean native profile")
		}
		item, err := entry.Info()
		if err != nil || item.Mode()&os.ModeSymlink != 0 || (!item.Mode().IsRegular() && !item.IsDir()) || item.Mode().Perm()&0022 != 0 {
			return fail("native profile contains inaccessible or symlinked entries")
		}
		if (name == "auth.json" || name == "cloud-config-bundle-cache.json") && (!item.Mode().IsRegular() || item.Mode().Perm()&0077 != 0) {
			return fail("native auth and cloud-cache artifacts must be private regular files; credentials are never read by Ginkgo")
		}
	}
	return nil
}

func codexEnvironment(home, root string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + root, "CODEX_HOME=" + home, "TMPDIR=" + root, "XDG_CONFIG_HOME=" + root, "XDG_DATA_HOME=" + root, "XDG_CACHE_HOME=" + root, "RUST_LOG=error"}
}

// login status loads config with an unbound default CloudConfigBundleLoader;
// it does NOT construct app-server or its managed-cloud/plugin startup service.
// cli/src/login.rs:447-510,591-613 has no --json or plan metadata. Accept the
// pinned native saved ChatGPT and API-key statuses, not externally supplied
// tokens. This is a login check, NOT a managed-startup isolation check. Never
// return/log status output (it can include a masked key), extract or replay it.
func codexVerifyNativeAuth(ctx context.Context, executable, root string, env []string) error {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := &limitedBuffer{limit: 4096, cancel: cancel}
	defer func() { output.mu.Lock(); clear(output.data.Bytes()); output.data.Reset(); output.mu.Unlock() }()
	cmd := command(probe, executable, []string{"login", "status"}, root, env, nil)
	cmd.Stdout = output
	cmd.Stderr = output
	defer func() { _ = killGroup(cmd) }()
	err := cmd.Run()
	if output.exceeded() {
		return ErrOutputLimit
	}
	if probe.Err() != nil {
		return probe.Err()
	}
	if err != nil {
		return ErrAuthentication
	}
	data := output.bytes()
	defer clear(data)
	status := bytes.TrimSpace(data)
	if bytes.Count(status, []byte("\n")) != 0 || (!bytes.Equal(status, []byte("Logged in using ChatGPT")) && !bytes.HasPrefix(status, []byte("Logged in using an API key - "))) {
		return ErrAuthentication
	}
	return nil
}

func codexVerifyVersion(ctx context.Context, name, root string, env []string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("codex executable unavailable: %w", ErrProviderUnavailable)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", ErrProviderUnavailable
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrProviderUnavailable
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return "", &CodexIsolationError{Reason: "Codex executable must be a trusted regular file without untrusted write permissions"}
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := &limitedBuffer{limit: 4096, cancel: cancel}
	cmd := command(probe, path, []string{"--version"}, root, env, nil)
	cmd.Stdout = output
	cmd.Stderr = output
	defer func() { _ = killGroup(cmd) }()
	err = cmd.Run()
	if output.exceeded() {
		return "", ErrOutputLimit
	}
	if probe.Err() != nil {
		return "", probe.Err()
	}
	if err != nil {
		return "", ErrProviderUnavailable
	}
	if strings.TrimSpace(string(output.bytes())) != "codex-cli 0.163.0" {
		return "", &CodexIsolationError{Reason: "only codex-cli 0.163.0 has an audited generation-only catalog and runtime contract; installed release is unsupported"}
	}
	return path, nil
}

func codexArgs(catalog string) []string {
	args := []string{"app-server", "--listen", "stdio://", "--strict-config"}
	config := []string{
		`model_provider="openai"`, `approval_policy="never"`, `sandbox_mode="read-only"`, `web_search="disabled"`,
		`tools.update_plan.enabled=false`, `tools.experimental_request_user_input.enabled=false`, `agents.enabled=false`,
		`project_doc_max_bytes=0`, `project_root_markers=[]`, `skills.include_instructions=false`, `skills.bundled.enabled=false`,
		`mcp_servers={}`, `plugins={}`, `analytics.enabled=false`, `feedback.enabled=false`,
		`developer_instructions=""`, `personality="none"`, `check_for_update_on_startup=false`,
		`thread_unload_delay_secs=0`,
		`suppress_unstable_features_warning=true`,
	}
	if catalog != "" {
		encoded, _ := json.Marshal(catalog)
		config = append(config, "model_catalog_json="+string(encoded))
	}
	for _, value := range config {
		args = append(args, "-c", value)
	}
	for _, feature := range []string{"goals", "shell_tool", "view_image", "sleep_tool", "request_permissions_tool", "deferred_executor", "send_message_to_user_async", "token_budget", "current_time_reminder", "image_generation", "apps", "plugins", "hooks", "memories", "code_mode", "code_mode_host", "multi_agent", "tool_suggest", "shell_snapshot", "remote_plugin"} {
		args = append(args, "--disable", feature)
	}
	args = append(args, "--enable", "skip_host_skill_discovery")
	return args
}

func codexWriteCatalog(root string, m codexModel) (string, error) {
	efforts := []any{}
	for _, e := range m.Efforts {
		efforts = append(efforts, map[string]any{"effort": e.Effort, "description": e.Description})
	}
	modalities := m.Modalities
	if modalities == nil {
		modalities = []string{"text"}
	} // Missing metadata must not acquire image support through native defaults.
	// Only model identity, modalities and advertised efforts come from discovery.
	// The remaining entries are local restrictive policy, NOT invented model
	// capabilities. No remote model_messages, tool metadata or instructions survive.
	model := map[string]any{
		"slug": m.Model, "display_name": m.DisplayName, "description": m.Description,
		"default_reasoning_level": m.DefaultEffort, "supported_reasoning_levels": efforts,
		"shell_type": "disabled", "visibility": "list", "supported_in_api": true, "priority": 0,
		"support_verbosity": false, "default_verbosity": nil, "apply_patch_tool_type": nil,
		"truncation_policy":            map[string]any{"mode": "bytes", "limit": 1048576},
		"experimental_supported_tools": []any{}, "input_modalities": modalities,
		"tool_mode": nil, "multi_agent_version": nil, "model_messages": map[string]any{"instructions_template": "Only satisfy the supplied generation request."},
		"include_skills_usage_instructions": false, "include_plugin_usage_instructions": false, "include_apps_usage_instructions": false,
		"supports_search_tool": false, "supports_experimental_context": false, "node_repl_disabled": true,
		"use_responses_lite": false,
	}
	if m.DefaultEffort == "" {
		model["default_reasoning_level"] = nil
	}
	data, err := json.Marshal(map[string]any{"models": []any{model}})
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "catalog.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}
