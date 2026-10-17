//go:build sherpa_onnx && cgo

package sherpavoice

import (
	"errors"
	"testing"
)

func TestNativeRecordingFailsBeforeAnyNativeCall(t *testing.T) {
	// Works with null link shims too: no version getter or constructor may run.
	f, r, err := OpenNative(NativeConfig{RecordingEnabled: true})
	if !errors.Is(err, ErrUnsafeKWSClock) || f != nil || r != nil {
		t.Fatalf("native recording guard: %v %v %v", f, r, err)
	}
}
