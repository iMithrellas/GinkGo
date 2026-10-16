package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO gives the test explicit control over when an already-running engine
// may read its audio or complete. Opening both ends avoids blocking the test on
// an engine that failed to launch; the request context handles failure cleanup.
func engineGate(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	gate, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	return path, gate
}

func waitForFile(t *testing.T, path string, ready func([]byte) bool) []byte {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		data, err := os.ReadFile(path)
		if err == nil && ready(data) {
			return data
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for engine marker %s", path)
	return nil
}

type transcriptionResult struct {
	transcript Transcript
	err        error
}

func startTranscription(tr *Transcriber, ctx context.Context, wav string) <-chan transcriptionResult {
	done := make(chan transcriptionResult, 1)
	go func() {
		transcript, err := tr.Transcribe(ctx, wav, Options{})
		done <- transcriptionResult{transcript: transcript, err: err}
	}()
	return done
}

func awaitTranscription(t *testing.T, done <-chan transcriptionResult) transcriptionResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(4 * time.Second):
		t.Fatal("transcription did not complete")
		return transcriptionResult{}
	}
}

func releaseEngine(t *testing.T, gate *os.File) {
	t.Helper()
	if _, err := gate.WriteString("continue\n"); err != nil {
		t.Fatal(err)
	}
}

func TestAudioSnapshotSurvivesCallerMutation(t *testing.T) {
	for _, replacePath := range []bool{false, true} {
		t.Run(fmt.Sprint("replace-path=", replacePath), func(t *testing.T) {
			dir := t.TempDir()
			ready := filepath.Join(dir, "ready")
			observed := filepath.Join(dir, "observed.wav")
			gatePath, gate := engineGate(t)
			c, wav := setup(t, fmt.Sprintf(`echo ready > '%s'
read -r token < '%s'
cat /proc/self/fd/3 > '%s'
printf '%%s' '{"transcription":[{"text":"snapshot"}]}' > "$out.json"
`, ready, gatePath, observed))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startTranscription(NewTranscriber(c), ctx, wav)
			waitForFile(t, ready, func(data []byte) bool { return len(data) > 0 })
			original := wavFixture()
			mutated := bytes.Clone(original)
			for i := 44; i < len(mutated); i++ {
				mutated[i] = 0x7f
			}
			if replacePath {
				replacement := writeFixture(t, filepath.Join(dir, "replacement.wav"), mutated, 0600)
				if err := os.Rename(replacement, wav); err != nil {
					t.Fatal(err)
				}
			} else {
				// Truncate/write the same inode, not merely a different pathname.
				writeFixture(t, wav, mutated, 0600)
			}
			releaseEngine(t, gate)
			result := awaitTranscription(t, done)
			if result.err != nil || result.transcript.Text != "snapshot" {
				t.Fatalf("got %#v, %v", result.transcript, result.err)
			}
			got, err := os.ReadFile(observed)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("engine did not read original snapshot: %v (length %d)", err, len(got))
			}
		})
	}
}

func TestConcurrentTranscriptionsSerializeAndCancelQueued(t *testing.T) {
	for _, cancelQueued := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel-queued=", cancelQueued), func(t *testing.T) {
			dir := t.TempDir()
			callsPath := filepath.Join(dir, "calls")
			activePath := filepath.Join(dir, "active")
			gatePath, gate := engineGate(t)
			c, wav := setup(t, fmt.Sprintf(`mkdir '%s' || exit 88
echo call >> '%s'
read -r token < '%s'
rmdir '%s'
printf '%%s' '{"transcription":[{"text":"serialized"}]}' > "$out.json"
`, activePath, callsPath, gatePath, activePath))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr := NewTranscriber(c)
			first := startTranscription(tr, ctx, wav)
			waitForFile(t, callsPath, func(data []byte) bool { return string(data) == "call\n" })
			queuedCtx, cancelSecond := context.WithCancel(ctx)
			defer cancelSecond()
			second := startTranscription(tr, queuedCtx, wav)
			// Keep the first engine blocked throughout this interval. An adapter
			// without serialization either launches a second engine or fails the
			// mkdir exclusion guard and returns before it is allowed to run.
			select {
			case result := <-second:
				t.Fatalf("queued transcription completed while first was active: %v", result.err)
			case <-time.After(50 * time.Millisecond):
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil || string(calls) != "call\n" {
				t.Fatalf("second engine launched before first completed: %q, %v", calls, err)
			}
			if cancelQueued {
				cancelSecond()
				if result := awaitTranscription(t, second); !errors.Is(result.err, context.Canceled) {
					t.Fatalf("queued cancellation returned %v", result.err)
				}
				select {
				case result := <-first:
					t.Fatalf("queued cancellation affected active request: %v", result.err)
				default:
				}
			}
			releaseEngine(t, gate)
			if result := awaitTranscription(t, first); result.err != nil {
				t.Fatal(result.err)
			}
			if cancelQueued {
				// Cancellation must not consume/leak the admission slot or its
				// gate token; a new request must still be able to run normally.
				second = startTranscription(tr, ctx, wav)
			}
			waitForFile(t, callsPath, func(data []byte) bool { return string(data) == "call\ncall\n" })
			releaseEngine(t, gate)
			if result := awaitTranscription(t, second); result.err != nil || result.transcript.Text != "serialized" {
				t.Fatalf("next request got %#v, %v", result.transcript, result.err)
			}
		})
	}
}

func TestParentExitCleansDescendants(t *testing.T) {
	for _, test := range []struct {
		name         string
		retainOutput bool
		exitStatus   int
		json         string
	}{
		{"success closed outputs", false, 0, `{"transcription":[{"text":"finished"}]}`},
		{"success inherited outputs", true, 0, `{"transcription":[{"text":"finished"}]}`},
		{"nonzero inherited outputs", true, 7, `{"transcription":[{"text":"finished"}]}`},
		{"invalid JSON inherited outputs", true, 0, `not JSON`},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			redirect := " >/dev/null 2>&1 5>&-"
			if test.retainOutput {
				redirect = ""
			}
			c, wav := setup(t, fmt.Sprintf(`sleep 60%s &
echo $! > '%s'
printf '%%s' '%s' > "$out.json"
exit %d
`, redirect, pidPath, test.json, test.exitStatus))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := startTranscription(NewTranscriber(c), ctx, wav)
			pidData := waitForFile(t, pidPath, func(data []byte) bool {
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				return err == nil && pid > 0
			})
			pid, _ := strconv.Atoi(strings.TrimSpace(string(pidData)))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			result := awaitTranscription(t, done)
			// Both parent exit and JSON were successful. An inherited output
			// pipe must not convert that result into a transcription failure.
			switch {
			case test.exitStatus != 0:
				var exitErr *exec.ExitError
				if !errors.As(result.err, &exitErr) || exitErr.ExitCode() != test.exitStatus {
					t.Fatalf("expected original exit error, got %v", result.err)
				}
			case test.json == "not JSON":
				if result.err == nil || !strings.Contains(result.err.Error(), "decode whisper-cli JSON") {
					t.Fatalf("expected JSON error, got %v", result.err)
				}
			case result.err != nil || result.transcript.Text != "finished":
				t.Fatalf("successful engine parent got %#v, %v", result.transcript, result.err)
			}
			until := time.Now().Add(time.Second)
			for time.Now().Before(until) {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), ") Z ") {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("descendant remained alive after successful parent exit")
		})
	}
}
