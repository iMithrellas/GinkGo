//go:build linux

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/stretchr/testify/require"
)

func nativePaddedImage(t *testing.T) imagecontext.Image {
	t.Helper()
	im := aiImage(t)
	im.Data = append(im.Data, make([]byte, openCodeNativeMaxImageBytes+1-len(im.Data))...)
	return im
}

func TestNativePreparationDeterministicWholeBatch(t *testing.T) {
	ctx := context.Background()
	batch := []imagecontext.Image{aiImage(t), nativePaddedImage(t), nativeImage(t, color.RGBA{B: 255, A: 255})}
	hashes := make([][32]byte, len(batch))
	for i := range batch {
		hashes[i] = sha256.Sum256(batch[i].Data)
	}
	first, transformed, err := prepareNativeImages(ctx, batch)
	require.NoError(t, err)
	require.True(t, transformed)
	second, transformed, err := prepareNativeImages(ctx, batch)
	require.NoError(t, err)
	require.True(t, transformed)
	require.Len(t, first, len(batch))
	for i := range first {
		require.Equal(t, hashes[i], sha256.Sum256(batch[i].Data), "input %d mutated", i)
		require.Equal(t, first[i].Data, second[i].Data)
		require.Equal(t, batch[i].Label, first[i].Label)
		require.LessOrEqual(t, len(first[i].Data), openCodeNativeMaxImageBytes)
		encoded := base64.StdEncoding.EncodeToString(first[i].Data)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		require.Equal(t, first[i].Data, decoded)
		if i != 1 {
			require.Equal(t, batch[i].Data, first[i].Data)
		}
	}
	request := Request{Prompt: "Synthetic", Images: batch}
	request.Images = first
	framed1, err := nativePrompt(request, first, "msg_synthetic", 1<<20)
	require.NoError(t, err)
	framed2, err := nativePrompt(request, second, "msg_synthetic", 1<<20)
	require.NoError(t, err)
	require.Equal(t, framed1, framed2)
}

func TestNativePreparedPreflightSendsNoContent(t *testing.T) {
	p, capture := nativeFixture(t, "ok")
	request := Request{System: "synthetic-private-system-marker", Prompt: "synthetic-private-prompt-marker", OutputSchema: json.RawMessage(NoteProposalSchema), Images: []imagecontext.Image{nativePaddedImage(t)}}
	hash := sha256.Sum256(request.Images[0].Data)
	require.NoError(t, p.PreflightImages(context.Background(), request))
	for _, item := range nativeCaptures(t, capture) {
		require.NotEqual(t, "/api/session", item.Path)
		require.False(t, strings.HasSuffix(item.Path, "/prompt"))
		require.NotContains(t, string(item.Body), "private-system-marker")
		require.NotContains(t, string(item.Body), "private-prompt-marker")
		require.NotContains(t, string(item.Body), "data:image/")
	}
	require.Equal(t, hash, sha256.Sum256(request.Images[0].Data))
	// Exact text/schema framing must also fail locally, without admitting a
	// partial image batch or initializing a provider when over its byte budget.
	bounded, err := NewOpenCodeNative(OpenCodeNativeOptions{Executable: "/nonexistent-must-not-run", Options: Options{MaxInputBytes: 1}})
	require.NoError(t, err)
	defer bounded.Close()
	require.ErrorIs(t, bounded.PreflightImages(context.Background(), request), ErrInputLimit)
	require.Nil(t, bounded.runtime)
}

func TestNativeReductionFormatsAlphaAndAspect(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		alpha, jpeg   bool
	}{
		{"opaque PNG", 256, 128, false, false},
		{"transparent PNG", 256, 128, true, false},
		{"JPEG", 256, 128, false, true},
		{"narrow tall", 1, 4096, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, tc.width, tc.height))
			x := uint32(12345)
			for i := range img.Pix {
				x ^= x << 13
				x ^= x >> 17
				x ^= x << 5
				img.Pix[i] = byte(x)
			}
			if !tc.alpha {
				for i := 3; i < len(img.Pix); i += 4 {
					img.Pix[i] = 255
				}
			}
			var buf bytes.Buffer
			if tc.jpeg {
				require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}))
			} else {
				require.NoError(t, png.Encode(&buf, img))
			}
			original, err := imagecontext.Decode(buf.Bytes(), time.Unix(123, 0), "synthetic")
			require.NoError(t, err)
			copy, err := reduceNativeImage(context.Background(), original, 4096)
			require.NoError(t, err)
			require.LessOrEqual(t, len(copy.Data), 4096)
			require.Equal(t, original.CapturedAt, copy.CapturedAt)
			require.Equal(t, original.Label, copy.Label)
			require.LessOrEqual(t, copy.Width, original.Width)
			require.LessOrEqual(t, copy.Height, original.Height)
			// Aspect rounding is bounded by one pixel on the shorter axis,
			// including the unavoidable 1px clamp for very narrow/tall inputs.
			if original.Width >= original.Height {
				require.InDelta(t, float64(copy.Width)*float64(original.Height)/float64(original.Width), float64(copy.Height), 1)
			} else {
				require.InDelta(t, float64(copy.Height)*float64(original.Width)/float64(original.Height), float64(copy.Width), 1)
			}
			decoded, format, err := image.Decode(bytes.NewReader(copy.Data))
			require.NoError(t, err)
			require.Equal(t, "image/"+format, copy.MediaType)
			if tc.alpha {
				require.Equal(t, "image/png", copy.MediaType)
				_, _, _, alpha := decoded.At(copy.Width/2, copy.Height/2).RGBA()
				require.Less(t, alpha, uint32(65535))
			}
		})
	}
}

func TestNativeReductionPrefersFullResolutionAndAcceptsOversizedJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 128))
	x := uint32(12345)
	for i := range img.Pix {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		img.Pix[i] = byte(x)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var pngBytes, jpegBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, img))
	require.NoError(t, jpeg.Encode(&jpegBytes, img, &jpeg.Options{Quality: 100}))
	for _, data := range [][]byte{pngBytes.Bytes(), jpegBytes.Bytes()} {
		original, err := imagecontext.Decode(data, time.Unix(123, 0), "synthetic")
		require.NoError(t, err)
		original.Data = append(original.Data, make([]byte, openCodeNativeMaxImageBytes+1-len(original.Data))...)
		copies, changed, err := prepareNativeImages(context.Background(), []imagecontext.Image{original})
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, 256, copies[0].Width)
		require.Equal(t, 128, copies[0].Height)
		require.Equal(t, "image/png", copies[0].MediaType, "lossless recompression precedes JPEG")
	}
	original, err := imagecontext.Decode(pngBytes.Bytes(), time.Unix(123, 0), "synthetic")
	require.NoError(t, err)
	copy, err := reduceNativeImage(context.Background(), original, 64<<10)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", copy.MediaType)
	require.Equal(t, 256, copy.Width)
	require.Equal(t, 128, copy.Height)
	var expected bytes.Buffer
	require.NoError(t, jpeg.Encode(&expected, img, &jpeg.Options{Quality: 95}))
	require.Equal(t, expected.Bytes(), copy.Data, "prefer full-resolution quality 95 before downscaling")
}

func TestNativePreparationFailureAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, changed, err := prepareNativeImages(ctx, []imagecontext.Image{nativePaddedImage(t)})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	require.False(t, changed)
	w := &nativeImageWriter{ctx: context.Background(), limit: 3}
	_, err = w.Write([]byte("abc"))
	require.NoError(t, err)
	_, err = w.Write([]byte("d"))
	require.ErrorIs(t, err, ErrInputLimit)
	require.Len(t, w.Bytes(), 3)
	w.ctx = ctx
	_, err = w.Write(nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = resizeNativeImage(ctx, image.NewRGBA(image.Rect(0, 0, 8, 8)), 4, 4)
	require.ErrorIs(t, err, context.Canceled)
	_, err = reduceNativeImage(context.Background(), aiImage(t), 1)
	require.ErrorIs(t, err, ErrInputLimit)
	large := aiImage(t)
	large.Data = append(large.Data, make([]byte, imagecontext.MaxImageBytes+1-len(large.Data))...)
	half := aiImage(t)
	half.Data = append(half.Data, make([]byte, (imagecontext.MaxTotalBytes/2)+1-len(half.Data))...)
	for _, batch := range [][]imagecontext.Image{
		{aiImage(t), {Data: []byte("invalid")}},
		{{Data: aiImage(t).Data[:24]}},
		{large},
		{half, half},
		make([]imagecontext.Image, imagecontext.MaxImages+1),
	} {
		prepared, transformed, err := prepareNativeImages(context.Background(), batch)
		require.ErrorIs(t, err, ErrImagePreparation)
		require.Nil(t, prepared, "never return a partially prepared batch")
		require.False(t, transformed)
	}
}

func TestNativePreparationOriginalBoundariesAndPixelGate(t *testing.T) {
	pad := func(size int) imagecontext.Image {
		im := aiImage(t)
		im.Data = append(im.Data, make([]byte, size-len(im.Data))...)
		return im
	}
	for _, batch := range [][]imagecontext.Image{
		{pad(imagecontext.MaxImageBytes)},
		{pad(imagecontext.MaxTotalBytes / 2), pad(imagecontext.MaxTotalBytes / 2)},
		{aiImage(t), aiImage(t), aiImage(t), aiImage(t), aiImage(t), aiImage(t), aiImage(t), aiImage(t)},
	} {
		prepared, _, err := prepareNativeImages(context.Background(), batch)
		require.NoError(t, err)
		require.Len(t, prepared, len(batch))
	}
	// A valid IHDR advertising >32 MP must be rejected before pixel allocation
	// or attempting to decode its deliberately tiny original IDAT payload.
	im := aiImage(t)
	binary.BigEndian.PutUint32(im.Data[16:20], uint32(imagecontext.MaxPixels+1))
	binary.BigEndian.PutUint32(im.Data[20:24], 1)
	binary.BigEndian.PutUint32(im.Data[29:33], crc32.ChecksumIEEE(im.Data[12:29]))
	_, _, err := prepareNativeImages(context.Background(), []imagecontext.Image{im})
	require.ErrorIs(t, err, ErrInputLimit)
}

func TestNativeTransformedAttributionSurvivesFailures(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		submitted *bool
		reason    string
	}{
		{"ok", boolPointer(true), ""},
		{"prompt-500", nil, "prompt admission failed (HTTP 500)"},
		{"prompt-error", boolPointer(false), "rate limited"},
		{"admission-mismatch", nil, "incompatible response"},
		{"reset-error", boolPointer(true), "AI provider failed"},
		{"delete-error", boolPointer(true), "AI provider failed"},
		{"console", boolPointer(false), "Console"},
		{"missing-model", boolPointer(false), "model unavailable"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p, capture := nativeFixture(t, tc.mode)
			if tc.mode == "missing-model" {
				p.options.Model = "test/missing-model"
			}
			request := Request{System: "Synthetic system", Prompt: "Synthetic request", Images: []imagecontext.Image{nativePaddedImage(t)}}
			response, err := p.Complete(context.Background(), request)
			require.True(t, response.ImagesTransformed)
			if tc.submitted == nil {
				require.Nil(t, response.ImagesSubmitted)
			} else {
				require.NotNil(t, response.ImagesSubmitted)
				require.Equal(t, *tc.submitted, *response.ImagesSubmitted)
			}
			if tc.reason == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				reason := providerFallbackReason(err)
				require.Contains(t, reason, tc.reason)
				require.NotContains(t, reason, "secret")
				require.NotContains(t, reason, "untrusted")
			}
			if tc.mode == "console" || tc.mode == "missing-model" {
				return
			}
			prompts := 0
			for _, item := range nativeCaptures(t, capture) {
				if strings.HasSuffix(item.Path, "/prompt") {
					prompts++
					var payload struct {
						Files []struct {
							URI string `json:"uri"`
						} `json:"files"`
					}
					require.NoError(t, json.Unmarshal(item.Body, &payload))
					require.Len(t, payload.Files, 1)
					_, raw, _ := strings.Cut(payload.Files[0].URI, ",")
					data, err := base64.StdEncoding.DecodeString(raw)
					require.NoError(t, err)
					require.LessOrEqual(t, len(data), openCodeNativeMaxImageBytes)
				}
			}
			require.Equal(t, 1, prompts, "ambiguous admissions must never replay")
		})
	}
}

type preparationFailureProvider struct{ imageProvider }

func (p preparationFailureProvider) PreflightImages(context.Context, Request) error {
	return errors.Join(ErrImagePreparation, ErrInputLimit, errors.New("secret preparation detail"))
}

func TestServiceImagePreparationFailureSkipsWholeBatch(t *testing.T) {
	calls := 0
	p := preparationFailureProvider{imageProvider{support: Supported, complete: func(request Request) (Response, error) {
		calls++
		require.Empty(t, request.Images)
		require.NotContains(t, request.Prompt, "image_count")
		return Response{Text: `{"action":"create_note","title":"T","body":"Text survived","tags":["fixture"]}`}, nil
	}}}
	service := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))})
	result, err := service.Note(context.Background(), NoteRequest{Text: "Synthetic", Images: []imagecontext.Image{aiImage(t), aiImage(t)}}, "")
	require.NoError(t, err)
	require.Empty(t, result.FallbackReason)
	require.Equal(t, "Text survived", result.Entry.Body)
	require.Len(t, result.Entry.Images(), 2)
	require.Equal(t, "retained; skipped because reduced-size AI images could not be prepared within adapter limits", result.ImageStatus)
	require.Equal(t, 1, calls)
}

func TestServiceTransformedImageStatuses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		submitted *bool
		fail      bool
		status    string
	}{
		{"included", boolPointer(true), false, "retained originals; included reduced-size copies in AI request"},
		{"cleanup failed", boolPointer(true), true, "retained originals; included reduced-size copies in AI request"},
		{"unconfirmed", nil, true, "retained originals; reduced-size image submission unconfirmed"},
		{"not sent", boolPointer(false), true, "retained; not sent to AI"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := imageProvider{support: Supported, complete: func(Request) (Response, error) {
				response := Response{Text: `{"action":"create_note","title":"T","body":"B","tags":["fixture"]}`, ImagesSubmitted: tc.submitted, ImagesTransformed: true}
				if tc.fail {
					return response, errors.New("secret provider error")
				}
				return response, nil
			}}
			service := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))})
			result, err := service.Note(context.Background(), NoteRequest{Text: "Synthetic", Images: []imagecontext.Image{aiImage(t)}}, "")
			require.NoError(t, err)
			require.Equal(t, tc.status, result.ImageStatus)
		})
	}
}
