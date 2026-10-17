package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/notes"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/wire"
	"github.com/mithrel/ginkgo/pkg/api"
)

// voiceRuntime is daemon-owned. Its single lazy transcriber serializes engine
// invocations; ordinary note operations never validate or initialize speech.
type voiceRuntime struct {
	enabled     bool
	mode        string
	residency   string
	configErr   error
	transcriber *voice.Transcriber
	notes       *notes.Service
	ai          *ai.Service
	defaultTags []string

	// mu protects lifecycle/status only; never hold it across capture, inference,
	// storage, synchronization, or a worker join.
	mu                         sync.Mutex
	root                       context.Context
	closed                     bool
	job                        *listenJob
	status                     ipc.VoiceStatus
	capturer                   segmentCapturer
	captureConfig              voice.CaptureConfig
	captureErr                 error
	sttConfig                  voice.Config
	processSegment             func(context.Context, ipc.Message, voice.Segment) ipc.Response
	syncNote                   func(context.Context)
	syncPending                chan struct{}
	syncCancel                 context.CancelFunc
	syncDone                   chan struct{}
	events                     *voiceevents.Hub
	liveCapturer               liveCapturer
	streamEnabled              bool
	streamErr                  error
	residentConfig             voice.ResidentConfig
	streamUpdate, streamWindow time.Duration
	triggerPhrase              string
	residentFactory            func(voice.ResidentConfig) residentSTT
	streamBackend              string
	sherpaSettings             *viper.Viper
	sherpaRunner               workerRunner
	persistSherpa              func(context.Context, ipc.Message, voice.Transcript, string, string) (api.Entry, error)
}

func newVoiceRuntime(app *wire.App, aiNotes *ai.Service) *voiceRuntime {
	v := app.Cfg
	timeout := 5 * time.Minute
	var err error
	if v.IsSet("voice.stt.timeout") {
		timeout, err = time.ParseDuration(v.GetString("voice.stt.timeout"))
		if err != nil || timeout <= 0 {
			err = fmt.Errorf("voice.stt.timeout must be a positive duration")
		}
	}
	runtime := &voiceRuntime{
		enabled: v.GetBool("voice.enabled"), mode: v.GetString("voice.mode"), residency: v.GetString("voice.stt.residency"), configErr: err,
		transcriber: voice.NewTranscriber(voice.Config{
			Enabled: v.GetBool("voice.enabled"), Executable: v.GetString("voice.stt.executable"), Model: v.GetString("voice.stt.model"),
			Language: v.GetString("voice.stt.language"), Device: v.GetString("voice.stt.device"), Threads: v.GetInt("voice.stt.threads"), Timeout: timeout,
		}),
		notes: notes.NewService(app.Store.Entries), ai: aiNotes, defaultTags: append([]string(nil), v.GetStringSlice("default_tags")...),
	}
	runtime.root = context.Background()
	runtime.sttConfig = voice.Config{Executable: v.GetString("voice.stt.executable"), Model: v.GetString("voice.stt.model"), Language: v.GetString("voice.stt.language"), Device: v.GetString("voice.stt.device"), Threads: v.GetInt("voice.stt.threads")}
	runtime.captureConfig, runtime.captureErr = captureSettings(v, runtime.enabled)
	runtime.capturer = voice.NewCapturer(runtime.captureConfig)
	runtime.liveCapturer = voice.NewCapturer(runtime.captureConfig)
	runtime.streamEnabled = config.VoiceFeatureEnabled(v, "voice.stream.enabled")
	runtime.streamBackend = config.VoiceStreamBackend(v)
	runtime.sherpaSettings = v
	runtime.triggerPhrase = v.GetString("voice.trigger.phrase")
	if v.IsSet("voice.stream.update_interval") {
		update, err := time.ParseDuration(v.GetString("voice.stream.update_interval"))
		if err != nil || update <= 0 {
			runtime.streamErr = fmt.Errorf("voice.stream.update_interval must be a positive duration")
		} else {
			runtime.streamUpdate = update
		}
	}
	if v.IsSet("voice.stream.window") {
		window, err := time.ParseDuration(v.GetString("voice.stream.window"))
		if err != nil || window <= 0 {
			runtime.streamErr = fmt.Errorf("voice.stream.window must be a positive duration")
		} else {
			runtime.streamWindow = window
		}
	}
	runtime.residentConfig = voice.ResidentConfig{Config: runtime.sttConfig, AllowLocalHTTP: v.GetBool("voice.stream.allow_local_http")}
	runtime.residentConfig.Enabled = runtime.enabled
	runtime.residentConfig.Executable = v.GetString("voice.stream.executable")
	runtime.residentConfig.Timeout = timeout
	runtime.status = ipc.VoiceStatus{Enabled: runtime.enabled, State: "idle", SilenceTimeout: runtime.captureConfig.SilenceTimeout}
	if runtime.streamBackend == "sherpa" {
		runtime.status.Backend, runtime.status.RequestedProvider, runtime.status.EffectiveProvider = "sherpa", v.GetString("voice.sherpa.provider"), "unknown"
	}
	return runtime
}

func (v *voiceRuntime) transcribe(ctx context.Context, m ipc.Message) ipc.Response {
	if !v.enabled {
		return ipc.Response{OK: false, Msg: voice.ErrDisabled.Error()}
	}
	if v.configErr != nil {
		return ipc.Response{OK: false, Msg: v.configErr.Error()}
	}
	if v.residency != "" && v.residency != "on_demand" {
		return ipc.Response{OK: false, Msg: "voice.stt.residency supports only on_demand in this POC; set it to on_demand"}
	}
	mode := m.Mode
	if mode == "" {
		mode = v.mode
	}
	if mode == "" {
		mode = "raw"
	}
	if mode != "raw" && mode != "ai" {
		return ipc.Response{OK: false, Msg: "voice mode must be raw or ai"}
	}
	if (m.NoteID != "" || (m.Action != "" && m.Action != "auto")) && (!m.Save || mode != "ai") {
		return ipc.Response{OK: false, Msg: "note actions require save and AI mode"}
	}
	if m.Action != "" && m.Action != "auto" && m.Action != "create" && m.Action != "expand" {
		return ipc.Response{OK: false, Msg: "invalid note action"}
	}
	if !m.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, m.Deadline)
		defer cancel()
	}
	tr, err := v.transcriber.Transcribe(ctx, m.AudioPath, voice.Options{Model: m.Model, Language: m.Language})
	if err != nil {
		return ipc.Response{OK: false, Msg: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return ipc.Response{OK: false, Msg: err.Error()}
	}
	response := ipc.Response{OK: true, Transcript: &ipc.Transcript{Text: tr.Text, Language: tr.Language, Engine: tr.Engine, Model: tr.Model}}
	if !m.Save || strings.TrimSpace(tr.Text) == "" {
		return response
	}
	source := &api.Source{Kind: "voice", OriginalText: tr.Text, Language: tr.Language, STTEngine: tr.Engine, STTModel: tr.Model}
	if mode == "ai" {
		result, err := v.ai.Note(ctx, ai.NoteRequest{Text: tr.Text, Namespace: m.Namespace, Title: m.Title, Action: m.Action, NoteID: m.NoteID, Tags: m.Tags, Source: source}, m.Provider)
		if err != nil {
			return ipc.Response{OK: false, Msg: err.Error()}
		}
		response.Entry, response.Action, response.FallbackReason = &result.Entry, result.Action, result.FallbackReason
		return response
	}
	title := m.Title
	if strings.TrimSpace(title) == "" {
		title = notes.RawTitle(tr.Text)
	}
	tags := m.Tags
	if len(tags) == 0 {
		tags = v.defaultTags
	}
	entry, err := v.notes.Create(ctx, notes.CreateRequest{Namespace: m.Namespace, Title: title, Body: tr.Text, OriginalText: tr.Text, Tags: tags, Source: source})
	if err != nil {
		return ipc.Response{OK: false, Msg: err.Error()}
	}
	response.Entry, response.Action = &entry, "create_note"
	return response
}
