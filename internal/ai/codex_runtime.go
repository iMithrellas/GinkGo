//go:build linux

package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type codexMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	bytes int
}
type codexRead struct {
	message codexMessage
	err     error
}
type codexWrite struct {
	data []byte
	done chan error
}
type codexRuntime struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	cancel      context.CancelFunc
	reads       chan codexRead
	writes      chan codexWrite
	done        chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	exited      chan struct{}
	stderr      *limitedBuffer
	sequence    int64
	budget      int
	limit       int
	budgetLimit int
	expected    map[string]any
	root        string
	accountType string
}

func codexStartRuntime(ctx context.Context, executable string, args []string, root string, env []string, limit int) (runtime *codexRuntime, err error) {
	ctx, startupCancel := context.WithTimeout(ctx, 10*time.Second)
	defer startupCancel()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	r := &codexRuntime{cancel: cancel, reads: make(chan codexRead, 16), writes: make(chan codexWrite), done: make(chan struct{}), exited: make(chan struct{}), limit: max(limit, 1<<20)}
	r.root = root
	r.budgetLimit = r.limit
	r.expected, err = codexExpectedConfig(args)
	if err != nil {
		cancel()
		return nil, err
	}
	r.cmd = command(life, executable, args, root, env, nil)
	r.stderr = &limitedBuffer{limit: 64 << 10, cancel: cancel}
	r.cmd.Stderr = r.stderr
	r.stdin, err = r.cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, ErrProviderUnavailable
	}
	r.stdout, err = r.cmd.StdoutPipe()
	if err != nil {
		r.stdin.Close()
		cancel()
		return nil, ErrProviderUnavailable
	}
	if err = r.cmd.Start(); err != nil {
		r.stdin.Close()
		r.stdout.Close()
		cancel()
		return nil, ErrProviderUnavailable
	}
	go func() { _ = r.cmd.Wait(); close(r.exited) }()
	r.wg.Add(2)
	go r.readLoop()
	go r.writeLoop()
	defer func() {
		if err != nil {
			r.stop()
		}
	}()
	var initialized struct {
		UserAgent string `json:"userAgent"`
		Home      string `json:"codexHome"`
		OS        string `json:"platformOs"`
	}
	err = r.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "ginkgo", "version": "1.0.0"}, "capabilities": map[string]any{"experimentalApi": true}}, &initialized, nil)
	if err != nil {
		return nil, err
	}
	identity := strings.Fields(initialized.UserAgent)
	if len(identity) == 0 || !strings.HasSuffix(identity[0], "/0.163.0") {
		return nil, &CodexIsolationError{Reason: "app-server handshake does not identify the audited native version"}
	}
	expectedHome := ""
	for _, value := range env {
		if strings.HasPrefix(value, "CODEX_HOME=") {
			expectedHome = strings.TrimPrefix(value, "CODEX_HOME=")
		}
	}
	if initialized.Home != expectedHome || initialized.OS != "linux" {
		return nil, &CodexIsolationError{Reason: "app-server did not preserve the exact native profile and Linux runtime contract"}
	}
	if err = r.send(ctx, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err = r.verifyAccount(ctx); err != nil {
		return nil, err
	}
	if err = r.verifyPolicy(ctx); err != nil {
		return nil, err
	}
	r.resetBudget()
	return r, nil
}

// Read only public account metadata, never login/start(chatgptAuthTokens) or
// account token refresh callbacks. Saved-login refresh remains native Codex's
// responsibility. No plan allowlist: policy compatibility, not subscription
// family, determines whether a generation can start.
func (r *codexRuntime) verifyAccount(ctx context.Context) error {
	var response struct {
		RequiresAuth *bool `json:"requiresOpenaiAuth"`
		Account      *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := r.call(ctx, "account/read", map[string]any{"refreshToken": false}, &response, nil); err != nil {
		return err
	}
	if response.RequiresAuth == nil {
		return ErrProtocol
	}
	// The native loopback audit explicitly configures an auth-free custom
	// provider. Production codexArgs always selects openai and never takes this
	// branch; do not infer an auth bypass from account/read alone.
	provider, _ := r.expected["model_provider"].(string)
	providers, _ := r.expected["model_providers"].(map[string]any)
	custom, _ := providers[provider].(map[string]any)
	if provider != "" && provider != "openai" && custom["requires_openai_auth"] == false && !*response.RequiresAuth {
		return nil
	}
	if !*response.RequiresAuth {
		return ErrProtocol
	}
	if response.Account == nil || (response.Account.Type != "apiKey" && response.Account.Type != "chatgpt") {
		return ErrAuthentication
	}
	r.accountType = response.Account.Type
	return nil
}

// Requirements have independent precedence over CLI configuration. Validate
// recognized constraints against the exact controls we will actually use; never
// remove them or retry with a more permissive launch. Unknown non-null policy
// needs additional auditing, not an auth-plan gate or a policy bypass.
func (r *codexRuntime) compatibleRequirements(raw json.RawMessage) bool {
	if string(raw) == "null" {
		return true
	}
	var requirements map[string]any
	if json.Unmarshal(raw, &requirements) != nil || requirements == nil {
		return false
	}
	contains := func(value any, wanted string) bool {
		values, ok := value.([]any)
		if !ok {
			return false
		}
		found := false
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return false
			}
			found = found || text == wanted
		}
		return found
	}
	for key, value := range requirements {
		if value == nil {
			continue
		}
		switch key {
		case "allowedApprovalPolicies":
			// Unlike the other constraint lists, native approval policies also
			// admit a granular object. Validate it before normalizing to a
			// nonmatching string; it cannot satisfy our required never policy.
			values, ok := value.([]any)
			if !ok {
				return false
			}
			normalized := make([]any, 0, len(values))
			for _, item := range values {
				if text, ok := item.(string); ok {
					normalized = append(normalized, text)
					continue
				}
				policy, ok := item.(map[string]any)
				if !ok || len(policy) != 1 {
					return false
				}
				granular, ok := policy["granular"].(map[string]any)
				if !ok {
					return false
				}
				for _, required := range []string{"sandbox_approval", "rules", "mcp_elicitations"} {
					if _, ok := granular[required].(bool); !ok {
						return false
					}
				}
				for field, setting := range granular {
					switch field {
					case "sandbox_approval", "rules", "mcp_elicitations", "skill_approval", "request_permissions":
						if _, ok := setting.(bool); !ok {
							return false
						}
					default:
						return false
					}
				}
				normalized = append(normalized, "granular")
			}
			if !contains(normalized, "never") {
				return false
			}
		case "allowedSandboxModes":
			if !contains(value, "read-only") {
				return false
			}
		case "allowedWebSearchModes":
			if !contains(value, "disabled") {
				return false
			}
		case "allowedLoginMethods":
			method := r.accountType
			if method == "apiKey" {
				method = "api"
			}
			if method == "" || !contains(value, method) {
				return false
			}
		case "featureRequirements":
			features, ok := value.(map[string]any)
			if !ok || !codexConfigContains(r.expected["features"], features) {
				return false
			}
		case "modelProvider":
			if !reflect.DeepEqual(value, r.expected["model_provider"]) {
				return false
			}
		case "modelProviders":
			providers, ok := value.(map[string]any)
			if !ok || (len(providers) != 0 && !reflect.DeepEqual(value, r.expected["model_providers"])) {
				return false
			}
		case "chatgptBaseUrl":
			if value != "https://chatgpt.com/backend-api/" {
				return false
			}
		case "modelCatalogJson", "checkForUpdateOnStartup":
			configKey := "model_catalog_json"
			if key == "checkForUpdateOnStartup" {
				configKey = "check_for_update_on_startup"
			}
			if !reflect.DeepEqual(value, r.expected[configKey]) {
				return false
			}
		case "allowBrowserAndComputerUse", "allowAppshots", "allowRemoteControl", "allowLoginShell", "allowManagedHooksOnly":
			// Permission to use a facility is not a requirement to enable it.
			// Required hooks/features are independently checked; effective
			// configuration and the sanitized catalog still remove tools.
			if _, ok := value.(bool); !ok {
				return false
			}
		case "additionalDeveloperInstructions":
			if value != "" {
				return false
			}
		case "cliAuthCredentialsStore":
			// Dedicated profiles use the native default file store. A required
			// keyring/ephemeral store needs its own lifecycle audit.
			if value != "file" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (r *codexRuntime) verifyPolicy(ctx context.Context) error {
	var requirements struct {
		Requirements json.RawMessage `json:"requirements"`
	}
	if err := r.call(ctx, "configRequirements/read", map[string]any{}, &requirements, nil); err != nil {
		return err
	}
	if len(requirements.Requirements) == 0 {
		return ErrProtocol
	}
	if !r.compatibleRequirements(requirements.Requirements) {
		return &CodexIsolationError{Reason: "native managed requirements cannot be verified compatible with generation-only controls; no generation thread was started; ask the workspace administrator for compatible policy (Ginkgo will not bypass it)"}
	}
	var config struct {
		Config map[string]any `json:"config"`
		Layers []struct {
			Name struct {
				Type string `json:"type"`
			} `json:"name"`
			Config map[string]any `json:"config"`
		} `json:"layers"`
	}
	if err := r.call(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": r.root}, &config, nil); err != nil {
		return err
	}
	if config.Layers == nil {
		return ErrProtocol
	}
	flags := 0
	for _, layer := range config.Layers {
		switch layer.Name.Type {
		case "sessionFlags":
			flags++
			if !reflect.DeepEqual(layer.Config, r.expected) {
				return &CodexIsolationError{Reason: "native session flags do not match the pinned isolation contract"}
			}
		case "user", "project", "system", "packagedDefaults":
			if len(layer.Config) > 0 {
				return &CodexIsolationError{Reason: "native external configuration layer is not empty"}
			}
		case "enterpriseManaged":
			// Preserve managed policy only when every supplied value already
			// matches an explicit restrictive control. No unknown instructions,
			// plugins or routes; no permissive policy override or fallback.
			if !codexConfigContains(r.expected, layer.Config) {
				return &CodexIsolationError{Reason: "native workspace-managed configuration is incompatible or unverified for generation-only controls; no generation thread was started; ask the workspace administrator for compatible policy"}
			}
		default:
			return &CodexIsolationError{Reason: "native unmanaged or enterprise configuration layer cannot be isolated"}
		}
	}
	if flags != 1 || config.Config == nil {
		return ErrProtocol
	}
	for _, key := range []string{"model_provider", "web_search", "project_doc_max_bytes", "project_root_markers", "developer_instructions", "personality", "model_catalog_json", "thread_unload_delay_secs", "check_for_update_on_startup", "mcp_servers", "plugins", "skills"} {
		want, exists := r.expected[key]
		if exists && !codexConfigContains(config.Config[key], want) {
			return &CodexIsolationError{Reason: "native effective settings differ from the pinned isolation contract"}
		}
	}
	if !codexConfigContains(config.Config["features"], r.expected["features"]) {
		return &CodexIsolationError{Reason: "native effective feature controls differ from the pinned isolation contract"}
	}
	wantProviders, _ := r.expected["model_providers"].(map[string]any)
	providers, _ := config.Config["model_providers"].(map[string]any)
	if len(providers) != len(wantProviders) {
		return &CodexIsolationError{Reason: "native provider routes are not the explicitly configured routes"}
	}
	for key, want := range wantProviders {
		if !codexConfigContains(providers[key], want) {
			return &CodexIsolationError{Reason: "native provider endpoint or authentication routing changed"}
		}
	}
	if value := config.Config["chatgpt_base_url"]; value != nil && value != "https://chatgpt.com/backend-api/" {
		return &CodexIsolationError{Reason: "native ChatGPT control-plane route is unexpected"}
	}
	for _, key := range []string{"instructions", "model_instructions_file", "experimental_compact_prompt_file", "notify", "hooks", "cloud", "mcp_enterprise_managed_auth", "orchestrator", "profiles", "openai_base_url", "browser_use", "computer_use", "desktop", "js_repl_node_path", "js_repl_node_module_dirs", "experimental_thread_store", "experimental_thread_store_endpoint", "forced_chatgpt_workspace_id", "forced_login_method", "profile"} {
		value := config.Config[key]
		if value != nil && value != "" {
			if m, ok := value.(map[string]any); !ok || len(m) > 0 {
				return &CodexIsolationError{Reason: "native ambient instructions, hooks, cloud or environment settings are present"}
			}
		}
	}
	return nil
}

func codexConfigContains(got, want any) bool {
	if m, ok := want.(map[string]any); ok {
		actual, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range m {
			if !codexConfigContains(actual[key], value) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(got, want)
}
func codexExpectedConfig(args []string) (map[string]any, error) {
	expected := map[string]any{}
	set := func(key string, value any) {
		parts := strings.Split(key, ".")
		m := expected
		for _, part := range parts[:len(parts)-1] {
			child, ok := m[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				m[part] = child
			}
			m = child
		}
		m[parts[len(parts)-1]] = value
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			i++
			if i >= len(args) {
				return nil, ErrProtocol
			}
			pair := strings.SplitN(args[i], "=", 2)
			if len(pair) != 2 {
				return nil, ErrProtocol
			}
			var value map[string]any
			if toml.Unmarshal([]byte("value="+pair[1]), &value) != nil {
				return nil, ErrProtocol
			}
			set(pair[0], value["value"])
		case "--disable", "--enable":
			enabled := args[i] == "--enable"
			i++
			if i >= len(args) {
				return nil, ErrProtocol
			}
			set("features."+args[i], enabled)
		}
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return nil, ErrProtocol
	}
	if json.Unmarshal(encoded, &expected) != nil {
		return nil, ErrProtocol
	}
	return expected, nil
}

func (r *codexRuntime) readLoop() {
	defer r.wg.Done()
	defer close(r.reads)
	scanner := bufio.NewScanner(r.stdout)
	scanner.Buffer(make([]byte, 4096), r.limit+1)
	for scanner.Scan() {
		var m codexMessage
		data := scanner.Bytes()
		err := json.Unmarshal(data, &m)
		m.bytes = len(data) + 1
		if err != nil || (m.Method == "" && len(m.ID) == 0) {
			err = ErrProtocol
		}
		select {
		case r.reads <- codexRead{m, err}:
		case <-r.done:
			return
		}
		if err != nil {
			return
		}
	}
	err := ErrProviderUnavailable
	if scanner.Err() != nil {
		err = ErrOutputLimit
	}
	select {
	case r.reads <- codexRead{err: err}:
	case <-r.done:
	}
}

func (r *codexRuntime) writeLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			return
		case write := <-r.writes:
			_, err := r.stdin.Write(write.data)
			write.done <- err
			if err != nil {
				return
			}
		}
	}
}
func (r *codexRuntime) send(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrProtocol
	}
	data = append(data, '\n')
	write := codexWrite{data: data, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return ErrProviderUnavailable
	case r.writes <- write:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return ErrProviderUnavailable
	case err := <-write.done:
		if err != nil {
			return ErrProviderUnavailable
		}
		return nil
	}
}
func (r *codexRuntime) resetBudget() { r.budget = 0 }
func (r *codexRuntime) next(ctx context.Context) (codexMessage, error) {
	select {
	case <-ctx.Done():
		return codexMessage{}, ctx.Err()
	case read, ok := <-r.reads:
		if !ok {
			return codexMessage{}, ErrProviderUnavailable
		}
		if read.err != nil {
			if r.stderr.exceeded() {
				return codexMessage{}, ErrOutputLimit
			}
			return codexMessage{}, read.err
		}
		r.budget += read.message.bytes
		if r.budget > r.budgetLimit {
			return codexMessage{}, ErrOutputLimit
		}
		if read.message.Method != "" && len(read.message.ID) > 0 {
			return codexMessage{}, &CodexIsolationError{Reason: "native runtime requested client tool execution or approval"}
		}
		if read.message.Method == "configWarning" || read.message.Method == "config/warning" || read.message.Method == "warning" {
			return codexMessage{}, &CodexIsolationError{Reason: "native runtime emitted a configuration warning"}
		}
		if read.message.Method == "model/rerouted" {
			return codexMessage{}, ErrModelUnavailable
		}
		if read.message.Method == "hook/started" || read.message.Method == "hook/completed" {
			return codexMessage{}, &CodexIsolationError{Reason: "native runtime unexpectedly executed a hook"}
		}
		return read.message, nil
	}
}
func (r *codexRuntime) call(ctx context.Context, method string, params any, result any, event func(codexMessage) error) error {
	r.sequence++
	id := r.sequence
	if err := r.send(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		m, err := r.next(ctx)
		if err != nil {
			return err
		}
		if len(m.ID) == 0 {
			if event != nil {
				if err = event(m); err != nil {
					return err
				}
			}
			continue
		}
		var responseID int64
		if json.Unmarshal(m.ID, &responseID) != nil || responseID != id {
			return ErrProtocol
		}
		if m.Error != nil {
			return codexRPCError(m.Error.Code, m.Error.Message)
		}
		if len(m.Result) == 0 {
			return ErrProtocol
		}
		if result != nil && json.Unmarshal(m.Result, result) != nil {
			return ErrProtocol
		}
		return nil
	}
}
func codexRPCError(code int, message string) error {
	message = strings.ToLower(message)
	switch {
	case code == 401 || code == 403:
		return ErrAuthentication
	case code == 429:
		return ErrRateLimited
	case strings.Contains(message, "auth") || strings.Contains(message, "unauthorized") || strings.Contains(message, "login"):
		return ErrAuthentication
	case strings.Contains(message, "rate limit") || strings.Contains(message, "quota"):
		return ErrRateLimited
	case strings.Contains(message, "model") && (strings.Contains(message, "not found") || strings.Contains(message, "unavailable") || strings.Contains(message, "unsupported")):
		return ErrModelUnavailable
	case code == -32600 || code == -32601 || code == -32602:
		return ErrProtocol
	default:
		return ErrProviderUnavailable
	}
}
func (r *codexRuntime) stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.done)
		r.cancel()
		_ = killGroup(r.cmd)
		_ = r.stdin.Close()
		_ = r.stdout.Close()
		<-r.exited
		r.wg.Wait()
	})
}

type codexAgentItem struct {
	Type  string  `json:"type"`
	ID    string  `json:"id"`
	Text  string  `json:"text"`
	Phase *string `json:"phase"`
}
type codexTurnState struct {
	thread string
	turn   string
	done   bool
	status string
	items  []codexAgentItem
	limit  int
	size   int
	err    error
}

func (s *codexTurnState) event(m codexMessage) error {
	if m.Method == "model/rerouted" {
		return ErrModelUnavailable
	}
	if m.Method == "hook/started" || m.Method == "hook/completed" {
		return &CodexIsolationError{Reason: "native runtime unexpectedly executed a hook"}
	}
	var params struct {
		Thread string         `json:"threadId"`
		TurnID string         `json:"turnId"`
		Item   codexAgentItem `json:"item"`
		Turn   struct {
			ID     string           `json:"id"`
			Status string           `json:"status"`
			Items  []codexAgentItem `json:"items"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		WillRetry bool `json:"willRetry"`
	}
	if json.Unmarshal(m.Params, &params) != nil {
		return ErrProtocol
	}
	if params.Thread != "" && params.Thread != s.thread {
		return nil
	}
	if params.TurnID != "" && s.turn != "" && params.TurnID != s.turn {
		return ErrProtocol
	}
	switch m.Method {
	case "turn/started":
		if s.turn != "" && s.turn != params.Turn.ID {
			return ErrProtocol
		}
		s.turn = params.Turn.ID
	case "item/started", "item/completed":
		switch params.Item.Type {
		case "agentMessage", "reasoning", "userMessage":
		default:
			return &CodexIsolationError{Reason: "native generation emitted a tool or action item"}
		}
		if m.Method == "item/completed" && params.Item.Type == "agentMessage" {
			return s.add(params.Item)
		}
	case "error":
		if !params.WillRetry {
			return codexRPCError(0, params.Error.Message)
		}
	case "turn/completed":
		if s.turn != "" && params.Turn.ID != s.turn {
			return ErrProtocol
		}
		s.turn = params.Turn.ID
		s.done = true
		s.status = params.Turn.Status
		if params.Turn.Error != nil {
			s.err = codexRPCError(0, params.Turn.Error.Message)
		}
		for _, item := range params.Turn.Items {
			if item.Type == "agentMessage" {
				if err := s.add(item); err != nil {
					return err
				}
			} else if item.Type != "reasoning" && item.Type != "userMessage" {
				return &CodexIsolationError{Reason: "completed native turn contains a tool or action item"}
			}
		}
	}
	return nil
}
func (s *codexTurnState) add(item codexAgentItem) error {
	if item.ID == "" {
		return ErrProtocol
	}
	for _, old := range s.items {
		if old.ID == item.ID {
			if old.Text != item.Text {
				return ErrProtocol
			}
			return nil
		}
	}
	s.size += len(item.Text)
	if s.size > s.limit {
		return ErrOutputLimit
	}
	s.items = append(s.items, item)
	return nil
}
func (s *codexTurnState) result() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if !s.done || s.status != "completed" {
		if s.status == "interrupted" {
			return "", context.Canceled
		}
		return "", ErrProviderUnavailable
	}
	var final []string
	lastUnknown := ""
	for _, item := range s.items {
		if item.Phase == nil {
			lastUnknown = item.Text
		} else if *item.Phase == "final_answer" {
			final = append(final, item.Text)
		} else {
			lastUnknown = ""
		}
	}
	if len(final) > 0 {
		return strings.Join(final, "\n"), nil
	}
	if lastUnknown != "" {
		return lastUnknown, nil
	}
	return "", errors.Join(ErrProtocol, errors.New("codex completed without a final agent message"))
}
