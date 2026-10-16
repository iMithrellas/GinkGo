//go:build linux

package ai

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// This opt-in probe reuses saved authentication but never requests a real model
// or opens a note repository. Do not print credential responses or server logs.
func TestOpenCodeCurrentProfileNoInference(t *testing.T) {
	executable := os.Getenv("GINKGO_OPENCODE_PROFILE_PROBE")
	if executable == "" {
		t.Skip("set GINKGO_OPENCODE_PROFILE_PROBE for saved-profile unknown-model probe")
	}
	provider, err := NewOpenCode(OpenCodeOptions{Executable: executable, Model: "ginkgo-invalid-provider/ginkgo-invalid-model", TempDir: t.TempDir(), Options: Options{Timeout: 45 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Complete(context.Background(), Request{Prompt: "Synthetic unknown-model diagnostic. Never infer."})
	if errors.Is(err, ErrOpenCodeConsole) {
		t.Skip("saved Console profile refused before generation; isolation remains enforced")
	}
	if !errors.Is(err, ErrModelUnavailable) {
		t.Fatal(providerFallbackReason(err))
	}
	t.Log("saved-profile private generation route rejected the unknown model before inference")
}
