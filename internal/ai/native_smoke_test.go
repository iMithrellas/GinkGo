//go:build linux

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/imagecontext"
)

// TestNativeProviderLiveImageSmoke is deliberately opt-in. It uses the selected
// native login, sends a synthetic image to a real model, and may incur a charge.
// Ordinary tests never enable it. It is distinct from metadata/version smokes
// and from fake-executable tests, which do not verify provider authentication.
func TestNativeProviderLiveImageSmoke(t *testing.T) {
	if os.Getenv("GINKGO_AI_LIVE_SMOKE") != "1" {
		t.Skip("explicitly set GINKGO_AI_LIVE_SMOKE=1 to send a harmless fixture to a real provider (may incur charges)")
	}
	executable := os.Getenv("GINKGO_AI_SMOKE_EXECUTABLE")
	if executable == "" {
		t.Fatal("set GINKGO_AI_SMOKE_EXECUTABLE explicitly; no default executable is selected by live tests")
	}
	model, variant := os.Getenv("GINKGO_AI_SMOKE_MODEL"), os.Getenv("GINKGO_AI_SMOKE_VARIANT")
	options := Options{Timeout: 2 * time.Minute, ImageInput: os.Getenv("GINKGO_AI_SMOKE_IMAGE_INPUT")}
	var provider Provider
	var err error
	switch os.Getenv("GINKGO_AI_SMOKE_PROVIDER") {
	case "opencode-native":
		provider, err = NewOpenCodeNative(OpenCodeNativeOptions{Options: options, Executable: executable, Model: model, Variant: variant, TempDir: t.TempDir()})
	case "codex":
		provider, err = NewCodex(CodexOptions{Options: options, Executable: executable, Model: model, Variant: variant, TempDir: t.TempDir(), AuthHome: os.Getenv("GINKGO_AI_SMOKE_AUTH_HOME")})
	case "claude-code":
		provider, err = NewClaudeCode(ClaudeCodeOptions{Options: options, Executable: executable, Model: model, Variant: variant, TempDir: t.TempDir()})
	default:
		t.Fatal("set GINKGO_AI_SMOKE_PROVIDER to opencode-native, codex, or claude-code")
	}
	if err != nil {
		t.Fatalf("construct native provider: %v", err)
	}
	if closer, ok := provider.(ProviderCloser); ok {
		t.Cleanup(func() {
			if err := closer.Close(); err != nil {
				t.Errorf("native runtime cleanup: %v", err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	caps, err := ProviderCapabilities(ctx, provider)
	if err != nil {
		t.Fatalf("native metadata/authentication preflight: %v", err)
	}
	if caps.ImageInput != Supported {
		t.Fatal("selected model lacks verified image capability; use a vision model and, only if its documentation confirms support, GINKGO_AI_SMOKE_IMAGE_INPUT=supported")
	}
	im := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var fixture bytes.Buffer
	if err := png.Encode(&fixture, im); err != nil {
		t.Fatal(err)
	}
	attachment, err := imagecontext.Decode(fixture.Bytes(), time.Unix(0, 0), "synthetic fixture")
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Complete(ctx, Request{
		System:       "Return only the requested JSON object, without markdown.\n" + ImageContextSystemPrompt,
		Prompt:       "Inspect the supplied image. What single solid basic color fills it? Return {\"color\":\"name\"}, with one lowercase English color word and no modifiers.",
		Images:       []imagecontext.Image{attachment},
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"color":{"type":"string"}},"required":["color"]}`),
	})
	if err != nil {
		t.Fatalf("authenticated native image generation failed: %v", err)
	}
	if response.ImagesSubmitted == nil || !*response.ImagesSubmitted {
		t.Fatal("native provider did not confirm image submission")
	}
	var answer struct {
		Color string `json:"color"`
	}
	if err := decodeObject([]byte(response.Text), &answer); err != nil || answer.Color != "red" {
		t.Fatal("native model did not return the expected valid color object for the synthetic image")
	}
	t.Log("authenticated native multimodal generation succeeded using a synthetic image; this is not a private-note test")
}
