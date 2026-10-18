//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// This executable implements the CLI wire contract without any provider/network.
// Fixture paths are administrator-controlled, not inherited integration toggles.
func claudeFake(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for deterministic fake executable")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL"} {
		t.Setenv(key, "")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CLAUDE_CODE_USE_") {
			t.Setenv(key, "")
		}
	}
	for _, key := range []string{"ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"} {
		t.Setenv(key, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	path := filepath.Join(dir, "claude")
	script := `#!/usr/bin/env python3
import sys,os,json,time,subprocess
root=` + strconv.Quote(dir) + `
args=sys.argv[1:]
mode=open(root+'/mode').read() if os.path.exists(root+'/mode') else 'success'
data=sys.stdin.read()
system=''
if '--system-prompt-file' in args:
 system=open(args[args.index('--system-prompt-file')+1]).read()
with open(root+'/calls','a') as f:
 f.write(json.dumps({'args':args,'input':data,'cwd':os.getcwd(),'env':dict(os.environ),'system':system})+'\n')
if args==['--version']:
 if mode=='version-hang':
  child=subprocess.Popen(['sleep','300']);open(root+'/child','w').write(str(child.pid));time.sleep(300)
 print('2.1.298 (Claude Code)' if mode=='old' else '2.1.300 (Claude Code)' if mode=='future' else '2.1.299 (Claude Code)');sys.exit(0)
if args[-3:]==['auth','status','--json']:
 if mode=='status-hang':
  child=subprocess.Popen(['sleep','300']);open(root+'/child','w').write(str(child.pid));time.sleep(300)
 if mode=='status-overflow': print('x'*20000);sys.exit(0)
 if mode=='status-malformed': print('{SECRET');sys.exit(0)
 if os.path.exists(root+'/auth-status'):
  print(open(root+'/auth-status').read());sys.exit(0)
 method='oauth_token' if os.environ.get('ANTHROPIC_AUTH_TOKEN') else 'api_key' if os.environ.get('ANTHROPIC_API_KEY') else 'oauth_token' if os.environ.get('CLAUDE_CODE_OAUTH_TOKEN') else 'claude.ai'
 print(json.dumps({'loggedIn':True,'authMethod':method,'apiProvider':'firstParty','apiKeySource':'ANTHROPIC_API_KEY' if method=='api_key' else None,'subscriptionType':'max','configDirectory':os.environ['CLAUDE_CONFIG_DIR']}))
 if mode=='swap-after-status': os.replace(root+'/replacement',root+'/claude')
 sys.exit(0)
if 'ginkgo-initialize' in data:
 if mode=='native-managed-mcp-refusal':
  print('You cannot use --strict-mcp-config when an enterprise MCP config is present PRIVATE',file=sys.stderr);sys.exit(2)
 if mode=='initialize-conflicting-policy':
  open(os.environ['CLAUDE_CONFIG_DIR']+'/remote-settings.json','w').write('{"managedMcpServers":{"server":{"url":"https://private.invalid"}}}')
 if mode=='initialize-hang':
  child=subprocess.Popen(['sleep','300']);open(root+'/child','w').write(str(child.pid));time.sleep(300)
 if mode=='probe-fail': print('bad');sys.exit(0)
 if mode=='unresolved': print(json.dumps({'type':'control_response','response':{'subtype':'success','request_id':'ginkgo-initialize','response':{'models':[{'value':'default'},{'value':'sonnet'}]}}}));sys.exit(0)
 print(json.dumps({'type':'control_response','response':{'subtype':'success','request_id':'ginkgo-initialize','response':{'models':[{'value':'default','resolvedModel':'claude-exact-default'},{'value':'sonnet','resolvedModel':'claude-exact-sonnet'},{'value':'opus','resolvedModel':'claude-exact-opus'}]}}}));sys.exit(0)
if mode=='wait':
 open(root+'/started','w').write('1')
 while not os.path.exists(root+'/release'): time.sleep(.005)
if mode=='hang':
 child=subprocess.Popen(['sleep','300'])
 open(root+'/child','w').write(str(child.pid))
 time.sleep(300)
if mode=='overflow': print('x'*20000);sys.exit(0)
if mode=='partial': print(json.dumps({'type':'assistant','message':{'content':[{'type':'text','text':'not final'}]}}));sys.exit(0)
if mode=='malformed': print('{');sys.exit(0)
if mode=='notjson': print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'not JSON'}));sys.exit(0)
if mode=='tools': print(json.dumps({'type':'system','subtype':'init','tools':['Bash']}))
if mode in ['hook','managed-policy']: print(json.dumps({'type':'system','subtype':'hook_started'}))
schema='--json-schema' in args
tools=['StructuredOutput'] if schema else []
plugins=[{'name':'cc-plugin-agents-md','path':'builtin','source':'cc-plugin-agents-md@builtin'},{'name':'cc-plugin-plugin-authoring','path':'builtin','source':'cc-plugin-plugin-authoring@builtin'}]
if mode=='managed-policy': plugins.append({'name':'cc-plugin-sec-default','path':'builtin','source':'cc-plugin-sec-default@builtin'})
if mode=='custom-plugin': plugins=[{'name':'custom','path':'/user/plugin','source':'custom@user'}]
if mode=='forged-builtin': plugins[0]['path']='/user/plugin'
if mode=='unknown-builtin': plugins=[{'name':'new-builtin','path':'builtin','source':'new-builtin@builtin'}]
if mode=='extra-schema-tool': tools.append('Bash')
if mode=='schema-no-tool': tools=[]
if mode=='plain-formatter': tools=['StructuredOutput']
print(json.dumps({'type':'system','subtype':'init','tools':tools,'mcp_servers':[{'name':'custom','status':'connected'}] if mode=='mcp' else [],'skills':['custom'] if mode=='skill' else [],'slash_commands':[],'plugins':plugins,'agents':['claude','Explore','general-purpose','Plan']}))
print(json.dumps({'type':'system','subtype':'api_retry','error':'rate_limit'}))
print(json.dumps({'type':'stream_event','event':{'delta':{'text':'not final'}}}))
print(json.dumps({'type':'assistant','message':{'content':[{'type':'text','text':'not final'}]}}))
if mode in ['auth','rate','model','error']:
 print(json.dumps({'type':'result','subtype':'error_during_execution','is_error':True,'errors':[{'auth':'authentication_failed SECRET','rate':'rate_limit SECRET','model':'model_not_found SECRET','error':'SECRET'}[mode]]}));sys.exit(0)
if mode=='action-tool-call': print(json.dumps({'type':'assistant','message':{'content':[{'type':'tool_use','name':'Bash','input':{'command':'never execute'}}]}}))
if schema:
 print(json.dumps({'type':'assistant','message':{'content':[{'type':'tool_use','name':'StructuredOutput','input':{'ok':True}}]}}))
result={'type':'result','subtype':'success','is_error':False,'result':'not-the-structured-output' if schema else '{"ok":true}'}
if schema and mode!='schema-missing': result['structured_output']=None if mode=='schema-null' else {'ok':True}
if mode=='schema-denied':
 result.pop('structured_output',None);print(json.dumps({'type':'system','subtype':'permission_denied','tool_name':'StructuredOutput'}))
print(json.dumps(result))
if mode=='truncated': print('{')
if mode=='duplicate': print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'second'}))
if mode=='nonzero': sys.exit(2)
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

type claudeCall struct {
	Args   []string          `json:"args"`
	Input  string            `json:"input"`
	CWD    string            `json:"cwd"`
	Env    map[string]string `json:"env"`
	System string            `json:"system"`
}

func claudeCalls(t *testing.T, dir string) []claudeCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []claudeCall
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var c claudeCall
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
func claudeMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
}
func claudeTestProvider(t *testing.T, path string, options Options) *ClaudeCodeProvider {
	t.Helper()
	p, err := NewClaudeCode(ClaudeCodeOptions{Executable: path, Options: options, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func claudeImage(t *testing.T, format string, c color.Color) imagecontext.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, c)
	var b bytes.Buffer
	if format == "png" {
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := jpeg.Encode(&b, img, nil); err != nil {
			t.Fatal(err)
		}
	}
	im, err := imagecontext.Decode(b.Bytes(), time.Unix(123, 0), format)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func TestClaudeCodeNativeImagesAndIsolation(t *testing.T) {
	path, dir := claudeFake(t)
	t.Setenv("CLAUDE_CODE_SIMPLE", "1")
	t.Setenv("CLAUDE_CODE_EFFORT_LEVEL", "max")
	t.Setenv("NODE_OPTIONS", "--require malicious.js")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
	images := []imagecontext.Image{claudeImage(t, "png", color.RGBA{R: 255, A: 255}), claudeImage(t, "jpeg", color.RGBA{B: 255, A: 255})}
	// Forged metadata must be replaced from actual validated bytes.
	images[0].MediaType = "image/jpeg"
	images[0].Width = 999
	req := Request{System: "private system", Prompt: "/command @secret\nUser text", Images: images, Model: "sonnet", Variant: "low", OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)}
	response, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"ok":true}` {
		t.Fatalf("wrong final: %q", response.Text)
	}
	if response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
		t.Fatal("validated image transport not reported")
	}
	textOnly, err := p.Complete(context.Background(), Request{Prompt: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if textOnly.ImagesSubmitted == nil || *textOnly.ImagesSubmitted {
		t.Fatal("text-only request reported image submission")
	}
	calls := claudeCalls(t, dir)
	if len(calls) != 6 {
		t.Fatalf("repeated version/metadata probes: %d calls", len(calls))
	}
	c := calls[3]
	if claudeArg(calls[5].Args, "--disallowedTools") != "*" || containsClaude(calls[5].Args, "--json-schema") {
		t.Fatal("plain request accidentally kept schema formatter")
	}
	for _, flag := range []string{"--safe-mode", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence", "--no-chrome", "--system-prompt-file"} {
		if !containsClaude(c.Args, flag) {
			t.Errorf("missing isolation flag %s", flag)
		}
	}
	for _, flag := range []string{"--bare", "--allowedTools", "--disallowedTools", "--continue", "--resume"} {
		if containsClaude(c.Args, flag) {
			t.Errorf("unsafe flag %s", flag)
		}
	}
	for flag, value := range map[string]string{"--tools": "", "--json-schema": string(req.OutputSchema), "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`, "--model": "claude-exact-sonnet", "--effort": "low"} {
		if claudeArg(c.Args, flag) != value {
			t.Errorf("%s: %q", flag, claudeArg(c.Args, flag))
		}
	}
	if strings.Contains(strings.Join(c.Args, " "), "private system") || c.System != "private system" || strings.Contains(c.System, string(req.OutputSchema)) {
		t.Fatal("native system/schema not separate from user")
	}
	if c.Env["HOME"] != os.Getenv("HOME") || c.Env["CLAUDE_CONFIG_DIR"] != os.Getenv("CLAUDE_CONFIG_DIR") {
		t.Fatal("saved auth location changed")
	}
	for _, key := range []string{"CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_EFFORT_LEVEL", "NODE_OPTIONS"} {
		if _, ok := c.Env[key]; ok {
			t.Errorf("unsafe inherited env %s", key)
		}
	}
	if c.Env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"] != "1" {
		t.Fatal("memory override inherited")
	}
	var message struct {
		Type           string `json:"type"`
		ClientComposed bool   `json:"client_composed"`
		Message        struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(c.Input), &message); err != nil {
		t.Fatal(err)
	}
	if !message.ClientComposed || message.Message.Role != "user" || len(message.Message.Content) != 3 || message.Message.Content[0].Text != req.Prompt {
		t.Fatal("wrong user envelope/order")
	}
	for i, media := range []string{"image/png", "image/jpeg"} {
		block := message.Message.Content[i+1]
		decoded, err := base64.StdEncoding.DecodeString(block.Source.Data)
		if err != nil || !bytes.Equal(decoded, images[i].Data) || block.Type != "image" || block.Source.Type != "base64" || block.Source.MediaType != media {
			t.Fatalf("image %d changed or reordered", i)
		}
	}
	for _, call := range calls {
		if _, err := os.Stat(call.CWD); !os.IsNotExist(err) {
			t.Errorf("work directory not removed: %s", call.CWD)
		}
	}
}
func containsClaude(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
func claudeArg(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "<missing>"
}

func TestClaudeCodeCapabilitiesConservativeAndCached(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{})
	caps, err := p.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.ImageInput != Unknown || caps.ResolvedModel != "claude-exact-default" || caps.ImageReason == "" || caps.MaxImageBytes != 5<<20 || caps.MaxTotalImageBytes != 6<<20 || caps.MaxImageWidth != 8000 || caps.MaxImageHeight != 8000 || caps.MaxRequestBytes != 10_000_000 {
		t.Fatalf("%+v", caps)
	}
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(claudeCalls(t, dir)) != 4 {
		t.Fatal("model/version capabilities not cached or auth status not refreshed")
	}
	_, err = p.Complete(context.Background(), Request{Prompt: "image", Images: []imagecontext.Image{claudeImage(t, "png", color.White)}})
	if !errors.Is(err, ErrGenerationRestricted) {
		t.Fatalf("images silently dropped: %v", err)
	}
	if len(claudeCalls(t, dir)) != 5 {
		t.Fatal("inference launched for unknown image support")
	}
	// Returned slices cannot mutate the cached metadata.
	caps.ImageMediaTypes[0] = "forged"
	caps, _ = p.Capabilities(context.Background())
	if caps.ImageMediaTypes[0] != "image/png" {
		t.Fatal("mutable cached capabilities")
	}
}

func TestClaudeCodeErrorsAndPartialFrames(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want error
	}{{"auth", ErrAuthentication}, {"rate", ErrRateLimited}, {"model", ErrModelUnavailable}, {"error", ErrProviderUnavailable}, {"malformed", ErrProtocol}, {"partial", ErrProtocol}, {"truncated", ErrProtocol}, {"duplicate", ErrProtocol}, {"tools", ErrGenerationRestricted}, {"overflow", ErrOutputLimit}} {
		t.Run(tc.mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, tc.mode)
			p := claudeTestProvider(t, path, Options{MaxOutputBytes: 10000})
			r, err := p.Complete(context.Background(), Request{Prompt: "hi"})
			if !errors.Is(err, tc.want) || r.Text != "" {
				t.Fatalf("%q %v, want %v", r.Text, err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("diagnostics leaked")
			}
		})
	}
	t.Run("nonzero", func(t *testing.T) {
		path, dir := claudeFake(t)
		claudeMode(t, dir, "nonzero")
		p := claudeTestProvider(t, path, Options{})
		r, err := p.Complete(context.Background(), Request{})
		if err == nil || r.Text != "" {
			t.Fatal("accepted success frame from failed process")
		}
	})
}

func TestClaudeCodeProbeFailureExpires(t *testing.T) {
	path, dir := claudeFake(t)
	claudeMode(t, dir, "probe-fail")
	p := claudeTestProvider(t, path, Options{})
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
	claudeMode(t, dir, "success")
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatal("failure cache missing")
	}
	p.probeUntil = time.Now().Add(-time.Second)
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatalf("transient failure permanent: %v", err)
	}
	if len(claudeCalls(t, dir)) != 6 {
		t.Fatal("wrong probe count")
	}
}

func TestClaudeCodeCanceledProbeDoesNotPoisonNextRequest(t *testing.T) {
	for _, mode := range []string{"version-hang", "initialize-hang"} {
		for _, deadline := range []bool{false, true} {
			t.Run(mode+"/"+strconv.FormatBool(deadline), func(t *testing.T) {
				path, dir := claudeFake(t)
				claudeMode(t, dir, mode)
				p := claudeTestProvider(t, path, Options{Timeout: 10 * time.Second})
				var ctx context.Context
				var cancel context.CancelFunc
				want := context.Canceled
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					want = context.DeadlineExceeded
				} else {
					ctx, cancel = context.WithCancel(context.Background())
				}
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := p.Capabilities(ctx); done <- err }()
				claudeWaitFile(t, filepath.Join(dir, "child"))
				if !deadline {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("canceled probe did not finish")
				}
				for _, err := range []error{p.versionErr, p.probeErr} {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						t.Fatal("request-local cancellation cached", err)
					}
				}
				claudeMode(t, dir, "success")
				caps, err := p.Capabilities(context.Background())
				if err != nil || caps.ResolvedModel != "claude-exact-default" {
					t.Fatalf("canceled request poisoned next queued request: %+v %v", caps, err)
				}
				versions := 0
				for _, call := range claudeCalls(t, dir) {
					if len(call.Args) == 1 && call.Args[0] == "--version" {
						versions++
					}
				}
				if mode == "initialize-hang" && versions != 1 {
					t.Fatal("canceled initialize unnecessarily reprobed successful version")
				}
			})
		}
	}
}

func TestClaudeCodeVersionAndLimits(t *testing.T) {
	path, dir := claudeFake(t)
	claudeMode(t, dir, "old")
	p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
	if _, err := p.Capabilities(context.Background()); err == nil {
		t.Fatal("old version accepted")
	}
	if len(claudeCalls(t, dir)) != 1 {
		t.Fatal("started unsafe old CLI")
	}
	_, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{{Data: []byte("bad")}}})
	if err == nil {
		t.Fatal("invalid bytes accepted")
	}
	if len(claudeCalls(t, dir)) != 1 {
		t.Fatal("probed before validating image bytes")
	}
	_, err = p.Complete(context.Background(), Request{Images: make([]imagecontext.Image, imagecontext.MaxImages+1)})
	if err == nil {
		t.Fatal("image count unbounded")
	}
	_, err = p.Complete(context.Background(), Request{Prompt: strings.Repeat("x", p.options.MaxInputBytes+1)})
	if !errors.Is(err, ErrInputLimit) {
		t.Fatal(err)
	}
	for _, o := range []ClaudeCodeOptions{{Options: Options{Timeout: -1}}, {Variant: "ultracode"}, {Model: "-bad"}, {Model: "x\ny"}} {
		if _, err := NewClaudeCode(o); err == nil {
			t.Fatalf("invalid options accepted: %+v", o)
		}
	}
}

func TestClaudeCodeUnauditedFutureVersionIsIsolationFailure(t *testing.T) {
	path, dir := claudeFake(t)
	claudeMode(t, dir, "future")
	p := claudeTestProvider(t, path, Options{})
	_, err := p.Complete(context.Background(), Request{Prompt: "do not send"})
	if !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("future binary not rejected as isolation unavailable: %v", err)
	}
	if len(claudeCalls(t, dir)) != 1 {
		t.Fatal("unaudited binary reached auth or initialize")
	}
}

func TestClaudeCodeExecutableIdentityInvalidatesBothCaches(t *testing.T) {
	for _, change := range []string{"inode", "in-place-restored-mtime", "symlink", "PATH"} {
		t.Run(change, func(t *testing.T) {
			path, dir := claudeFake(t)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			future := bytes.ReplaceAll(original, []byte("2.1.299 (Claude Code)"), []byte("2.1.300 (Claude Code)"))
			selected := path
			if change == "symlink" {
				selected = filepath.Join(dir, "selected")
				if err := os.Symlink(path, selected); err != nil {
					t.Fatal(err)
				}
			}
			if change == "PATH" {
				selected = "claude"
				t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			}
			p := claudeTestProvider(t, selected, Options{ImageInput: "supported"})
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(claudeCalls(t, dir)) != 3 || len(p.models) == 0 {
				t.Fatal("initial metadata did not populate cache")
			}
			target := path
			switch change {
			case "inode":
				replacement := filepath.Join(dir, "replacement")
				if err := os.WriteFile(replacement, future, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "in-place-restored-mtime":
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, future, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target = filepath.Join(dir, "new-target")
				if err := os.WriteFile(target, future, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(selected); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, selected); err != nil {
					t.Fatal(err)
				}
			case "PATH":
				newDir := t.TempDir()
				target = filepath.Join(newDir, "claude")
				if err := os.WriteFile(target, future, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", newDir+":"+os.Getenv("PATH"))
			}
			response, err := p.Complete(context.Background(), Request{Prompt: "must not be submitted", Images: []imagecontext.Image{claudeImage(t, "png", color.White)}})
			if !errors.Is(err, ErrIsolationUnavailable) || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
				t.Fatalf("replacement bypassed audit: %+v %v", response, err)
			}
			calls := claudeCalls(t, dir)
			if len(calls) != 4 || strings.Join(calls[3].Args, " ") != "--version" {
				t.Fatal("replacement reached auth/initialize/generation before version audit")
			}
			if len(p.models) != 0 || !p.probeUntil.IsZero() {
				t.Fatal("replacement left old model cache live")
			}
			// A subsequent audited replacement must recover immediately, refresh
			// models, and not inherit the previous replacement's cached failure.
			restored := bytes.ReplaceAll(original, []byte("claude-exact-default"), []byte("claude-new-default"))
			if err := os.WriteFile(target, restored, 0700); err != nil {
				t.Fatal(err)
			}
			caps, err := p.Capabilities(context.Background())
			if err != nil || caps.ResolvedModel != "claude-new-default" {
				t.Fatalf("replacement model metadata stayed cached: %+v %v", caps, err)
			}
			if len(claudeCalls(t, dir)) != 7 {
				t.Fatal("replacement did not refresh version, auth and model discovery")
			}
		})
	}
}

func TestClaudeCodeExecutableReplacementDuringOperationFailsBeforeGeneration(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	future := bytes.ReplaceAll(original, []byte("2.1.299 (Claude Code)"), []byte("2.1.300 (Claude Code)"))
	if err := os.WriteFile(filepath.Join(dir, "replacement"), future, 0700); err != nil {
		t.Fatal(err)
	}
	claudeMode(t, dir, "swap-after-status")
	response, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{claudeImage(t, "png", color.White)}})
	if !errors.Is(err, ErrIsolationUnavailable) || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("mid-operation replacement generated: %+v %v", response, err)
	}
	if len(claudeCalls(t, dir)) != 4 {
		t.Fatal("replacement received a user turn")
	}
}

func TestClaudeCodeExecutablePinRejectsPATHAndSymlinkRetarget(t *testing.T) {
	for _, change := range []string{"PATH", "symlink"} {
		t.Run(change, func(t *testing.T) {
			path, dir := claudeFake(t)
			selected := "claude"
			if change == "PATH" {
				t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			} else {
				selected = filepath.Join(dir, "selected")
				if err := os.Symlink(path, selected); err != nil {
					t.Fatal(err)
				}
			}
			p := claudeTestProvider(t, selected, Options{})
			ctx, err := p.pinExecutable(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(t.TempDir(), "claude")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(other, data, 0700); err != nil {
				t.Fatal(err)
			}
			if change == "PATH" {
				t.Setenv("PATH", filepath.Dir(other)+":"+os.Getenv("PATH"))
			} else {
				if err := os.Remove(selected); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, selected); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.run(ctx, []string{"--version"}, nil); !errors.Is(err, ErrIsolationUnavailable) {
				t.Fatalf("pinned operation followed executable retarget: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("retargeted binary executed under old audit")
			}
		})
	}
}

func TestClaudeCodeReplacementProbeCancellationIsNotCached(t *testing.T) {
	for _, mode := range []string{"version-hang", "initialize-hang"} {
		t.Run(mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\n# audited replacement identity\n")...)
			if err := os.WriteFile(path, data, 0700); err != nil {
				t.Fatal(err)
			}
			claudeMode(t, dir, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := p.Capabilities(ctx); done <- err }()
			claudeWaitFile(t, filepath.Join(dir, "child"))
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("replacement probe ignored cancellation")
			}
			if !p.probeUntil.IsZero() || len(p.models) != 0 {
				t.Fatal("canceled replacement reused previous executable's model cache")
			}
			if p.versionErr != nil || p.probeErr != nil {
				t.Fatal("replacement cancellation was cached")
			}
			claudeMode(t, dir, "success")
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatalf("replacement cancellation poisoned retry: %v", err)
			}
		})
	}
}

func TestClaudeCodeConstructorDoesNotResolveExecutable(t *testing.T) {
	p, err := NewClaudeCode(ClaudeCodeOptions{Executable: filepath.Join(t.TempDir(), "not-installed")})
	if err != nil {
		t.Fatalf("constructor resolved executable instead of remaining lazy: %v", err)
	}
	defer p.Close()
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrIsolationUnavailable) {
		t.Fatalf("missing executable did not fail on first operation: %v", err)
	}
}

func TestClaudeCodeSubmissionStatusOnFailure(t *testing.T) {
	for _, tc := range []struct {
		mode             string
		known, submitted bool
	}{
		{"status-malformed", true, false}, {"old", true, false}, {"unresolved", true, false},
		{"malformed", false, false}, {"partial", false, false}, {"overflow", false, false}, {"rate", false, false},
		{"schema-missing", true, true}, {"schema-null", true, true}, {"duplicate", true, true}, {"truncated", true, true}, {"nonzero", true, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, tc.mode)
			p := claudeTestProvider(t, path, Options{ImageInput: "supported", MaxOutputBytes: 10000})
			response, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{claudeImage(t, "png", color.White)}, OutputSchema: json.RawMessage(`{"type":"object"}`)})
			if err == nil || response.Text != "" {
				t.Fatalf("expected final failure: %+v %v", response, err)
			}
			if (response.ImagesSubmitted != nil) != tc.known || (tc.known && *response.ImagesSubmitted != tc.submitted) {
				t.Fatalf("wrong transport admission status for %s: %+v %v", tc.mode, response, err)
			}
		})
	}
	for _, failure := range []string{"invalid-image", "preflight-limit", "invalid-schema", "canceled", "closed"} {
		t.Run(failure, func(t *testing.T) {
			path, dir := claudeFake(t)
			p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
			ctx := context.Background()
			request := Request{Images: []imagecontext.Image{claudeImage(t, "png", color.White)}}
			switch failure {
			case "invalid-image":
				request.Images[0].Data = []byte("invalid")
			case "preflight-limit":
				request.Prompt = strings.Repeat("x", p.options.MaxInputBytes+1)
			case "invalid-schema":
				request.OutputSchema = json.RawMessage(`{`)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				_ = p.Close()
			}
			response, err := p.Complete(ctx, request)
			if err == nil || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
				t.Fatalf("known pre-send failure lost non-submission status: %+v %v", response, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("invalid request launched subprocess")
			}
		})
	}
}

func TestClaudeCodeImageTransportBoundsBeforeStartup(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
	im := claudeImage(t, "png", color.White)
	oversize := im
	oversize.Data = append(bytes.Clone(im.Data), make([]byte, (5<<20)+1-len(im.Data))...)
	if _, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{oversize}}); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("per-image byte cap: %v", err)
	}
	// A real decodable image whose dimension exceeds the provider boundary.
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8001, 1))); err != nil {
		t.Fatal(err)
	}
	wide, err := imagecontext.Decode(b.Bytes(), time.Unix(1, 0), "wide")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{wide}}); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("dimension cap: %v", err)
	}
	// Trailing PNG padding is valid encoded data and must be counted/preserved.
	im.Data = append(bytes.Clone(im.Data), make([]byte, (3<<20)-len(im.Data))...)
	if _, err := p.Complete(context.Background(), Request{Images: []imagecontext.Image{im, im, im}}); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("6 MiB aggregate image cap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("oversized request started CLI")
	}
}

func TestClaudeCodeProbeWaitHonorsCancellation(t *testing.T) {
	path, _ := claudeFake(t)
	p := claudeTestProvider(t, path, Options{})
	p.probeGate <- struct{}{}
	defer func() { <-p.probeGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Capabilities(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestClaudeCodeNativeSchema(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{})
	if _, err := p.Complete(context.Background(), Request{OutputSchema: json.RawMessage(`{`)}); err == nil {
		t.Fatal("malformed input schema accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("malformed schema reached process")
	}
	claudeMode(t, dir, "notjson")
	response, err := p.Complete(context.Background(), Request{OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if !errors.Is(err, ErrProtocol) || response.Text != "" {
		t.Fatal("non-JSON schema response accepted")
	}
}

func TestClaudeCodeNativeSchemaRequiresStructuredOutput(t *testing.T) {
	for _, mode := range []string{"schema-missing", "schema-null", "schema-denied", "notjson"} {
		t.Run(mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, mode)
			p := claudeTestProvider(t, path, Options{})
			response, err := p.Complete(context.Background(), Request{OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)})
			if !errors.Is(err, ErrProtocol) || response.Text != "" || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
				t.Fatalf("success without structured output accepted: %+v %v", response, err)
			}
		})
	}
}

func TestClaudeCodeAuditedBuiltinsAndFormatterOnly(t *testing.T) {
	for _, schema := range []bool{false, true} {
		for _, mode := range []string{"success", "custom-plugin", "forged-builtin", "unknown-builtin", "mcp", "skill", "action-tool-call", "plain-formatter", "extra-schema-tool", "schema-no-tool"} {
			if (!schema && (mode == "extra-schema-tool" || mode == "schema-no-tool")) || (schema && mode == "plain-formatter") {
				continue
			}
			t.Run(strconv.FormatBool(schema)+"/"+mode, func(t *testing.T) {
				path, dir := claudeFake(t)
				claudeMode(t, dir, mode)
				p := claudeTestProvider(t, path, Options{})
				request := Request{Prompt: "native plugin metadata"}
				if schema {
					request.OutputSchema = json.RawMessage(`{"type":"object"}`)
				}
				response, err := p.Complete(context.Background(), request)
				if mode == "success" {
					if err != nil || response.Text != `{"ok":true}` {
						t.Fatalf("approved builtins/formatter rejected: %q %v", response.Text, err)
					}
				} else if !errors.Is(err, ErrGenerationRestricted) {
					t.Fatalf("unapproved customization/action accepted: %v", err)
				}
			})
		}
	}
}

func TestClaudeCodeStructuredOutputNormalizedWithoutResultText(t *testing.T) {
	data := []byte("{\"type\":\"system\",\"subtype\":\"init\",\"tools\":[\"StructuredOutput\"]}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ignore me\",\"structured_output\": { \"value\" : 1234567890123456789, \"ok\" : true }}\n")
	response, err := claudeResult(data, true)
	if err != nil || response.Text != `{"value":1234567890123456789,"ok":true}` {
		t.Fatalf("structured JSON not normalized precisely: %q %v", response.Text, err)
	}
}

func TestClaudeCodeExactImagePreflightCanContinueWithoutImages(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{ImageInput: "supported", MaxInputBytes: 2 << 20})
	im := claudeImage(t, "png", color.White)
	im.Data = append(bytes.Clone(im.Data), make([]byte, (3<<20)-len(im.Data))...)
	request := Request{Prompt: strings.Repeat("<", 300000), Images: []imagecontext.Image{im, im}}
	// Six MiB raw images fit the aggregate bound, but the exact base64 and
	// Go JSON escaping push this complete stdin frame above 10,000,000 bytes.
	if err := p.PreflightImages(context.Background(), request); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("exact wire preflight missed encoded input overflow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("preflight started a subprocess")
	}
	request.Images = nil
	if err := p.PreflightImages(context.Background(), request); err != nil {
		t.Fatal("text fallback preflight failed", err)
	}
	response, err := p.Complete(context.Background(), request)
	if err != nil || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("text-only fallback failed: %+v %v", response, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.PreflightImages(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatal("preflight ignored cancellation", err)
	}
}

func TestClaudeCodeAuthIsolationFailClosed(t *testing.T) {
	for _, mode := range []string{"remote-cache", "external-profile", "saved-gateway", "missing", "wrong-config", "custom-endpoint-saved-auth"} {
		t.Run(mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			config := os.Getenv("CLAUDE_CONFIG_DIR")
			want := ErrClaudeCodeIsolation
			switch mode {
			case "remote-cache":
				_ = os.WriteFile(filepath.Join(config, "remote-settings.json"), []byte(`{"managedMcpServers":{"server":{"url":"https://private.invalid"}}}`), 0600)
			case "wrong-config":
				claudeAuthFixture(t, dir, map[string]any{"configDirectory": "/another-native-profile"})
			case "custom-endpoint-saved-auth":
				t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.invalid")
			case "external-profile":
				claudeAuthFixture(t, dir, map[string]any{"authMethod": "api_key_helper"})
			case "saved-gateway":
				claudeAuthFixture(t, dir, map[string]any{"apiProvider": "cloudGateway"})
			case "missing":
				claudeAuthFixture(t, dir, map[string]any{"loggedIn": false, "authMethod": "none"})
				want = ErrAuthentication
			}
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Complete(context.Background(), Request{}); !errors.Is(err, want) {
				t.Fatalf("unsafe profile launched: %v", err)
			}
			if mode == "remote-cache" {
				if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
					t.Fatal("local managed policy reached process startup")
				}
			} else {
				calls := claudeCalls(t, dir)
				if len(calls) != 2 || calls[1].Input != "" || !containsClaude(calls[1].Args, "auth") || !containsClaude(calls[1].Args, "--json") {
					t.Fatal("unsafe profile reached general initialize or inference")
				}
			}
		})
	}
	t.Run("gateway-env-auth", func(t *testing.T) {
		path, dir := claudeFake(t)
		t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.invalid")
		t.Setenv("ANTHROPIC_AUTH_TOKEN", "test-only-secret")
		p := claudeTestProvider(t, path, Options{})
		if _, err := p.Capabilities(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := claudeCalls(t, dir)[1]
		if c.Env["ANTHROPIC_AUTH_TOKEN"] != "test-only-secret" || containsClaude(c.Args, "--bare") {
			t.Fatal("native auth changed")
		}
	})
}

func claudeAuthFixture(t *testing.T, dir string, overrides map[string]any) {
	t.Helper()
	status := map[string]any{"loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty", "subscriptionType": "max", "configDirectory": os.Getenv("CLAUDE_CONFIG_DIR")}
	for key, value := range overrides {
		status[key] = value
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-status"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCodeAuthDiscoveryOnlyNativeStatus(t *testing.T) {
	for _, plan := range []string{"pro", "max", "team", "enterprise", "", "future-plan"} {
		t.Run(plan, func(t *testing.T) {
			path, dir := claudeFake(t)
			// There is deliberately NO credential file. Metadata exists exclusively in
			// the fake native command's output; inspecting a credential store fails.
			claudeAuthFixture(t, dir, map[string]any{"subscriptionType": plan, "email": "PRIVATE", "orgName": "PRIVATE", "token": "DO-NOT-ECHO"})
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls := claudeCalls(t, dir)
			if len(calls) != 3 || strings.Join(calls[1].Args, "|") != `--safe-mode|--setting-sources||--settings|{"disableAllHooks":true}|auth|status|--json` || calls[1].Input != "" {
				t.Fatal("not using native isolated auth status contract")
			}
			// A later logout must be checked before returning cached model metadata
			// or starting a user request; a plan change itself is not a refusal.
			claudeAuthFixture(t, dir, map[string]any{"loggedIn": false, "authMethod": "none"})
			if _, err := p.Complete(context.Background(), Request{Prompt: "never send"}); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("stale auth snapshot: %v", err)
			}
			calls = claudeCalls(t, dir)
			if len(calls) != 4 || calls[3].Input != "" {
				t.Fatal("signed-out account reached inference")
			}
		})
	}
	for _, tc := range []struct {
		mode string
		want error
	}{{"status-malformed", ErrProtocol}, {"status-overflow", ErrOutputLimit}} {
		t.Run(tc.mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, tc.mode)
			p := claudeTestProvider(t, path, Options{MaxOutputBytes: 10000})
			_, err := p.Capabilities(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("raw auth output disclosed")
			}
			if len(claudeCalls(t, dir)) != 2 {
				t.Fatal("bad auth reached initialize")
			}
		})
	}
}

func TestClaudeCodeGatewayMustSelectExplicitEnvironmentKey(t *testing.T) {
	for _, source := range []string{"", "apiKeyHelper", "config"} {
		t.Run(source, func(t *testing.T) {
			path, dir := claudeFake(t)
			t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.invalid")
			t.Setenv("ANTHROPIC_API_KEY", "synthetic-key")
			claudeAuthFixture(t, dir, map[string]any{"authMethod": "api_key", "apiKeySource": source})
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrIsolationUnavailable) {
				t.Fatalf("unconfirmed environment key accepted: %v", err)
			}
			if len(claudeCalls(t, dir)) != 2 {
				t.Fatal("unconfirmed gateway authentication reached initialize")
			}
		})
	}
}

func TestClaudeCodeNativeSubscriptionAndEnvironmentAuth(t *testing.T) {
	for _, credential := range []string{"saved", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		t.Run(credential, func(t *testing.T) {
			path, dir := claudeFake(t)
			if credential != "saved" {
				t.Setenv(credential, "synthetic-not-a-real-credential")
			} else {
				// Unknown/missing account metadata does not reject a native login.
				claudeAuthFixture(t, dir, map[string]any{"subscriptionType": nil, "orgId": nil, "email": nil})
			}
			p := claudeTestProvider(t, path, Options{})
			for _, schema := range []json.RawMessage{nil, json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`)} {
				response, err := p.Complete(context.Background(), Request{Prompt: "fixture", OutputSchema: schema})
				if err != nil || response.Text != `{"ok":true}` {
					t.Fatalf("native auth generation failed: %+v %v", response, err)
				}
			}
			for _, call := range claudeCalls(t, dir) {
				if containsClaude(call.Args, "--bare") || containsClaude(call.Args, "--dangerously-skip-permissions") || containsClaude(call.Args, "--allow-dangerously-skip-permissions") {
					t.Fatal("authentication used unsafe or OAuth-disabling fallback")
				}
				if call.Env["CLAUDE_CONFIG_DIR"] != os.Getenv("CLAUDE_CONFIG_DIR") || (credential != "saved" && call.Env[credential] != "synthetic-not-a-real-credential") {
					t.Fatal("native credential selection changed")
				}
				if containsClaude(call.Args, "--print") {
					if !containsClaude(call.Args, "--tools") || !containsClaude(call.Args, "--strict-mcp-config") || !containsClaude(call.Args, "dontAsk") || !containsClaude(call.Args, "--safe-mode") {
						t.Fatal("native subscription lost generation-only controls")
					}
					for i, arg := range call.Args {
						if arg == "--tools" && call.Args[i+1] != "" {
							t.Fatal("action tools enabled")
						}
					}
				}
			}
		})
	}
}

func TestClaudeCodeSavedSubscriptionNeverRedirectedToCustomEndpoint(t *testing.T) {
	for _, base := range []string{"http://api.anthropic.com", "https://api.anthropic.com:444", "https://api.anthropic.com/custom", "https://api.anthropic.com?custom=1", "https://gateway.invalid"} {
		t.Run(base, func(t *testing.T) {
			path, dir := claudeFake(t)
			t.Setenv("ANTHROPIC_BASE_URL", base)
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrClaudeCodeIsolation) {
				t.Fatal("saved subscription credentials could be redirected", err)
			}
			if len(claudeCalls(t, dir)) != 2 {
				t.Fatal("custom endpoint received initialize or inference")
			}
		})
	}
}

func TestClaudeCodeManagedStartupEventsAreNotModelTools(t *testing.T) {
	for _, mode := range []string{"hook", "managed-policy"} {
		t.Run(mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, mode)
			claudeAuthFixture(t, dir, map[string]any{"subscriptionType": "enterprise"})
			policy := `{"disableAllHooks":false,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"native-admin-hook"}]}]}}`
			policyPath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "remote-settings.json")
			if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
				t.Fatal(err)
			}
			p := claudeTestProvider(t, path, Options{})
			for _, schema := range []json.RawMessage{nil, json.RawMessage(`{"type":"object"}`)} {
				if _, err := p.Complete(context.Background(), Request{Prompt: "fixture", OutputSchema: schema}); err != nil {
					t.Fatal("mandatory startup policy incorrectly treated as model tool access", err)
				}
			}
			data, _ := os.ReadFile(policyPath)
			if string(data) != policy {
				t.Fatal("mandatory managed policy was changed")
			}
		})
	}
}

func TestClaudeCodeCachedPolicyChecksActualConflictsNotAccounts(t *testing.T) {
	for _, tc := range []struct {
		name, policy, reason string
	}{
		{"empty", `{}`, ""},
		{"benign", `{"permissions":{"deny":["Bash"]},"forceRemoteSettingsRefresh":true,"disableAllHooks":true}`, ""},
		{"hooks", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"PRIVATE"}]}]}}`, ""},
		{"environment", `{"env":{"CLAUDE_CODE_SAFE_MODE":"0"}}`, ""},
		{"helper", `{"apiKeyHelper":"PRIVATE"}`, ""},
		{"hooks-override", `{"disableAllHooks":false}`, ""},
		{"memory", `{"autoMemoryEnabled":true}`, ""},
		{"skills", `{"syncClaudeAiSkills":true}`, ""},
		{"plugins", `{"syncClaudeAiPlugins":true}`, ""},
		{"connectors", `{"disableClaudeAiConnectors":false}`, ""},
		{"mcp", `{"managedMcpServers":{"server":{"url":"https://private.invalid"}}}`, "managedMcpServers"},
		{"malformed", `{PRIVATE`, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeAuthFixture(t, dir, map[string]any{"subscriptionType": "team"})
			policyPath := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "remote-settings.json")
			if err := os.WriteFile(policyPath, []byte(tc.policy), 0600); err != nil {
				t.Fatal(err)
			}
			p := claudeTestProvider(t, path, Options{})
			_, err := p.Capabilities(context.Background())
			if tc.reason == "" {
				if err != nil {
					t.Fatal("benign policy rejected", err)
				}
			} else {
				if !errors.Is(err, ErrClaudeCodeIsolation) || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("missing safe, actionable policy error", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
					t.Fatal("conflicting cache reached native startup")
				}
			}
			data, _ := os.ReadFile(policyPath)
			if string(data) != tc.policy {
				t.Fatal("provider managed cache was modified")
			}
		})
	}
}

func TestClaudeCodeNativeIdentityInvalidatesModelCache(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{})
	for i, status := range []map[string]any{
		{"orgId": "org-a", "email": "account-a"},
		{"orgId": "org-b", "email": "account-a"},
		{"orgId": "org-b", "email": "account-b"},
		{"orgId": "org-b", "email": "account-b", "subscriptionType": "team"},
	} {
		claudeAuthFixture(t, dir, status)
		if _, err := p.Capabilities(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls := claudeCalls(t, dir); len(calls) != 3+2*i || !strings.Contains(calls[len(calls)-1].Input, "ginkgo-initialize") {
			t.Fatal("identity change reused another account's models")
		}
	}
	// Account status is unchanged, but an explicit model selector changed.
	t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "synthetic-new-model")
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(claudeCalls(t, dir)) != 11 {
		t.Fatal("model environment change reused stale discovery")
	}
	// A policy appearing during the cache lifetime must not be hidden by it.
	if err := os.WriteFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "remote-settings.json"), []byte(`{"managedMcpServers":{"server":{"url":"https://private.invalid"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrClaudeCodeIsolation) {
		t.Fatal("model cache hid a policy conflict", err)
	}
	if len(claudeCalls(t, dir)) != 11 {
		t.Fatal("policy conflict reached startup through cache")
	}
}

func TestClaudeCodeInitializationPolicyConflictNeverSubmitsContext(t *testing.T) {
	path, dir := claudeFake(t)
	claudeMode(t, dir, "initialize-conflicting-policy")
	p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
	response, err := p.Complete(context.Background(), Request{Prompt: "PRIVATE", Images: []imagecontext.Image{claudeImage(t, "png", color.White)}})
	if !errors.Is(err, ErrClaudeCodeIsolation) || response.ImagesSubmitted == nil || *response.ImagesSubmitted {
		t.Fatalf("policy conflict lost pre-submission state: %+v %v", response, err)
	}
	for _, call := range claudeCalls(t, dir) {
		if strings.Contains(call.Input, "PRIVATE") || strings.Contains(call.Input, `"type":"user"`) {
			t.Fatal("initialization policy conflict reached user context")
		}
	}
}

func TestClaudeCodeNativeManagedMCPRefusalIsPreserved(t *testing.T) {
	path, dir := claudeFake(t)
	claudeMode(t, dir, "native-managed-mcp-refusal")
	p := claudeTestProvider(t, path, Options{})
	_, err := p.Complete(context.Background(), Request{Prompt: "never-submit"})
	if !errors.Is(err, ErrClaudeCodeIsolation) || !strings.Contains(err.Error(), "MCP policy") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("native managed MCP refusal was lost or exposed raw diagnostics", err)
	}
	if len(claudeCalls(t, dir)) != 3 {
		t.Fatal("native policy refusal retried with weaker controls")
	}
}

func TestClaudeCodeEndpointPolicyInspection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "managed-settings.d"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"managed-settings.json", "managed-settings.d/benign.json", "managed-mcp.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := claudeEndpointPolicy(context.Background(), dir); err != nil {
		t.Fatal("empty endpoint policy refused", err)
	}
	for _, tc := range []struct{ name, policy, reason string }{
		{"managed-settings.json", `{"managedMcpServers":{"PRIVATE":{"url":"https://private.invalid"}}}`, "managedMcpServers"},
		{"managed-settings.d/benign.json", `{"managedMcpServers":{"PRIVATE":{"url":"https://private.invalid"}}}`, "managedMcpServers"},
		{"managed-mcp.json", `{"mcpServers":{"PRIVATE":{"command":"PRIVATE"}}}`, "mcpServers"},
	} {
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.policy), 0600); err != nil {
			t.Fatal(err)
		}
		if err := claudeEndpointPolicy(context.Background(), dir); !errors.Is(err, ErrClaudeCodeIsolation) || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("endpoint conflict not identified safely", err)
		}
		if err := os.WriteFile(filepath.Join(dir, tc.name), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeCodePolicyInspectionRejectsUnsafeFiles(t *testing.T) {
	for _, mode := range []string{"symlink", "fifo", "directory", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "remote-settings.json")
			var err error
			switch mode {
			case "symlink":
				// Never follow a policy link into any credential or unrelated file.
				err = os.Symlink(filepath.Join(dir, "must-not-be-read"), path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "oversize":
				err = os.WriteFile(path, bytes.Repeat([]byte(" "), (2<<20)+1), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := claudePolicyFile(context.Background(), path); !errors.Is(err, ErrClaudeCodeIsolation) {
				t.Fatal("unsafe policy file accepted", err)
			}
		})
	}
}

func TestClaudeCodeExternalSelectorsNeverFallBackToSavedAuth(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN", "ANTHROPIC_IDENTITY_TOKEN_FILE", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Run(key, func(t *testing.T) {
			path, dir := claudeFake(t)
			t.Setenv(key, "unsupported")
			p := claudeTestProvider(t, path, Options{})
			if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrClaudeCodeIsolation) {
				t.Fatal("external credential selector fell back to another account")
			}
			if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("unsupported external auth reached process startup")
			}
		})
	}
}

func TestClaudeCodeUnresolvedModelNeverGetsImages(t *testing.T) {
	for _, model := range []string{"", "sonnet", "sonnet[1m]", "unknown-alias", "claude-unknown-latest"} {
		t.Run(model, func(t *testing.T) {
			path, dir := claudeFake(t)
			claudeMode(t, dir, "unresolved")
			p := claudeTestProvider(t, path, Options{ImageInput: "supported"})
			p.options.Model = model
			caps, err := p.Capabilities(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if caps.ImageInput != Unknown || caps.ResolvedModel != "" || !strings.Contains(caps.ImageReason, "resolvedModel") {
				t.Fatalf("unresolved alias got supported override: %+v", caps)
			}
			_, err = p.Complete(context.Background(), Request{Prompt: "image", Images: []imagecontext.Image{claudeImage(t, "png", color.White)}})
			if !errors.Is(err, ErrModelUnavailable) {
				t.Fatalf("unresolved image model launched: %v", err)
			}
			for _, c := range claudeCalls(t, dir) {
				if strings.Contains(c.Input, `"type":"user"`) {
					t.Fatal("sent images via unresolved alias")
				}
			}
			if _, err := p.Complete(context.Background(), Request{Prompt: "text remains usable"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeCodeCancelAndCloseReapGroup(t *testing.T) {
	for _, closeProvider := range []bool{false, true} {
		t.Run(strconv.FormatBool(closeProvider), func(t *testing.T) {
			path, dir := claudeFake(t)
			p := claudeTestProvider(t, path, Options{Timeout: 10 * time.Second})
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			claudeMode(t, dir, "hang")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := p.Complete(ctx, Request{Prompt: "hang"}); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			var pid int
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(filepath.Join(dir, "child"))
				if err == nil {
					pid, _ = strconv.Atoi(string(data))
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("fake child did not start")
			}
			if closeProvider {
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation blocked")
			}
			// An orphan killed with its process group may briefly be dead (X) or a
			// zombie (Z) pending init's reap, but it must no longer execute.
			data, err := os.ReadFile(fmtClaudeProc(pid))
			if err == nil {
				fields := strings.Fields(string(data))
				if len(fields) < 3 || (fields[2] != "Z" && fields[2] != "X") {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Fatalf("child still running: %s", data)
				}
			}
			if closeProvider {
				if _, err := p.Complete(context.Background(), Request{}); err == nil {
					t.Fatal("closed provider reusable")
				}
			}
		})
	}
}
func fmtClaudeProc(pid int) string { return "/proc/" + strconv.Itoa(pid) + "/stat" }

func claudeWaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fake process never signaled %s", path)
	return nil
}

func TestClaudeCodeQueuedCancellationDoesNotInterruptGeneration(t *testing.T) {
	path, dir := claudeFake(t)
	p := claudeTestProvider(t, path, Options{Timeout: 10 * time.Second})
	if _, err := p.Capabilities(context.Background()); err != nil {
		t.Fatal(err)
	}
	claudeMode(t, dir, "wait")
	active := make(chan error, 1)
	go func() { _, err := p.Complete(context.Background(), Request{Prompt: "active"}); active <- err }()
	claudeWaitFile(t, filepath.Join(dir, "started"))
	before := len(claudeCalls(t, dir))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Complete(ctx, Request{Prompt: "queued"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation: %v", err)
	}
	if len(claudeCalls(t, dir)) != before {
		t.Fatal("queued process ran concurrently with active generation")
	}
	select {
	case err := <-active:
		t.Fatalf("queued cancellation interrupted unrelated generation: %v", err)
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-active:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active generation failed to finish")
	}
}

func TestClaudeCodeCloseJoinsActiveAndQueuedProcesses(t *testing.T) {
	for _, mode := range []string{"wait", "status-hang"} {
		t.Run(mode, func(t *testing.T) {
			path, dir := claudeFake(t)
			p := claudeTestProvider(t, path, Options{Timeout: 10 * time.Second})
			if _, err := p.Capabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			claudeMode(t, dir, mode)
			done := make(chan error, 9)
			go func() { _, err := p.Complete(context.Background(), Request{Prompt: "active"}); done <- err }()
			marker := "started"
			if mode == "status-hang" {
				marker = "child"
			}
			data := claudeWaitFile(t, filepath.Join(dir, marker))
			// Both capabilities and user requests must join Close, including callers
			// waiting behind a running generation or native status subprocess.
			for i := 0; i < 8; i++ {
				go func(i int) {
					if i%2 == 0 {
						_, err := p.Capabilities(context.Background())
						done <- err
					} else {
						_, err := p.Complete(context.Background(), Request{Prompt: "queued"})
						done <- err
					}
				}(i)
			}
			closed := make(chan struct{})
			go func() { _ = p.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("Close did not join queued/active work")
			}
			for i := 0; i < 9; i++ {
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("work survived Close")
					}
				case <-time.After(time.Second):
					t.Fatal("Close left a queued operation")
				}
			}
			calls := claudeCalls(t, dir)
			for _, c := range calls {
				if _, err := os.Stat(c.CWD); !os.IsNotExist(err) {
					t.Fatal("Close left private work directory")
				}
			}
			if mode == "status-hang" {
				pid, _ := strconv.Atoi(string(data))
				state, err := os.ReadFile(fmtClaudeProc(pid))
				if err == nil {
					fields := strings.Fields(string(state))
					if len(fields) < 3 || fields[2] != "Z" {
						t.Fatal("Close left native auth status descendant running")
					}
				}
			}
		})
	}
}

// This opt-in test uses only synthetic credentials and an invalid gateway. It
// executes native auth status, NOT initialize or a user turn, and never infers.
func TestClaudeCodeRealAuthStatusNoHooksSmoke(t *testing.T) {
	if os.Getenv("GINKGO_CLAUDE_CODE_METADATA_SMOKE") != "1" {
		t.Skip("opt-in harmless native auth-status smoke")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	config := t.TempDir()
	marker := filepath.Join(config, "hook-or-helper-ran")
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_SIMPLE"} {
		t.Setenv(key, "")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.invalid")
	t.Setenv("ANTHROPIC_API_KEY", "not-a-real-api-key")
	settings := map[string]any{"apiKeyHelper": "touch " + marker, "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "touch " + marker}}}}, "SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "touch " + marker}}}}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "remote-settings.json"} {
		if err := os.WriteFile(filepath.Join(config, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := NewClaudeCode(ClaudeCodeOptions{Options: Options{Timeout: 15 * time.Second}, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, done, err := p.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	// Deliberately test the native subcommand in the presence of tripwires. The
	// auth status is not a session. General initialization/generation may execute
	// these mandatory hooks; safe mode is deliberately not a startup sandbox.
	if err := p.authStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native auth status executed a session hook or API helper")
	}
}

// Explicit opt-in, control initialization ONLY. This never submits a user turn.
func TestClaudeCodeRealMetadataSmoke(t *testing.T) {
	if os.Getenv("GINKGO_CLAUDE_CODE_METADATA_SMOKE") != "1" {
		t.Skip("opt-in harmless metadata smoke")
	}
	p, err := NewClaudeCode(ClaudeCodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	caps, err := p.Capabilities(context.Background())
	if errors.Is(err, ErrClaudeCodeIsolation) {
		t.Skip("native profile blocked by managed-policy isolation")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved model=%q image input=%s", caps.ResolvedModel, caps.ImageInput)
}
