package tests

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Native fixtures and expected private dictation stay outside the repository.
// Config supplies real helper/model paths; cases supplies a JSON array of:
// {"name":"...","wav":"/private/file.wav","want":["body substring"],
//
//	"save":true,"action":"stop","stop_at_ms":0,"require_selection":false}.
//
// An empty want array asserts zero notes. stop_at_ms pauses PCM at that point
// (instead of EOF + 4s silence), allowing stop/cancel during a selected recording.
type nativeVoiceCase struct {
	Name             string   `json:"name"`
	WAV              string   `json:"wav"`
	Want             []string `json:"want"`
	Save             bool     `json:"save"`
	Action           string   `json:"action"`
	StopAtMS         int      `json:"stop_at_ms"`
	RequireSelection bool     `json:"require_selection"`
}

// TestSherpaNativeCLIWorkflow is deliberately opt-in: it executes the installed
// native helper and CPU models, but never a microphone, real HOME, or real DB.
func TestSherpaNativeCLIWorkflow(t *testing.T) {
	configPath, casesPath := os.Getenv("GINKGO_VOICE_E2E_CONFIG"), os.Getenv("GINKGO_VOICE_E2E_CASES")
	if configPath == "" || casesPath == "" {
		t.Skip("set GINKGO_VOICE_E2E_CONFIG and GINKGO_VOICE_E2E_CASES to private native assets")
	}
	base := viper.New()
	base.SetConfigFile(configPath)
	require.NoError(t, base.ReadInConfig())
	var cases []nativeVoiceCase
	b, err := os.ReadFile(casesPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &cases))
	require.NotEmpty(t, cases)
	binDir := t.TempDir()
	cli := filepath.Join(binDir, "ginkgo-cli")
	build := exec.Command("go", "build", "-o", cli, "../cmd/ginkgo-cli")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off", "GOTOOLCHAIN=local")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	daemon := filepath.Join(binDir, "ginkgod")
	require.NoError(t, os.Symlink(cli, daemon))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) { runNativeVoiceCase(t, base, cli, daemon, tc) })
	}
}

func runNativeVoiceCase(t *testing.T, base *viper.Viper, cli, daemon string, tc nativeVoiceCase) {
	t.Helper()
	require.Contains(t, []string{"stop", "cancel"}, tc.Action)
	require.True(t, filepath.IsAbs(tc.WAV))
	root := t.TempDir()
	if artifacts := os.Getenv("GINKGO_VOICE_E2E_ARTIFACTS"); artifacts != "" {
		require.True(t, filepath.IsAbs(artifacts), "artifact directory must be an absolute private path")
		require.NoError(t, os.MkdirAll(artifacts, 0700))
		var err error
		root, err = os.MkdirTemp(artifacts, "native-")
		require.NoError(t, err)
	}
	caseBytes, err := json.Marshal(tc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "case.json"), caseBytes, 0600))
	buildEvidence := map[string]string{}
	for name, path := range map[string]string{"cli": cli, "helper": base.GetString("voice.sherpa.executable")} {
		binary, err := os.ReadFile(path)
		require.NoError(t, err)
		buildEvidence[name+"_sha256"] = fmt.Sprintf("%x", sha256.Sum256(binary))
	}
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	buildEvidence["source_commit"] = strings.TrimSpace(string(revision))
	buildEvidence["helper_source_commit"] = os.Getenv("GINKGO_VOICE_E2E_HELPER_COMMIT")
	gitStatus, err := exec.Command("git", "status", "--porcelain").Output()
	require.NoError(t, err)
	buildEvidence["worktree_dirty"] = strconv.FormatBool(len(gitStatus) != 0)
	buildBytes, err := json.Marshal(buildEvidence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "build.json"), buildBytes, 0600))
	// Keep Unix socket names short even under Go's long test-name temp directory.
	run, err := os.MkdirTemp("/tmp/opencode", "ginkgo-v-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(run)) })
	env := []string{}
	for _, value := range os.Environ() {
		key := strings.SplitN(value, "=", 2)[0]
		if key != "HOME" && !strings.HasPrefix(key, "XDG_") && !strings.HasPrefix(key, "GINKGO_") {
			env = append(env, value)
		}
	}
	for key, dir := range map[string]string{"HOME": root, "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_RUNTIME_DIR": run} {
		require.NoError(t, os.MkdirAll(dir, 0700))
		env = append(env, key+"="+dir)
	}
	marker, pidPath := filepath.Join(root, "pcm.done"), filepath.Join(root, "recorder.pid")
	testExe, err := os.Executable()
	require.NoError(t, err)
	recorder := filepath.Join(root, "file-recorder")
	// The trusted recorder ignores pw-record arguments and only emits this file.
	require.NoError(t, os.WriteFile(recorder, []byte(fmt.Sprintf("#!/bin/sh\nexec %s -test.run=^TestSherpaNativeFileRecorder$ -- %s %s %s %d\n", shellQuote(testExe), shellQuote(tc.WAV), shellQuote(marker), shellQuote(pidPath), tc.StopAtMS)), 0700))
	v := viper.New()
	// Accept only voice settings from the supplied file, never storage/remotes.
	v.Set("voice", base.GetStringMap("voice"))
	v.Set("data_dir", filepath.Join(root, "db"))
	v.Set("namespace", "native-test")
	v.Set("namespaces", map[string]any{"native-test": map[string]any{"e2ee": false}})
	v.Set("http_addr", "127.0.0.1:0")
	v.Set("remotes", map[string]any{})
	v.Set("voice.enabled", true)
	v.Set("voice.events.enabled", true)
	v.Set("voice.stream.enabled", true)
	v.Set("voice.stream.backend", "sherpa")
	v.Set("voice.sherpa.provider", "cpu")
	v.Set("voice.sherpa.cpu_fallback", false)
	v.Set("voice.sherpa.trigger_timeout", "3s")
	v.Set("voice.capture.executable", recorder)
	v.Set("voice.capture.target", "")
	cfg := filepath.Join(root, "config.json")
	b, err := json.Marshal(v.AllSettings())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, b, 0600))
	command := func(args ...string) *exec.Cmd {
		cmd := exec.Command(cli, append([]string{"--config", cfg}, args...)...)
		cmd.Env = env
		return cmd
	}
	runCLI := func(args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, cli, append([]string{"--config", cfg}, args...)...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		// Do not print output: note bodies and paths are private.
		require.NoError(t, err, "CLI command failed: %v", args)
		return out
	}
	d := exec.Command(daemon, "--config", cfg)
	d.Env = env
	log, err := os.OpenFile(filepath.Join(root, "daemon.log"), os.O_CREATE|os.O_WRONLY, 0600)
	require.NoError(t, err)
	d.Stdout, d.Stderr = log, log
	require.NoError(t, d.Start())
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	t.Cleanup(func() {
		_ = d.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = d.Process.Kill()
			<-done
			t.Error("daemon failed to join")
		}
		_ = log.Close()
	})
	// The current CLI also opens storage during bootstrap. Wait for the daemon's
	// socket before invoking it, avoiding concurrent first-run SQLite migrations.
	require.NoError(t, waitForSocket(filepath.Join(run, "ginkgo.sock"), 10*time.Second))
	runCLI("voice", "status")
	watch := command("voice", "watch", "--output", "json")
	pipe, err := watch.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, watch.Start())
	var mu sync.Mutex
	var events []voiceevents.Event
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		decoder := json.NewDecoder(bufio.NewReader(pipe))
		for {
			var e voiceevents.Event
			if decoder.Decode(&e) != nil {
				return
			}
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = watch.Process.Kill(); _ = watch.Wait(); <-watchDone })
	snapshot := func() []voiceevents.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]voiceevents.Event(nil), events...)
	}
	t.Cleanup(func() {
		b, err := json.Marshal(snapshot())
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "events.json"), b, 0600)
		}
		require.NoError(t, err)
	})
	require.Eventually(t, func() bool { return len(snapshot()) > 0 }, 5*time.Second, 10*time.Millisecond)
	runCLI("voice", "stream", "--save="+strconv.FormatBool(tc.Save), "--trigger", "GINKGO", "--namespace", "native-test")
	require.Eventually(t, func() bool {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
		for _, e := range snapshot() {
			if e.Type == "error" {
				return true
			}
		}
		return false
	}, 4*time.Minute, 100*time.Millisecond, "file PCM did not finish; inspect private daemon log")
	for _, e := range snapshot() {
		require.NotEqual(t, "error", e.Type, "native capture failed; inspect private events")
	}
	_, err = os.Stat(marker)
	require.NoError(t, err, "file PCM did not finish")
	var status ipc.Response
	require.NoError(t, json.Unmarshal(runCLI("voice", "status", "--output", "json"), &status))
	require.NotNil(t, status.VoiceStatus)
	require.Equal(t, "cpu", status.VoiceStatus.EffectiveProvider)
	require.Equal(t, "sherpa", status.VoiceStatus.Backend)
	if tc.RequireSelection {
		observations := snapshot()
		require.NotEmpty(t, observations)
		require.True(t, observations[len(observations)-1].RecordingNote, "stop/cancel fixture must still have an active selection")
	}
	runCLI("voice", tc.Action)
	require.Eventually(t, func() bool {
		for _, e := range snapshot() {
			if e.Type == "transcript" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "actual native ASR produced no public transcript")
	// An acknowledged cancel must revoke late ASR/persistence callbacks. Stop must
	// drain candidates before releasing capture. The CLI list reads actual SQLite.
	readEntries := func() []api.Entry {
		var entries []api.Entry
		require.NoError(t, json.Unmarshal(runCLI("note", "list", "--output", "json", "--export"), &entries))
		return entries
	}
	entries := readEntries()
	stored, err := sql.Open("sqlite", "file:"+filepath.Join(root, "db", "ginkgo.db")+"?mode=ro")
	require.NoError(t, err)
	defer stored.Close()
	var persisted int
	require.NoError(t, stored.QueryRow("SELECT count(*) FROM entries").Scan(&persisted))
	require.Equal(t, len(tc.Want), persisted, "direct isolated SQLite count differs from expectations")
	b, err = json.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "entries.json"), b, 0600))
	require.Equal(t, len(tc.Want), len(entries), "unexpected persisted count (private bodies omitted)")
	matchedNotes := map[string]bool{}
	for i, want := range tc.Want {
		found := false
		for _, entry := range entries {
			if !matchedNotes[entry.ID] && strings.Contains(strings.ToLower(entry.Body), strings.ToLower(want)) {
				found = true
				matchedNotes[entry.ID] = true
				break
			}
		}
		require.True(t, found, "expected private dictation %d missing", i)
	}
	require.Eventually(t, func() bool {
		count := 0
		for _, e := range snapshot() {
			if e.Type == "note.saved" {
				count++
			}
		}
		return count == len(entries)
	}, 5*time.Second, 20*time.Millisecond)
	seen := map[string]bool{}
	for _, e := range snapshot() {
		require.NotEqual(t, "error", e.Type, "native workflow emitted public error")
		require.NotEqual(t, "note.error", e.Type, "native persistence failed")
		if e.Type != "note.saved" {
			continue
		}
		require.NotEmpty(t, e.NoteSessionID)
		require.False(t, seen[e.NoteSessionID], "duplicate session persistence")
		seen[e.NoteSessionID] = true
		matched := false
		for _, entry := range entries {
			if entry.ID != e.NoteID {
				continue
			}
			require.NotEmpty(t, entry.Provenance.Sources)
			source := entry.Provenance.Sources[0]
			require.Equal(t, "sherpa-onnx", source.STTEngine)
			require.Equal(t, "parakeet-tdt-0.6b-v2", source.STTModel)
			require.Equal(t, "en", source.Language)
			for _, transcript := range snapshot() {
				if transcript.Type == "transcript" && transcript.Final && transcript.NoteSessionID == e.NoteSessionID {
					require.True(t, source.OriginalText == transcript.Text, "OriginalText differs from selected final")
					require.True(t, entry.Body == source.OriginalText, "conservative native body must retain exact transcript")
					matched = true
				}
			}
		}
		require.True(t, matched, "saved entry lacks exact selected-session final")
	}
	// No recorder process can survive the acknowledged stop/cancel.
	pidBytes, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(pidBytes))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 5*time.Second, 20*time.Millisecond)
	tasks, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", d.Process.Pid))
	require.NoError(t, err)
	require.NotEmpty(t, tasks)
	for _, task := range tasks {
		children, err := os.ReadFile(task)
		if os.IsNotExist(err) {
			continue
		} // A runtime thread can exit meanwhile.
		require.NoError(t, err)
		require.Empty(t, strings.TrimSpace(string(children)), "daemon retained a helper child after acknowledgement")
	}
	time.Sleep(250 * time.Millisecond)
	require.Equal(t, len(entries), len(readEntries()), "late persistence after acknowledgement")
	t.Logf("persisted=%d; exact selected provenance/session attribution checked; recorder joined", len(entries))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// TestSherpaNativeFileRecorder is an actual subprocess PCM executable, not a
// simulated helper/ASR. The real helper still owns capture and native inference.
func TestSherpaNativeFileRecorder(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	if err := nativeFilePCM(args[1:]); err != nil {
		os.Exit(71)
	}
	os.Exit(0)
}

func nativeFilePCM(args []string) error {
	if len(args) < 4 {
		return fmt.Errorf("missing recorder arguments")
	}
	if err := os.WriteFile(args[2], []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, 12)
	if _, err := io.ReadFull(f, header); err != nil {
		return err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return fmt.Errorf("not WAV")
	}
	var pcm []byte
	validFormat := false
	for {
		chunk := make([]byte, 8)
		if _, err := io.ReadFull(f, chunk); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(chunk[4:])
		if n > 64<<20 {
			return fmt.Errorf("oversized fixture")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(f, data); err != nil {
			return err
		}
		if n%2 != 0 {
			if _, err := f.Seek(1, io.SeekCurrent); err != nil {
				return err
			}
		}
		if string(chunk[:4]) == "fmt " {
			validFormat = len(data) >= 16 && binary.LittleEndian.Uint16(data) == 1 && binary.LittleEndian.Uint16(data[2:]) == 1 && binary.LittleEndian.Uint32(data[4:]) == 16000 && binary.LittleEndian.Uint16(data[14:]) == 16
		}
		if string(chunk[:4]) == "data" {
			pcm = data
			break
		}
	}
	if !validFormat {
		return fmt.Errorf("require mono 16kHz PCM16")
	}
	stopMS, err := strconv.Atoi(args[3])
	if err != nil {
		return err
	}
	if stopMS > 0 {
		n := stopMS * 32
		if n > len(pcm) {
			return fmt.Errorf("stop exceeds fixture")
		}
		pcm = pcm[:n]
	} else {
		pcm = append(pcm, make([]byte, 4*32000)...)
	}
	clock := time.NewTicker(32 * time.Millisecond)
	defer clock.Stop()
	for len(pcm) > 0 {
		<-clock.C
		n := min(1024, len(pcm))
		if _, err := os.Stdout.Write(pcm[:n]); err != nil {
			return err
		}
		pcm = pcm[n:]
	}
	if err := os.WriteFile(args[1], []byte("complete"), 0600); err != nil {
		return err
	}
	// Hold capture open: EOF is a fault, not successful note finalization.
	for {
		time.Sleep(time.Hour)
	}
}
