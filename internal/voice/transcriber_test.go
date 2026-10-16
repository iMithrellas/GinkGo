package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func wavFixture() []byte {
	data := make([]byte, 44+320)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 16000)
	binary.LittleEndian.PutUint32(data[28:], 32000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 320)
	return data
}

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func setup(t *testing.T, body string) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	model := writeFixture(t, filepath.Join(dir, "model with spaces.bin"), []byte("lmggfake model"), 0600)
	wav := writeFixture(t, filepath.Join(dir, "audio with spaces.wav"), wavFixture(), 0600)
	script := `#!/bin/sh
set -eu
out=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -of) out="$2"; shift 2 ;;
    -m|-f) test -r "$2"; shift 2 ;;
    -t|-l) shift 2 ;;
    -oj|-np|-nt|-ng) shift ;;
    *) exit 87 ;;
  esac
done
test -n "$out"
` + body
	exe := writeFixture(t, filepath.Join(dir, "fake whisper"), []byte(script), 0700)
	return Config{Enabled: true, Executable: exe, Model: model, Device: "cpu", Timeout: 3 * time.Second}, wav
}

func TestTranscribeExactTextAndMetadata(t *testing.T) {
	c, wav := setup(t, `printf '%s' '{"result":{"language":"fr"},"transcription":[{"text":"  Bonjour"},{"text":"\nmonde!  "}]}' > "$out.json"`)
	// Both symlinks are resolved, then passed as pinned file descriptors.
	model := c.Model
	c.Model = filepath.Join(t.TempDir(), "model-link")
	if err := os.Symlink(model, c.Model); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "audio-link")
	if err := os.Symlink(wav, link); err != nil {
		t.Fatal(err)
	}
	got, err := NewTranscriber(c).Transcribe(context.Background(), link, Options{Language: "fr"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "  Bonjour\nmonde!  " || got.Language != "fr" || got.Engine != "whisper.cpp" || got.Model != model {
		t.Fatalf("unexpected transcript: %#v", got)
	}
}

func TestDisabledAndLazyConstruction(t *testing.T) {
	tr := NewTranscriber(Config{Executable: "/missing", Model: "/missing", Device: "unsupported"})
	_, err := tr.Transcribe(context.Background(), "/missing", Options{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v", err)
	}
}

func TestOnDemandAndOverrides(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "calls")
	c, wav := setup(t, fmt.Sprintf("echo call >> '%s'\nprintf '%%s' '{\"transcription\":[{\"text\":\"raw\"}]}' > \"$out.json\"", marker))
	model := c.Model
	c.Model = "/not-installed"
	c.Language = "invalid-default"
	tr := NewTranscriber(c)
	for range 2 {
		got, err := tr.Transcribe(context.Background(), wav, Options{Model: model, Language: "en"})
		if err != nil || got.Text != "raw" || got.Language != "" {
			t.Fatalf("got %#v, %v", got, err)
		}
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "call\ncall\n" {
		t.Fatalf("expected one fresh engine per call: %q, %v", calls, err)
	}
}

func TestQueuedRequestDeadline(t *testing.T) {
	tr := NewTranscriber(Config{Enabled: true, Timeout: 10 * time.Millisecond})
	tr.slot <- struct{}{} // Simulate the single active inference.
	defer func() { <-tr.slot }()
	_, err := tr.Transcribe(context.Background(), "/not-probed", Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestNonRegularInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegular(path, 1024); err == nil {
		t.Fatal("FIFO accepted")
	}
	if _, _, err := openRegular(t.TempDir(), 1024); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestInvalidInputsDoNotLaunch(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config, string)
	}{
		{"invalid wav", func(c *Config, wav string) { writeFixture(t, wav, []byte("not WAV"), 0600) }},
		{"wrong rate", func(c *Config, wav string) {
			data := wavFixture()
			binary.LittleEndian.PutUint32(data[24:], 44100)
			writeFixture(t, wav, data, 0600)
		}},
		{"oversize wav", func(c *Config, wav string) { c.MaxAudioBytes = 44 }},
		{"duration", func(c *Config, wav string) { c.MaxAudioDuration = time.Millisecond }},
		{"missing model", func(c *Config, wav string) { c.Model += ".missing" }},
		{"bad model", func(c *Config, wav string) { writeFixture(t, c.Model, []byte("nope"), 0600) }},
		{"model directory", func(c *Config, wav string) { c.Model = t.TempDir() }},
		{"model limit", func(c *Config, wav string) { c.MaxModelBytes = 4 }},
		{"gpu", func(c *Config, wav string) { c.Device = "gpu" }},
		{"bad language", func(c *Config, wav string) { c.Language = "en --translate" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "launched")
			c, wav := setup(t, fmt.Sprintf("touch '%s'\nexit 99\n", marker))
			test.change(&c, wav)
			if _, err := NewTranscriber(c).Transcribe(context.Background(), wav, Options{}); err == nil {
				t.Fatal("expected error")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("engine launched: %v", err)
			}
		})
	}
}

func TestEngineOutputs(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
		limit      bool
	}{
		{"empty speech", `printf '%s' '{"transcription":[]}' > "$out.json"`, false, false},
		{"whitespace speech", `printf '%s' '{"transcription":[{"text":"  \n"}]}' > "$out.json"`, false, false},
		{"missing output", `exit 0`, true, false},
		{"invalid json", `printf 'no json' > "$out.json"`, true, false},
		{"missing segments", `printf '{}' > "$out.json"`, true, false},
		{"missing text", `printf '%s' '{"transcription":[{}]}' > "$out.json"`, true, false},
		{"failure", `echo 'private engine diagnostic' >&2; exit 8`, true, false},
		{"json bound", `head -c 4096 /dev/zero > "$out.json"`, true, true},
		{"stdout bound", `head -c 4096 /dev/zero`, true, true},
		{"stderr bound", `head -c 4096 /dev/zero >&2`, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, wav := setup(t, test.body)
			c.MaxOutputBytes = 128
			got, err := NewTranscriber(c).Transcribe(context.Background(), wav, Options{})
			if (err != nil) != test.wantError {
				t.Fatalf("got %#v, %v", got, err)
			}
			if test.limit && !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("expected output limit, got %v", err)
			}
			if !test.wantError && strings.TrimSpace(got.Text) != "" {
				t.Fatalf("caller must not create a note for %#v", got)
			}
			if err != nil && strings.Contains(err.Error(), "private engine diagnostic") {
				t.Fatal("leaked engine diagnostics")
			}
		})
	}
}

func TestCancellationKillsProcessGroup(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			c, wav := setup(t, fmt.Sprintf("sleep 60 &\necho $! > '%s'\nwait\n", pidPath))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				c.Timeout = 200 * time.Millisecond
			}
			done := make(chan error, 1)
			go func() { _, err := NewTranscriber(c).Transcribe(ctx, wav, Options{}); done <- err }()
			var pid int
			until := time.Now().Add(2 * time.Second)
			for time.Now().Before(until) {
				data, _ := os.ReadFile(pidPath)
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("engine child did not start")
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			if !deadline {
				cancel()
			}
			select {
			case err := <-done:
				want := context.Canceled
				if deadline {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation blocked")
			}
			// Orphaned killed children can remain zombies until init reaps them.
			for time.Now().Before(until) {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), ") Z ") {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("descendant still running after cancellation")
		})
	}
}

func TestWAVValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"truncated", func(b []byte) []byte { return b[:20] }},
		{"trailing bytes", func(b []byte) []byte { return append(b, 1) }},
		{"stereo", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[22:], 2); return b }},
		{"float", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[20:], 3); return b }},
		{"alignment", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[32:], 1); return b }},
		{"overflow chunk", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], ^uint32(0)); return b }},
		{"no data", func(b []byte) []byte { copy(b[36:], "JUNK"); return b }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWAV(test.mutate(wavFixture()), time.Minute); err == nil || !strings.Contains(err.Error(), "16000 Hz") {
				t.Fatalf("expected actionable WAV error, got %v", err)
			}
		})
	}
}
