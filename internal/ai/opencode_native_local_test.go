//go:build linux

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
)

func TestOpenCodeNativeImageByteLimit(t *testing.T) {
	im := aiImage(t)
	im.Data = append(im.Data, make([]byte, openCodeNativeMaxImageBytes-len(im.Data))...)
	prepared, changed, err := prepareNativeImages(context.Background(), []imagecontext.Image{im})
	if err != nil || changed || !bytes.Equal(prepared[0].Data, im.Data) {
		t.Fatal("safe boundary was modified:", err)
	}
	im.Data = append(im.Data, 0)
	prepared, changed, err = prepareNativeImages(context.Background(), []imagecontext.Image{im})
	if err != nil || !changed || len(prepared[0].Data) > openCodeNativeMaxImageBytes {
		t.Fatal("oversized image was not prepared:", err)
	}
	p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: "/nonexistent-must-not-run"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	request := Request{Prompt: "synthetic", Images: []imagecontext.Image{{Data: []byte("invalid")}}}
	if err := p.PreflightImages(context.Background(), request); !errors.Is(err, ErrImagePreparation) {
		t.Fatalf("invalid preflight: %v", err)
	}
	response, err := p.Complete(context.Background(), request)
	if !errors.Is(err, ErrImagePreparation) || response.ImagesSubmitted == nil || *response.ImagesSubmitted || p.runtime != nil {
		t.Fatalf("invalid direct request was not rejected before native startup: %v", err)
	}
}

// Reproduce the upstream Prompt.Base64 predicate in the exact executable's Bun
// runtime. Only synthetic buffers and booleans are produced; OpenCode is never
// started, and no profile/model/credential route is accessed.
func TestOpenCodeNativeRealBase64RuntimeLimit(t *testing.T) {
	executable, dir := openCodeSmokeProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `const grouped=/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/u;
const linear=/^[A-Za-z0-9+/]*={0,2}$/u;
const safe=Buffer.alloc(4*1024*1024,1).toString("base64");
const large=Buffer.alloc(5323630,1).toString("base64");
console.log(JSON.stringify({safe:grouped.test(safe),large:grouped.test(large),valid:linear.test(large),roundtrip:Buffer.from(large,"base64").length===5323630}));`
	cmd := command(ctx, executable, []string{"-e", script}, dir, []string{"BUN_BE_BUN=1", "HOME=" + dir, "PATH=/usr/bin:/bin"}, nil)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal("synthetic native runtime predicate:", err)
	}
	var result struct{ Safe, Large, Valid, Roundtrip bool }
	if json.Unmarshal(output, &result) != nil || !result.Safe || result.Large || !result.Valid || !result.Roundtrip {
		t.Fatal("audited native Base64 runtime behavior changed; reassess the per-image workaround")
	}
	t.Log("audited grouped Base64 predicate rejects valid 5,323,630-byte data; conservative 4 MiB boundary succeeds")
}

// Opt-in real runner regression: all credentials/profile data are synthetic and
// disposable, and the only model endpoint is an in-process loopback SSE fixture.
// Unlike resume:false tests, this exercises actual Complete and strict note JSON.
func TestOpenCodeNativeRealLocalComplete(t *testing.T) {
	for _, tc := range []struct {
		name      string
		noiseRows int
		changed   bool
	}{
		{"screenshot below native limit", 350, false},
		{"large original retained with reduced-size vision", 460, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable, dir := openCodeSmokeProfile(t)
			const answer = `{"action":"create_note","title":"Synthetic fixture","body":"Synthetic fixture processed.","tags":["fixture"]}`
			var calls, imageCalls atomic.Int32
			var expectedURIs atomic.Value
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
				if err != nil {
					t.Error("read synthetic model request failed")
					w.WriteHeader(500)
					return
				}
				calls.Add(1)
				if bytes.Contains(body, []byte("data:image/")) {
					imageCalls.Add(1)
				}
				var request struct {
					Tools    []json.RawMessage                   `json:"tools"`
					Messages []struct{ Content json.RawMessage } `json:"messages"`
				}
				if json.Unmarshal(body, &request) != nil || len(request.Tools) != 0 {
					t.Error("invalid synthetic request or tools enabled")
				}
				var uris []string
				for _, message := range request.Messages {
					var parts []struct {
						Type string `json:"type"`
						URL  struct {
							URL string `json:"url"`
						} `json:"image_url"`
					}
					if json.Unmarshal(message.Content, &parts) != nil {
						continue
					}
					for _, part := range parts {
						if part.Type == "image_url" {
							uris = append(uris, part.URL.URL)
						}
					}
				}
				if len(uris) != 0 {
					expected, _ := expectedURIs.Load().([]string)
					if len(uris) != len(expected) {
						t.Error("image batch lost items")
					} else {
						for i := range uris {
							if uris[i] != expected[i] {
								t.Error("model image copy/order differs from deterministic preparation")
							}
						}
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, delta := range []map[string]any{{"role": "assistant", "content": answer}, {}} {
					finish := any(nil)
					if len(delta) == 0 {
						finish = "stop"
					}
					chunk, _ := json.Marshal(map[string]any{"id": "chatcmpl-synthetic", "object": "chat.completion.chunk", "created": 1, "model": "synthetic-vision", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\n", chunk)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer endpoint.Close()
			wrapper := nativeLocalWrapper(t, executable, dir, endpoint.URL)
			im := nativeLocalScreenshot(t, tc.noiseRows)
			t.Logf("synthetic PNG bytes=%d dimensions=3840x2160", len(im.Data))
			if (len(im.Data) > openCodeNativeMaxImageBytes) != tc.changed {
				t.Fatal("synthetic screenshot no longer straddles the native limit")
			}
			p, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: wrapper, TempDir: dir, Model: "synthetic/synthetic-vision", Variant: "low", EphemeralAuth: true, Options: Options{Timeout: 90 * time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			caps, err := p.Capabilities(ctx)
			if err != nil || caps.ImageInput != Supported || caps.MaxImageBytes != imagecontext.MaxImageBytes {
				t.Fatalf("synthetic metadata: caps=%+v err=%v", caps, err)
			}
			originalHash := sha256.Sum256(im.Data)
			batch := []imagecontext.Image{aiImage(t), im, nativeImage(t, color.RGBA{R: 255, A: 255})}
			prepared, changed, err := prepareNativeImages(ctx, batch)
			if err != nil || changed != tc.changed || len(prepared[1].Data) > openCodeNativeMaxImageBytes {
				t.Fatalf("synthetic preparation failed: %v", err)
			}
			if prepared[1].Width != 3840 || prepared[1].Height != 2160 {
				t.Fatal("opaque screenshot was unnecessarily downscaled before high-quality full-resolution encoding")
			}
			var expected []string
			for _, image := range prepared {
				expected = append(expected, "data:"+image.MediaType+";base64,"+base64.StdEncoding.EncodeToString(image.Data))
			}
			expectedURIs.Store(expected)
			if err := p.PreflightImages(ctx, Request{Prompt: "Synthetic fixture", Images: batch}); err != nil || calls.Load() != 0 {
				t.Fatalf("preflight failed or invoked model: %v", err)
			}
			store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
			service := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{
				Enabled: true, Images: store,
			})
			result, err := service.Note(ctx, NoteRequest{Text: "Synthetic screen fixture", Action: "create", Images: batch}, "synthetic")
			if err != nil || result.FallbackReason != "" || result.Entry.Body != "Synthetic fixture processed." || len(result.Entry.Tags) != 1 || len(result.Entry.Images()) != len(batch) {
				t.Fatalf("synthetic note failed: err=%v fallback=%q status=%q", err, result.FallbackReason, result.ImageStatus)
			}
			if calls.Load() == 0 || imageCalls.Load() == 0 {
				t.Fatalf("unexpected loopback submission: calls=%d image_calls=%d", calls.Load(), imageCalls.Load())
			}
			if !tc.changed && result.ImageStatus != "retained; included in AI request" || tc.changed && result.ImageStatus != "retained originals; included reduced-size copies in AI request" {
				t.Fatalf("unexpected image status: %q", result.ImageStatus)
			}
			loaded, err := store.Load(ctx, result.Entry.Namespace, result.Entry.Images()[1])
			if err != nil || sha256.Sum256(loaded.Data) != originalHash || sha256.Sum256(im.Data) != originalHash {
				t.Fatalf("original store/export input changed: %v", err)
			}
		})
	}
}

func nativeLocalWrapper(t *testing.T, executable, dir, endpoint string) string {
	t.Helper()
	config, err := json.Marshal(map[string]any{
		"plugins": []string{"-*", "opencode.config.agent", "opencode.config.image", "opencode.config.provider", "opencode.models.dev", "opencode.provider.*"},
		"providers": map[string]any{"synthetic": map[string]any{
			"package":  "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": endpoint + "/v1", "apiKey": "synthetic-not-a-provider-key"},
			"models": map[string]any{"synthetic-vision": map[string]any{
				"capabilities": map[string]any{"input": []string{"text", "image"}, "output": []string{"text"}},
				"limit":        map[string]int{"context": 32768, "output": 1024},
				"variants":     []map[string]any{{"id": "low"}},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Admit only disposable profile/runtime variables. No inherited API keys,
	// provider settings, or user authentication can reach the native process.
	wrapper := filepath.Join(dir, "native-local")
	script := "#!/usr/bin/python3\nimport os,sys\nallow={'HOME','XDG_CONFIG_HOME','XDG_DATA_HOME','XDG_CACHE_HOME','XDG_STATE_HOME','TMPDIR','OPENCODE_DB','OPENCODE_CONFIG_DIR','OPENCODE_CONFIG_PROJECT_DISABLE','OPENCODE_DISABLE_CLAUDE_CODE','OPENCODE_FILEWATCHER_DISABLE','OPENCODE_PASSWORD'}\nenv={k:v for k,v in os.environ.items() if k in allow}\nenv['PATH']='/usr/bin:/bin'\nenv['OPENCODE_CONFIG_CONTENT']=" + strconv.Quote(string(config)) + "\nos.execve(" + strconv.Quote(executable) + ",[" + strconv.Quote(executable) + "]+sys.argv[1:],env)\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

// A noisy upper region and flat lower region create a reproducible RGBA PNG
// around the live screenshot size without any screen capture or user content.
func nativeLocalScreenshot(t *testing.T, noiseRows int) imagecontext.Image {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	x := uint32(12345)
	for y := 0; y < 2160; y++ {
		for col := 0; col < 3840; col++ {
			i := y*im.Stride + col*4
			if y < noiseRows {
				for c := 0; c < 3; c++ {
					x ^= x << 13
					x ^= x >> 17
					x ^= x << 5
					im.Pix[i+c] = byte(x)
				}
			}
			im.Pix[i+3] = 255
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, im); err != nil {
		t.Fatal(err)
	}
	return imagecontext.Image{Data: encoded.Bytes(), Label: "synthetic display"}
}
