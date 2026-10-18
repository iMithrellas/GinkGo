package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
	"github.com/mithrel/ginkgo/pkg/api"
)

func aiImage(t *testing.T) imagecontext.Image {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 4, 3))))
	im, err := imagecontext.Decode(b.Bytes(), time.Now(), "screen")
	require.NoError(t, err)
	return im
}

func TestOpenAIImageCapabilitiesAndTransport(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, override string
		want                     Support
	}{
		{"image metadata", `{"id":"test","input_modalities":["text","image"]}`, "", Supported},
		{"text only", `{"id":"test","capabilities":{"input":["text"]}}`, "", Unsupported},
		{"unknown", `{"id":"test"}`, "", Unknown},
		{"different model", `{"id":"other","input_modalities":["image"]}`, "", Unknown},
		{"declared image support", `{}`, "supported", Supported},
		{"explicitly disabled", `{"id":"test","input_modalities":["image"]}`, "unsupported", Unsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			im := aiImage(t)
			metadataCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					metadataCalls++
					require.Equal(t, "/models/test", r.URL.Path)
					fmt.Fprint(w, tc.metadata)
					return
				}
				var body struct {
					Messages []struct {
						Role    string
						Content json.RawMessage
					}
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				content := body.Messages[len(body.Messages)-1].Content
				if tc.want == Supported {
					var parts []struct {
						Type string
						Text string
						URL  struct{ URL string } `json:"image_url"`
					}
					require.NoError(t, json.Unmarshal(content, &parts))
					require.Len(t, parts, 2)
					require.Equal(t, "note", parts[0].Text)
					require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(im.Data), parts[1].URL.URL)
				} else {
					require.JSONEq(t, `"note"`, string(content))
				}
				fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)
			}))
			defer server.Close()
			p, err := NewOpenAI(OpenAIOptions{Options: Options{ImageInput: tc.override}, BaseURL: server.URL, Model: "test"})
			require.NoError(t, err)
			caps, err := p.Capabilities(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.want, caps.ImageInput)
			_, err = p.Complete(context.Background(), Request{Prompt: "note", Images: []imagecontext.Image{im}})
			require.NoError(t, err)
			if tc.override == "" {
				require.Equal(t, 1, metadataCalls)
			} else {
				require.Zero(t, metadataCalls)
			}
		})
	}
}

type imageProvider struct {
	support  Support
	complete func(Request) (Response, error)
}

func (p imageProvider) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{ImageInput: p.support}, nil
}
func (p imageProvider) Complete(_ context.Context, req Request) (Response, error) {
	return p.complete(req)
}

func TestImageNotesPreserveOriginalsAndSkipUnsupportedModels(t *testing.T) {
	for _, support := range []Support{Supported, Unsupported, Unknown} {
		t.Run(string(support), func(t *testing.T) {
			repo := noteTestRepo(t)
			store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
			im := aiImage(t)
			p := imageProvider{support: support, complete: func(req Request) (Response, error) {
				if support == Supported {
					require.Len(t, req.Images, 1)
					require.Contains(t, req.System, ImageContextSystemPrompt, "custom prompts must retain image-use policy")
				} else {
					require.Empty(t, req.Images)
				}
				return Response{Text: `{"action":"create_note","title":"Title","body":"Body","tags":["work"]}`}, nil
			}}
			s := NewService(repo, func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: store, SystemPrompt: "custom rules"})
			result, err := s.Note(context.Background(), NoteRequest{Text: "exact original", Namespace: "work", Images: []imagecontext.Image{im}}, "")
			require.NoError(t, err)
			require.Len(t, result.Entry.Images(), 1)
			original, err := store.Load(context.Background(), "work", result.Entry.Images()[0])
			require.NoError(t, err)
			require.Equal(t, im.Data, original.Data)
			require.Equal(t, "exact original", result.Entry.Provenance.Sources[0].OriginalText)
			if support != Supported {
				require.Contains(t, result.ImageStatus, "skipped")
			}
		})
	}
}

func TestImageOriginalsSurviveAIFallbackAndExpansion(t *testing.T) {
	ctx := context.Background()
	repo := noteTestRepo(t)
	store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
	im := aiImage(t)
	s := NewService(repo, func(string) (Provider, error) { return nil, errors.New("unavailable") }, ServiceOptions{Enabled: true, Images: store})
	first, err := s.Note(ctx, NoteRequest{Text: "exact original", Namespace: "work", Images: []imagecontext.Image{im}}, "")
	require.NoError(t, err)
	require.NotEmpty(t, first.FallbackReason)
	require.Len(t, first.Entry.Images(), 1)
	refs := first.Entry.Images()
	s.resolve = func(string) (Provider, error) {
		return imageProvider{support: Supported, complete: func(req Request) (Response, error) {
			require.Len(t, req.Images, 1, "only the current input's image is supplied")
			return Response{Text: fmt.Sprintf(`{"action":"expand_note","target_id":%q,"title":"Expanded","body":"More detail","tags":["work"]}`, first.Entry.ID)}, nil
		}}, nil
	}
	second, err := s.Note(ctx, NoteRequest{Text: "next original", Namespace: "work", Action: "expand", NoteID: first.Entry.ID, Images: []imagecontext.Image{im}}, "")
	require.NoError(t, err)
	require.Empty(t, second.FallbackReason)
	require.Len(t, second.Entry.Images(), 2)
	require.Equal(t, refs[0], second.Entry.Images()[0])
	_, err = store.Load(ctx, "work", second.Entry.Images()[1])
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.Note(canceled, NoteRequest{Text: "canceled", Namespace: "work", Images: []imagecontext.Image{im}}, "")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, noteCount(t, repo))
}

type brokenImageStore struct{}

func (brokenImageStore) Discard(context.Context, string, []api.ImageAttachment) error { return nil }
func (brokenImageStore) Save(context.Context, string, []imagecontext.Image) ([]api.ImageAttachment, error) {
	return nil, errors.New("private path")
}
func (brokenImageStore) Load(context.Context, string, api.ImageAttachment) (imagecontext.Image, error) {
	return imagecontext.Image{}, errors.New("unavailable")
}

func TestImageStorageFailurePreservesText(t *testing.T) {
	repo := noteTestRepo(t)
	s := NewService(repo, func(string) (Provider, error) { t.Fatal("inference must not lose image provenance"); return nil, nil }, ServiceOptions{Enabled: true, Images: brokenImageStore{}})
	result, err := s.Note(context.Background(), NoteRequest{Text: "exact input", Namespace: "work", Images: []imagecontext.Image{aiImage(t)}}, "")
	require.NoError(t, err)
	require.Equal(t, "exact input", result.Entry.Body)
	require.Empty(t, result.Entry.Images())
	require.Contains(t, result.FallbackReason, "saved text only")
	require.NotContains(t, result.FallbackReason, "private")
}

func TestCanceledImageInferenceDiscardsUncommittedOriginals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	repo := noteTestRepo(t)
	entered := make(chan struct{})
	p := imageProvider{support: Supported, complete: func(Request) (Response, error) { close(entered); <-ctx.Done(); return Response{}, ctx.Err() }}
	s := NewService(repo, func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(root))})
	done := make(chan error, 1)
	im := aiImage(t)
	go func() {
		_, err := s.Note(ctx, NoteRequest{Text: "this screen", Namespace: "work", Images: []imagecontext.Image{im}}, "")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("inference did not start")
	}
	files, err := filepath.Glob(filepath.Join(root, "*", "*"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	files, err = filepath.Glob(filepath.Join(root, "*", "*"))
	require.NoError(t, err)
	require.Empty(t, files)
	require.Zero(t, noteCount(t, repo))
}

func TestTextOnlyAdaptersCannotOverrideImageTransportCapability(t *testing.T) {
	stateless, err := NewOpenCode(OpenCodeOptions{Options: Options{ImageInput: "supported"}})
	require.NoError(t, err)
	session, err := NewOpenCodeSession(OpenCodeSessionOptions{Options: Options{ImageInput: "supported"}})
	require.NoError(t, err)
	for _, provider := range []Provider{stateless, session} {
		caps, err := ProviderCapabilities(context.Background(), provider)
		require.NoError(t, err)
		require.Equal(t, Unsupported, caps.ImageInput)
	}
}
