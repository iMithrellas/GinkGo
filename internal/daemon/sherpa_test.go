package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/config"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voice"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/internal/voiceworker"
	"github.com/mithrel/ginkgo/pkg/api"
)

// TestSherpaInstalledHelper runs in a separate process over real inherited pipes.
// No native code, provider, model, microphone, or system recorder is used.
func TestSherpaInstalledHelper(t *testing.T) {
	args := os.Args
	i := 0
	for i < len(args) && args[i] != "--" {
		i++
	}
	if i == len(args) {
		return
	}
	mode, logPath := args[i+1], args[i+2]
	if mode == "full-engine" {
		if runSherpaFakePipeline(logPath) != nil {
			os.Exit(78)
		}
		os.Exit(0)
	}
	scan := bufio.NewScanner(os.Stdin)
	if !scan.Scan() {
		os.Exit(70)
	}
	init, err := voiceworker.DecodeCommand(scan.Bytes())
	if err != nil || init.Type != "init" {
		os.Exit(71)
	}
	log := func(line string) {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(72)
		}
		fmt.Fprintln(f, line)
		f.Close()
	}
	c := init.Config
	log("init " + string(c.ExecutionProvider()) + " keyword=" + c.SelectedKeyword)
	if mode == "crash" || mode == "retry" && c.ExecutionProvider() != voiceworker.CPU {
		os.Exit(17)
	}
	if mode == "no-ready" {
		for scan.Scan() {
		}
		os.Exit(0)
	}
	sequence := uint64(0)
	emit := func(e voiceworker.Event) {
		sequence++
		e.Version, e.Epoch, e.Sequence = 1, init.Epoch, sequence
		if json.NewEncoder(os.Stdout).Encode(e) != nil {
			os.Exit(73)
		}
	}
	status := &voiceworker.ProviderStatus{Requested: c.Provider, Effective: c.ExecutionProvider(), FallbackReason: c.FallbackReason}
	if c.ExecutionProvider() == voiceworker.Auto {
		status.Effective = voiceworker.CUDA
	}
	if mode == "fallback" {
		status.Effective, status.FallbackReason = voiceworker.CPU, "cuda_unavailable"
	}
	emit(voiceworker.Event{Type: "ready", Provider: status})
	if !scan.Scan() {
		os.Exit(74)
	}
	start, err := voiceworker.DecodeCommand(scan.Bytes())
	if err != nil {
		os.Exit(75)
	}
	if start.Type == "stop" {
		emit(voiceworker.Event{Type: "stopped"})
		os.Exit(0)
	}
	if start.Type != "start" {
		os.Exit(76)
	}
	log("start") // The fake recorder is authorized only after accepted ready.
	meta := voiceworker.Metadata{Engine: "sherpa-onnx", Model: "parakeet-tdt-0.6b-v2", Language: "en"}
	emit(voiceworker.Event{Type: "transcript", Transcript: &voiceworker.Transcript{UtteranceID: "ambient", Revision: 1, Text: "background preview", Audio: voiceworker.Span{Start: 0, End: 1600}, Metadata: meta}})
	selected := c.RecordingEnabled || mode == "malicious-savefalse"
	if selected {
		emit(voiceworker.Event{Type: "wake", RecordingNote: true, SessionID: "selected", UtteranceID: "note", Audio: &voiceworker.Span{Start: 1600, End: 3200}})
		log("wake")
	}
	if mode == "fault" {
		emit(voiceworker.Event{Type: "error", RecordingNote: selected, Code: "capture_failed"})
		os.Exit(1)
	}
	if mode == "eof" {
		os.Exit(0)
	}
	for scan.Scan() {
		command, err := voiceworker.DecodeCommand(scan.Bytes())
		if err != nil || command.Type != "stop" {
			os.Exit(77)
		}
		if selected && mode == "cancel-selected" {
			emit(voiceworker.Event{Type: "note_canceled", SessionID: "selected", Code: "empty"})
		} else if selected {
			emit(voiceworker.Event{Type: "state", SessionID: "selected"}) // recording ends before final ASR.
			emit(voiceworker.Event{Type: "transcript", Transcript: &voiceworker.Transcript{UtteranceID: "ambient", Revision: 2, Final: true, Text: "ambient must never save", Audio: voiceworker.Span{Start: 0, End: 16000}, Metadata: meta}})
			emit(voiceworker.Event{Type: "note_candidate", Candidate: &voiceworker.Candidate{SessionID: "selected", UtteranceID: "note", Original: "Ginkgo: Keep Exact CASE!", Body: "Keep Exact CASE!", Audio: voiceworker.Span{Start: 1600, End: 32000}, Control: &voiceworker.Span{Start: 1600, End: 3200}, ControlText: "Ginkgo: ", Metadata: meta}})
		}
		emit(voiceworker.Event{Type: "stopped"})
		os.Exit(0)
	}
	os.Exit(0)
}

func sherpaRuntime(t *testing.T, mode string) (*voiceRuntime, *eventLog, string) {
	t.Helper()
	v, log, _, _ := streamTestRuntime(t)
	v.streamBackend = "sherpa"
	s := viper.New()
	for _, o := range config.GetConfigOptions() {
		s.SetDefault(o.Key, o.Default)
	}
	s.Set("voice.enabled", true)
	s.Set("voice.stream.enabled", true)
	s.Set("voice.stream.backend", "sherpa")
	s.Set("voice.events.enabled", true)
	// Deliberately broken legacy settings must not gate Sherpa requests.
	s.Set("voice.stt.device", "invalid")
	s.Set("voice.stt.residency", "persistent")
	s.Set("voice.stt.model", "")
	s.Set("voice.stream.allow_local_http", false)
	dir := t.TempDir()
	exe, err := os.Executable()
	require.NoError(t, err)
	path := filepath.Join(dir, "installed-helper")
	logPath := filepath.Join(dir, "commands")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestSherpaInstalledHelper$ -- %q %q\n", exe, mode, logPath)), 0700))
	s.Set("voice.sherpa.executable", path)
	s.Set("voice.capture.executable", path) // Passed only, never executed by fake helper.
	s.Set("voice.sherpa.keyword", "GINKGO")
	for _, graph := range []string{"parakeet", "kws"} {
		for _, part := range []string{"encoder", "decoder", "joiner", "tokens"} {
			s.Set("voice.sherpa."+graph+"."+part, "/not-probed/"+graph+"-"+part)
		}
	}
	s.Set("voice.sherpa.silero_model", "/not-probed/silero.onnx")
	s.Set("voice.sherpa.keywords_file", "/not-probed/keywords.txt")
	v.sherpaSettings = s
	return v, log, logPath
}

func TestSherpaPipesPreviewWakeStopSelectedProvenance(t *testing.T) {
	v, log, path := sherpaRuntime(t, "normal")
	response := v.stream(context.Background(), ipc.Message{Save: true, Namespace: "work", Trigger: "GINKGO"})
	require.True(t, response.OK, response.Msg)
	require.Equal(t, "unknown", response.VoiceStatus.EffectiveProvider)
	preview := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" && !e.Final })
	require.Equal(t, "background preview", preview.Text)
	require.False(t, preview.RecordingNote)
	wake := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "state" && e.RecordingNote })
	require.NotEmpty(t, wake.ActiveNoteSessionID)
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
	status := v.voiceStatus().VoiceStatus
	require.Equal(t, "sherpa-onnx", status.HelperEngine)
	require.Equal(t, "cuda", status.EffectiveProvider)
	require.False(t, v.listen(context.Background(), ipc.Message{}, true).OK, "legacy capture must remain exclusive")
	require.False(t, v.stream(context.Background(), ipc.Message{}).OK)
	stopped := v.stopListener(true)
	require.True(t, stopped.OK, stopped.Msg)
	saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
	require.Equal(t, wake.ActiveNoteSessionID, saved.NoteSessionID)
	require.False(t, saved.RecordingNote)
	selectedFinal := log.wait(t, func(e voiceevents.Event) bool {
		return e.Type == "transcript" && e.Final && e.NoteSessionID == saved.NoteSessionID
	})
	require.Equal(t, "Ginkgo: Keep Exact CASE!", selectedFinal.Text)
	require.Less(t, wake.Seq, selectedFinal.Seq)
	require.Less(t, selectedFinal.Seq, saved.Seq)
	entry, err := v.notes.ScopedGet(context.Background(), saved.NoteID, "work")
	require.NoError(t, err)
	require.Equal(t, "Keep Exact CASE!", entry.Body)
	source := entry.Provenance.Sources[0]
	require.Equal(t, "Ginkgo: Keep Exact CASE!", source.OriginalText)
	require.Equal(t, "sherpa-onnx", source.STTEngine)
	require.Equal(t, "parakeet-tdt-0.6b-v2", source.STTModel)
	require.Equal(t, "en", source.Language)
	commands, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "init auto keyword=GINKGO\nstart\nwake\n", string(commands))
	count := 0
	for _, e := range log.all() {
		if e.Type == "note.saved" {
			count++
		}
	}
	require.Equal(t, 1, count)
}

func TestSherpaProviderReadySnapshots(t *testing.T) {
	for _, tt := range []struct {
		mode, requested, effective, reason string
		starts                             int
	}{
		{"normal", "cpu", "cpu", "", 1}, {"normal", "cuda", "cuda", "", 1}, {"fallback", "auto", "cpu", "cuda_unavailable", 1}, {"retry", "auto", "cpu", "cuda_init_failed", 1},
	} {
		t.Run(tt.mode+tt.requested, func(t *testing.T) {
			v, log, path := sherpaRuntime(t, tt.mode)
			v.sherpaSettings.Set("voice.sherpa.provider", tt.requested)
			r := v.stream(context.Background(), ipc.Message{})
			require.True(t, r.OK, r.Msg)
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" })
			s := v.voiceStatus().VoiceStatus
			require.Equal(t, tt.requested, s.RequestedProvider)
			require.Equal(t, tt.effective, s.EffectiveProvider)
			require.Equal(t, tt.reason, s.ProviderFallbackReason)
			require.True(t, v.stopListener(true).OK)
			commands, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tt.starts, strings.Count(string(commands), "start\n"))
			if tt.mode == "retry" {
				require.Equal(t, 2, strings.Count(string(commands), "init "))
			}
		})
	}
}

func TestSherpaNoSaveAndFaultIsolation(t *testing.T) {
	for _, mode := range []string{"normal", "malicious-savefalse", "fault", "eof", "crash"} {
		t.Run(mode, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, mode)
			r := v.stream(context.Background(), ipc.Message{})
			require.True(t, r.OK, r.Msg)
			if mode == "normal" {
				log.wait(t, func(e voiceevents.Event) bool { return e.Type == "transcript" })
				require.True(t, v.stopListener(true).OK)
			} else {
				log.wait(t, func(e voiceevents.Event) bool { return e.Type == "error" })
				require.Eventually(t, func() bool { return !v.voiceStatus().VoiceStatus.Active }, 5*time.Second, time.Millisecond)
				require.False(t, v.events.Snapshot().Listening)
				require.NotContains(t, v.voiceStatus().VoiceStatus.LastError, "/not-probed")
			}
			require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
			for _, e := range log.all() {
				require.NotEqual(t, "note.saved", e.Type)
			}
		})
	}
}

type fakeWorker func(context.Context, voiceworker.Config, <-chan struct{}, func(voiceworker.Event) error) error

func (f fakeWorker) Run(ctx context.Context, c voiceworker.Config, stop <-chan struct{}, emit func(voiceworker.Event) error) error {
	return f(ctx, c, stop, emit)
}

func TestSherpaCancelJoinsPersistenceAndRevokesLateCallback(t *testing.T) {
	v, log, _ := sherpaRuntime(t, "unused")
	entered, release, runExited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	callback := make(chan func(voiceworker.Event) error, 1)
	v.persistSherpa = func(context.Context, ipc.Message, voice.Transcript, string, string) (api.Entry, error) {
		close(entered)
		<-release
		return api.Entry{}, errors.New("canceled save")
	}
	v.sherpaRunner = fakeWorker(func(ctx context.Context, _ voiceworker.Config, _ <-chan struct{}, emit func(voiceworker.Event) error) error {
		callback <- emit
		if err := emit(voiceworker.Event{Type: "wake", SessionID: "selected", RecordingNote: true}); err != nil {
			return err
		}
		if err := emit(voiceworker.Event{Type: "note_candidate", Candidate: &voiceworker.Candidate{SessionID: "selected", Body: "exact body", Original: "exact body"}}); err != nil {
			return err
		}
		<-ctx.Done()
		close(runExited)
		return ctx.Err()
	})
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true}).OK)
	awaitListener(t, entered)
	ack := make(chan ipc.Response, 1)
	go func() { ack <- v.stopListener(false) }()
	awaitListener(t, runExited)
	select {
	case <-ack:
		t.Fatal("cancel acknowledged before persistence joined")
	default:
	}
	require.True(t, v.voiceStatus().VoiceStatus.Active)
	require.False(t, v.stream(context.Background(), ipc.Message{}).OK)
	close(release)
	require.True(t, (<-ack).OK)
	old := <-callback
	newReady := make(chan struct{})
	v.sherpaRunner = fakeWorker(func(ctx context.Context, _ voiceworker.Config, _ <-chan struct{}, emit func(voiceworker.Event) error) error {
		err := emit(voiceworker.Event{Type: "ready", Provider: &voiceworker.ProviderStatus{Requested: voiceworker.Auto, Effective: voiceworker.CPU, FallbackReason: "cuda_unavailable"}})
		close(newReady)
		if err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	require.True(t, v.stream(context.Background(), ipc.Message{}).OK)
	awaitListener(t, newReady)
	before := v.events.Snapshot().Seq
	require.Error(t, old(voiceworker.Event{Type: "wake", SessionID: "stale", RecordingNote: true}))
	require.Equal(t, before, v.events.Snapshot().Seq)
	require.True(t, v.voiceStatus().VoiceStatus.Active, "late callback must not cancel replacement job")
	require.Equal(t, "cpu", v.voiceStatus().VoiceStatus.EffectiveProvider)
	require.True(t, v.stopListener(false).OK)
	require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
	for _, e := range log.all() {
		require.NotEqual(t, "note.saved", e.Type)
	}
}

func TestSherpaSelectedCancelFaultAndEOFNeverSave(t *testing.T) {
	for _, mode := range []string{"cancel-selected", "fault", "eof", "normal"} {
		t.Run(mode, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, mode)
			require.True(t, v.stream(context.Background(), ipc.Message{Save: true}).OK)
			if mode == "normal" || mode == "cancel-selected" {
				log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
				require.True(t, v.stopListener(mode == "cancel-selected").OK)
			} else {
				log.wait(t, func(e voiceevents.Event) bool { return e.Type == "error" })
				require.Eventually(t, func() bool { return !v.voiceStatus().VoiceStatus.Active }, 5*time.Second, time.Millisecond)
			}
			log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.canceled" })
			require.Empty(t, v.voiceStatus().VoiceStatus.LastNoteID)
		})
	}
}

func TestSherpaQueuedCandidateDiscardedOnCancel(t *testing.T) {
	v, _, _ := sherpaRuntime(t, "unused")
	entered, release, queued := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	v.persistSherpa = func(context.Context, ipc.Message, voice.Transcript, string, string) (api.Entry, error) {
		calls++
		close(entered)
		<-release
		return api.Entry{}, errors.New("canceled")
	}
	v.sherpaRunner = fakeWorker(func(ctx context.Context, _ voiceworker.Config, _ <-chan struct{}, emit func(voiceworker.Event) error) error {
		for _, id := range []string{"first", "queued"} {
			if err := emit(voiceworker.Event{Type: "wake", SessionID: id, RecordingNote: true}); err != nil {
				return err
			}
			if err := emit(voiceworker.Event{Type: "note_candidate", Candidate: &voiceworker.Candidate{SessionID: id, Body: id, Original: id}}); err != nil {
				return err
			}
			if id == "first" {
				<-entered
			}
		}
		close(queued)
		<-ctx.Done()
		return ctx.Err()
	})
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true}).OK)
	awaitListener(t, queued)
	ack := make(chan ipc.Response, 1)
	go func() { ack <- v.stopListener(false) }()
	require.Eventually(t, func() bool { return v.voiceStatus().VoiceStatus.State == "canceling" }, 5*time.Second, time.Millisecond)
	close(release)
	require.True(t, (<-ack).OK)
	require.Equal(t, 1, calls)
}

func TestSherpaDisabledAndStatusDoNotResolveExecutables(t *testing.T) {
	v, _, _ := sherpaRuntime(t, "unused")
	v.enabled = false
	v.sherpaSettings.Set("voice.capture.executable", "/not-installed/recorder")
	v.sherpaSettings.Set("voice.sherpa.executable", "/not-installed/helper")
	v.sherpaSettings.Set("voice.sherpa.provider", "invalid")
	require.Contains(t, v.stream(context.Background(), ipc.Message{}).Msg, "disabled")
	require.True(t, v.voiceStatus().OK)
	require.True(t, v.stopListener(false).OK)
}

func TestSherpaCancelBeforeReadyNeverStartsOrRetries(t *testing.T) {
	v, _, path := sherpaRuntime(t, "no-ready")
	require.True(t, v.stream(context.Background(), ipc.Message{}).OK)
	require.Eventually(t, func() bool { b, _ := os.ReadFile(path); return strings.Contains(string(b), "init ") }, 5*time.Second, time.Millisecond)
	require.True(t, v.stopListener(false).OK)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(b), "init "))
	require.NotContains(t, string(b), "start")
}

func TestSherpaEmptySelectorUsesConfiguration(t *testing.T) {
	for _, tt := range []struct{ configured, requested, want string }{
		{"GINKGO", "", "GINKGO"},
		{"", "", ""},
		{"GINKGO", "OTHER_LABEL", "OTHER_LABEL"},
	} {
		t.Run(tt.configured+"/"+tt.requested, func(t *testing.T) {
			v, _, _ := sherpaRuntime(t, "unused")
			v.sherpaSettings.Set("voice.sherpa.keyword", tt.configured)
			selected := make(chan string, 1)
			v.sherpaRunner = fakeWorker(func(ctx context.Context, c voiceworker.Config, _ <-chan struct{}, _ func(voiceworker.Event) error) error {
				selected <- c.SelectedKeyword
				<-ctx.Done()
				return ctx.Err()
			})
			r := v.stream(context.Background(), ipc.Message{Trigger: tt.requested})
			require.True(t, r.OK, r.Msg)
			require.Equal(t, tt.want, <-selected)
			require.True(t, v.stopListener(false).OK)
		})
	}
}
