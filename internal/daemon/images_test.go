package daemon

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/ai"
	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/internal/ipc"
	"github.com/mithrel/ginkgo/internal/voiceevents"
	"github.com/mithrel/ginkgo/pkg/api"
)

type desktopCaptureFunc func(context.Context, imagecontext.Mode) ([]imagecontext.Image, error)

func (f desktopCaptureFunc) Modes() []imagecontext.Mode {
	return []imagecontext.Mode{imagecontext.Screen}
}
func (f desktopCaptureFunc) Capture(ctx context.Context, mode imagecontext.Mode) ([]imagecontext.Image, error) {
	return f(ctx, mode)
}

type desktopResolver struct{ capture imagecontext.Capturer }

func (r desktopResolver) Resolve(context.Context, string) (imagecontext.Capturer, error) {
	return r.capture, nil
}

type visualProvider struct {
	support  ai.Support
	complete listenAIProvider
}

func (p visualProvider) Capabilities(context.Context) (ai.Capabilities, error) {
	return ai.Capabilities{ImageInput: p.support}, nil
}
func (p visualProvider) Complete(ctx context.Context, r ai.Request) (ai.Response, error) {
	return p.complete(ctx, r)
}

func daemonImage(t *testing.T) imagecontext.Image {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	im, err := imagecontext.Decode(b.Bytes(), time.Now(), "wake snapshot")
	require.NoError(t, err)
	return im
}

func TestSherpaScreenshotCapabilityAndExplicitOverride(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		automatic, explicit    bool
		support                ai.Support
		captures, sent, stored int
	}{
		{"automatic supported", true, false, ai.Supported, 1, 1, 1},
		{"automatic text model", true, false, ai.Unsupported, 0, 0, 0},
		{"automatic unknown model", true, false, ai.Unknown, 0, 0, 0},
		{"automatic off", false, false, ai.Supported, 0, 0, 0},
		{"explicit while disabled", false, true, ai.Supported, 0, 1, 1},
		{"explicit replaces automatic", true, true, ai.Supported, 0, 1, 1},
		{"explicit preserved for text model", false, true, ai.Unsupported, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, log, _ := sherpaRuntime(t, "normal")
			app := sherpaAIApp(t, v)
			v.autoScreenshot, v.screenshotMode = tc.automatic, imagecontext.Screen
			v.pendingImages = imagecontext.NewPending()
			im := daemonImage(t)
			if tc.explicit {
				require.NoError(t, v.pendingImages.Stage("work", []imagecontext.Image{im}))
			}
			var captures atomic.Int32
			captured := make(chan struct{}, 1)
			v.screenshotResolver = desktopResolver{desktopCaptureFunc(func(_ context.Context, mode imagecontext.Mode) ([]imagecontext.Image, error) {
				require.Equal(t, imagecontext.Screen, mode)
				captures.Add(1)
				captured <- struct{}{}
				return []imagecontext.Image{im}, nil
			})}
			store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
			v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
				return visualProvider{tc.support, func(_ context.Context, req ai.Request) (ai.Response, error) {
					require.Len(t, req.Images, tc.sent)
					return ai.Response{Text: sherpaAIDraft}, nil
				}}, nil
			}, ai.ServiceOptions{Enabled: true, Images: store})
			r := v.stream(context.Background(), ipc.Message{Save: true, Mode: "ai", Namespace: "work"})
			require.True(t, r.OK, r.Msg)
			log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
			if tc.captures > 0 {
				awaitListener(t, captured)
			}
			require.True(t, v.stopListener(true).OK)
			saved := log.wait(t, func(e voiceevents.Event) bool { return e.Type == "note.saved" })
			require.EqualValues(t, tc.captures, captures.Load())
			entry, err := app.Store.Entries.GetEntry(context.Background(), saved.NoteID)
			require.NoError(t, err)
			require.Len(t, entry.Images(), tc.stored)
			require.Zero(t, v.pendingImages.Count("work"))
			if tc.stored > 0 {
				require.NotEmpty(t, saved.ImageStatus)
			}
		})
	}
}

func TestSherpaCancelJoinsScreenshotAndSavesNothing(t *testing.T) {
	v, log, _ := sherpaRuntime(t, "normal")
	app := sherpaAIApp(t, v)
	v.autoScreenshot = true
	v.screenshotMode = imagecontext.Screen
	v.ai = ai.NewService(app.Store.Entries, func(string) (ai.Provider, error) {
		return visualProvider{support: ai.Supported, complete: func(context.Context, ai.Request) (ai.Response, error) {
			t.Error("unexpected inference after cancel")
			return ai.Response{}, nil
		}}, nil
	}, ai.ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))})
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	v.screenshotResolver = desktopResolver{desktopCaptureFunc(func(ctx context.Context, _ imagecontext.Mode) ([]imagecontext.Image, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, ctx.Err()
	})}
	require.True(t, v.stream(context.Background(), ipc.Message{Save: true, Mode: "ai", Namespace: "work"}).OK)
	log.wait(t, func(e voiceevents.Event) bool { return e.RecordingNote })
	awaitListener(t, entered)
	ack := make(chan ipc.Response, 1)
	go func() { ack <- v.stopListener(false) }()
	awaitListener(t, canceled)
	select {
	case <-ack:
		t.Fatal("cancel returned before screenshot joined")
	default:
	}
	close(release)
	require.True(t, (<-ack).OK)
	entries, _, err := app.Store.Entries.ListEntries(context.Background(), api.ListQuery{})
	require.NoError(t, err)
	require.Empty(t, entries)
}
