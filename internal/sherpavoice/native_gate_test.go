//go:build sherpa_onnx && cgo

package sherpavoice

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeIncompleteRecordingConfigFailsBeforeAnyNativeCall(t *testing.T) {
	// Works with null link shims too: no version getter or constructor may run.
	f, r, err := OpenNative(NativeConfig{RecordingEnabled: true})
	if err == nil || f != nil || r != nil {
		t.Fatalf("native recording guard: %v %v %v", f, r, err)
	}
}

func TestUnknownNativeProfileCannotEnableRecording(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "wrong-model")
	keywords := filepath.Join(dir, "keywords")
	if err := os.WriteFile(model, []byte("not the audited model"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keywords, []byte("HH EY1 G IH1 NG K OW1 @NOTE_ALIAS\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, recording := range []bool{false, true} {
		k, err := openNativeKWS(NativeConfig{RecordingEnabled: recording, KWS: ModelFiles{model, model, model, model}, KeywordFile: keywords})
		if k != nil || !errors.Is(err, ErrUnverifiedNativeProfile) {
			t.Fatal("unknown profile accepted")
		}
	}
}
