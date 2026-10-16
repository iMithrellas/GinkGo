// Package voice provides optional local file transcription, independently of AI
// and note storage. The on-demand adapter currently requires Linux (/proc).
package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// ErrDisabled indicates that transcription was not enabled. No paths are probed.
var ErrDisabled = errors.New("voice transcription is disabled")

// ErrOutputLimit indicates an engine output exceeded a configured bound.
var ErrOutputLimit = errors.New("whisper-cli output exceeds configured limit")

// Config selects a trusted, user-installed whisper.cpp executable and model.
// Zero limits select conservative defaults. Construction performs no I/O.
type Config struct {
	Enabled          bool
	Executable       string
	Model            string
	Language         string
	Device           string // auto or cpu; explicit gpu is rejected (CLI cannot guarantee it).
	Threads          int
	Timeout          time.Duration
	MaxAudioBytes    int64
	MaxAudioDuration time.Duration
	MaxModelBytes    int64
	MaxOutputBytes   int64 // Each of stdout, stderr and structured JSON output.
}

// Options contains optional per-request model and language overrides.
type Options struct {
	Model    string
	Language string
}

// Transcript preserves segment text exactly, without trimming or AI rewriting.
// Empty/whitespace-only Text means no note should be created by the caller.
type Transcript struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	Engine   string `json:"engine"`
	Model    string `json:"model"`
}

// Transcriber runs one short-lived whisper-cli process per request.
type Transcriber struct {
	config Config
	slot   chan struct{}
}

// NewTranscriber constructs a lazy adapter; it never probes installed software.
func NewTranscriber(config Config) *Transcriber {
	return &Transcriber{config: config, slot: make(chan struct{}, 1)}
}

var languagePattern = regexp.MustCompile(`^[a-z]{2,3}$`)

func (t *Transcriber) settings(options Options) (Config, error) {
	c := t.config
	if !c.Enabled {
		return c, ErrDisabled
	}
	if options.Model != "" {
		c.Model = options.Model
	}
	if options.Language != "" {
		c.Language = options.Language
	}
	if c.Executable == "" {
		c.Executable = "whisper-cli"
	}
	if c.Language == "" {
		c.Language = "auto"
	}
	if c.Language != "auto" && !languagePattern.MatchString(c.Language) {
		return c, errors.New("voice language must be auto or a lowercase two/three-letter language code")
	}
	if c.Device == "" {
		c.Device = "auto"
	}
	if c.Device != "auto" && c.Device != "cpu" {
		return c, errors.New("voice device must be auto or cpu; whisper-cli cannot guarantee explicit GPU availability in this POC")
	}
	if c.Threads == 0 {
		c.Threads = 4
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.MaxAudioBytes == 0 {
		c.MaxAudioBytes = 32 << 20
	}
	if c.MaxAudioDuration == 0 {
		c.MaxAudioDuration = 5 * time.Minute
	}
	if c.MaxModelBytes == 0 {
		c.MaxModelBytes = 8 << 30
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 1 << 20
	}
	if c.Threads < 1 || c.Threads > 256 || c.Timeout < 0 || c.MaxAudioBytes < 44 || c.MaxAudioDuration < 0 || c.MaxModelBytes < 4 || c.MaxOutputBytes < 1 {
		return c, errors.New("voice requires positive timeout and resource limits, audio limit >= 44, model limit >= 4, and threads between 1 and 256")
	}
	if c.MaxAudioBytes > 1<<30 || c.MaxModelBytes > 16<<30 || c.MaxOutputBytes > 16<<20 {
		return c, errors.New("voice hard limits: audio <= 1 GiB, model <= 16 GiB, each output <= 16 MiB")
	}
	return c, nil
}

// Transcribe validates a local PCM WAV and runs user-installed whisper-cli with
// explicit argv, a bounded deadline, and process-group cancellation. It performs
// no model downloads, note writes, AI calls, or persistent worker initialization.
func (t *Transcriber) Transcribe(ctx context.Context, audioPath string, options Options) (Transcript, error) {
	c, err := t.settings(options)
	if err != nil {
		return Transcript{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	// Bound inference concurrency per adapter; queued callers retain their own
	// request deadline and do not probe/open resources until admitted.
	select {
	case t.slot <- struct{}{}:
		defer func() { <-t.slot }()
	case <-ctx.Done():
		return Transcript{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Transcript{}, err
	}
	audio, _, err := openRegular(audioPath, c.MaxAudioBytes)
	if err != nil {
		return Transcript{}, fmt.Errorf("open WAV: %w", err)
	}
	defer audio.Close()
	data, err := io.ReadAll(io.LimitReader(audio, c.MaxAudioBytes+1))
	if err != nil {
		return Transcript{}, fmt.Errorf("read WAV: %w", err)
	}
	if int64(len(data)) > c.MaxAudioBytes {
		return Transcript{}, errors.New("WAV exceeds audio byte limit")
	}
	if err := validateWAV(data, c.MaxAudioDuration); err != nil {
		return Transcript{}, err
	}
	model, modelPath, err := openRegular(c.Model, c.MaxModelBytes)
	if err != nil {
		return Transcript{}, fmt.Errorf("open installed whisper.cpp model (downloads are not supported): %w", err)
	}
	defer model.Close()
	var magic [4]byte
	if _, err := io.ReadFull(model, magic[:]); err != nil || string(magic[:]) != "lmgg" {
		return Transcript{}, errors.New("model must be a whisper.cpp GGML model (expected GGML magic); install a compatible ggml-*.bin model")
	}
	if _, err := model.Seek(0, io.SeekStart); err != nil {
		return Transcript{}, fmt.Errorf("rewind model: %w", err)
	}
	executable, err := exec.LookPath(c.Executable)
	if err != nil {
		return Transcript{}, fmt.Errorf("find installed whisper-cli: %w", err)
	}
	engine, _, err := openRegular(executable, 0)
	if err != nil {
		return Transcript{}, fmt.Errorf("open whisper-cli: %w", err)
	}
	defer engine.Close()
	info, err := engine.Stat()
	if err != nil || info.Mode().Perm()&0111 == 0 {
		return Transcript{}, errors.New("whisper-cli must be an executable regular file")
	}
	dir, err := os.MkdirTemp("", "ginkgo-voice-")
	if err != nil {
		return Transcript{}, fmt.Errorf("create private voice directory: %w", err)
	}
	defer os.RemoveAll(dir)
	// Snapshot the bounded WAV so later caller edits cannot change validated audio.
	wavPath := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(wavPath, data, 0600); err != nil {
		return Transcript{}, fmt.Errorf("snapshot WAV: %w", err)
	}
	wav, err := os.Open(wavPath)
	if err != nil {
		return Transcript{}, fmt.Errorf("open WAV snapshot: %w", err)
	}
	defer wav.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		return Transcript{}, fmt.Errorf("create JSON pipe: %w", err)
	}
	defer reader.Close()
	defer writer.Close()
	prefix := filepath.Join(dir, "result")
	// whisper-cli appends .json. Redirect that file to an inherited pipe: no
	// unbounded temporary output file can be created by the normal engine.
	if err := os.Symlink("/proc/self/fd/5", prefix+".json"); err != nil {
		return Transcript{}, fmt.Errorf("prepare bounded JSON output: %w", err)
	}
	args := []string{"-m", "/proc/self/fd/4", "-f", "/proc/self/fd/3", "-l", c.Language, "-t", fmt.Sprint(c.Threads), "-oj", "-of", prefix, "-np", "-nt"}
	if c.Device == "cpu" {
		args = append(args, "-ng")
	}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/6", args...)
	cmd.ExtraFiles = []*os.File{wav, model, writer, engine}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	cmd.Cancel = func() error { kill(); return nil }
	cmd.WaitDelay = time.Second
	stdout := &boundedOutput{limit: c.MaxOutputBytes, cancel: cancel}
	stderr := &boundedOutput{limit: c.MaxOutputBytes, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return Transcript{}, fmt.Errorf("start whisper-cli: %w", err)
	}
	defer kill()
	_ = writer.Close()
	jsonOutput := &boundedOutput{limit: c.MaxOutputBytes, cancel: cancel}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(jsonOutput, reader); done <- err }()
	waitErr := cmd.Wait()
	kill() // Also stop descendants retaining the JSON/stdout/stderr pipes.
	var readErr error
	select {
	case readErr = <-done:
	case <-time.After(time.Second):
		_ = reader.Close() // Detached descendants cannot hold the request open.
		<-done
		readErr = errors.New("whisper-cli JSON pipe did not close")
	}
	if stdout.exceeded || stderr.exceeded || jsonOutput.exceeded {
		return Transcript{}, ErrOutputLimit
	}
	if err := ctx.Err(); err != nil {
		return Transcript{}, fmt.Errorf("transcribe WAV: %w", err)
	}
	// ErrWaitDelay means the parent exited successfully but inherited stdout or
	// stderr pipes remained open. The group is now killed and those diagnostic
	// pipes are closed; do not discard valid JSON solely for that cleanup delay.
	// Context errors, output overflow, JSON read failures and malformed/missing
	// JSON remain errors, so incomplete transcription can never become success.
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		return Transcript{}, fmt.Errorf("whisper-cli failed (verify compatible CLI flags and GGML model): %w", waitErr)
	}
	if readErr != nil {
		return Transcript{}, fmt.Errorf("read whisper-cli JSON: %w", readErr)
	}
	text, language, err := parseTranscript(jsonOutput.data)
	if err != nil {
		return Transcript{}, err
	}
	return Transcript{Text: text, Language: language, Engine: "whisper.cpp", Model: modelPath}, nil
}

// openRegular resolves links and pins the resulting regular inode. Nonblocking
// open avoids FIFO hangs; O_NOFOLLOW rejects a final-component replacement race.
func openRegular(path string, limit int64) (*os.File, string, error) {
	if path == "" {
		return nil, "", errors.New("path is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, "", err
	}
	fd, err := syscall.Open(resolved, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	file := os.NewFile(uintptr(fd), resolved)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || (limit > 0 && info.Size() > limit) {
		file.Close()
		return nil, "", errors.New("must be a nonempty regular file within the configured byte limit")
	}
	return file, resolved, nil
}

type boundedOutput struct {
	data     []byte
	limit    int64
	exceeded bool
	cancel   context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	remaining := b.limit - int64(len(b.data))
	if int64(len(p)) > remaining {
		b.data = append(b.data, p[:int(remaining)]...)
		b.exceeded = true
		b.cancel()
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func parseTranscript(data []byte) (string, string, error) {
	var result struct {
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription *[]struct {
			Text *string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", "", fmt.Errorf("decode whisper-cli JSON (requires -oj output schema): %w", err)
	}
	if result.Transcription == nil {
		return "", "", errors.New("whisper-cli JSON is missing transcription array")
	}
	var text strings.Builder
	for _, segment := range *result.Transcription {
		if segment.Text == nil {
			return "", "", errors.New("whisper-cli JSON segment is missing text")
		}
		text.WriteString(*segment.Text)
	}
	return text.String(), result.Result.Language, nil
}
