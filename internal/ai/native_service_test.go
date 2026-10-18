package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mithrel/ginkgo/internal/blobstore"
	"github.com/mithrel/ginkgo/internal/imagecontext"
)

type constrainedProvider struct {
	caps     Capabilities
	request  Request
	response Response
	err      error
	capErr   error
}

func (p *constrainedProvider) Capabilities(context.Context) (Capabilities, error) {
	return p.caps, p.capErr
}
func (p *constrainedProvider) Complete(_ context.Context, r Request) (Response, error) {
	p.request = r
	return p.response, p.err
}

func TestServiceNativeSchemaLimitsAndSubmissionStatus(t *testing.T) {
	for _, tc := range []struct {
		name          string
		caps          Capabilities
		fail          bool
		discoveryFail bool
		submitted     *bool
		status        string
		wantImages    int
	}{
		{name: "supported", caps: Capabilities{ImageInput: Supported}, status: "included", wantImages: 1},
		{name: "unknown model", caps: Capabilities{ImageInput: Unknown}, status: "support is unknown"},
		{name: "unsupported model", caps: Capabilities{ImageInput: Unsupported}, status: "disabled or unsupported"},
		{name: "metadata unavailable", caps: Capabilities{ImageInput: Supported}, discoveryFail: true, status: "capability discovery failed"},
		{name: "size limit", caps: Capabilities{ImageInput: Supported, MaxImageBytes: 1}, status: "skipped"},
		{name: "format limit", caps: Capabilities{ImageInput: Supported, ImageMediaTypes: []string{"image/jpeg"}}, status: "skipped"},
		{name: "aggregate limit", caps: Capabilities{ImageInput: Supported, MaxTotalImageBytes: 1}, status: "skipped"},
		{name: "width limit", caps: Capabilities{ImageInput: Supported, MaxImageWidth: 1}, status: "skipped"},
		{name: "height limit", caps: Capabilities{ImageInput: Supported, MaxImageHeight: 1}, status: "skipped"},
		{name: "encoded request limit", caps: Capabilities{ImageInput: Supported, MaxRequestBytes: 64 << 10}, status: "skipped"},
		{name: "preflight rejection", caps: Capabilities{ImageInput: Supported}, fail: true, submitted: boolPointer(false), status: "not sent", wantImages: 1},
		{name: "unknown failure", caps: Capabilities{ImageInput: Supported}, fail: true, status: "unconfirmed", wantImages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &constrainedProvider{caps: tc.caps, response: Response{Text: `{"action":"create_note","title":"T","body":"B","tags":["work"]}`, ImagesSubmitted: tc.submitted}}
			if tc.fail {
				p.err = errors.New("secret provider output")
			}
			if tc.discoveryFail {
				p.capErr = errors.New("secret discovery output")
			}
			s := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(t.TempDir())), SystemPrompt: "custom"})
			r, err := s.Note(context.Background(), NoteRequest{Text: "exact original", Images: []imagecontext.Image{aiImage(t)}}, "")
			require.NoError(t, err)
			require.Contains(t, r.ImageStatus, tc.status)
			require.NotContains(t, r.FallbackReason, "secret")
			require.NotContains(t, r.ImageStatus, "secret")
			require.Len(t, r.Entry.Images(), 1)
			require.Len(t, p.request.Images, tc.wantImages)
			require.True(t, json.Valid(p.request.OutputSchema))
			require.Contains(t, p.request.System, ImageContextSystemPrompt)
			if tc.fail {
				require.Equal(t, "exact original", r.Entry.Body)
				require.NotEmpty(t, r.FallbackReason)
			} else {
				require.Empty(t, r.FallbackReason)
			}
		})
	}
	images := []imagecontext.Image{aiImage(t), aiImage(t)}
	require.False(t, imagesFitCapabilities(images, Capabilities{MaxImages: 1}))
}

func TestServiceLegacyStrictCLIWireShape(t *testing.T) {
	p, err := NewCLI(CLIOptions{Options: Options{ImageInput: "supported"}, Executable: fixture(t, "cli-strict-historical-note")})
	require.NoError(t, err)
	store := imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))
	s := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true, Images: store})
	r, err := s.Note(context.Background(), NoteRequest{Text: "original", Images: []imagecontext.Image{aiImage(t)}}, "")
	require.NoError(t, err)
	require.Empty(t, r.FallbackReason)
	require.Equal(t, "Generated body", r.Entry.Body)
	require.Len(t, r.Entry.Images(), 1)
}

func TestImageRequestBudgetAccountsForActualJSONEscaping(t *testing.T) {
	r := Request{System: strings.Repeat("\x00", 1000), Prompt: strings.Repeat("\"", 1000), Images: []imagecontext.Image{aiImage(t)}, OutputSchema: json.RawMessage(NoteProposalSchema)}
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	limit := len(encoded) + (64 << 10)
	require.True(t, imageRequestFitsCapabilities(r, Capabilities{MaxRequestBytes: limit}))
	require.False(t, imageRequestFitsCapabilities(r, Capabilities{MaxRequestBytes: limit - 1}))
	require.Greater(t, len(encoded), len(r.System)+len(r.Prompt)+len(r.Images[0].Data))
}

func TestIsolationFallbackIsDistinctFromLegacyFreeTier(t *testing.T) {
	reason := providerFallbackReason(errors.Join(ErrGenerationRestricted, ErrIsolationUnavailable, errors.New("secret native output")))
	require.Contains(t, reason, "provider/version support matrix")
	require.NotContains(t, reason, "free-tier")
	require.NotContains(t, reason, "secret")
	require.Contains(t, providerFallbackReason(ErrGenerationRestricted), "free-tier")
	p := &constrainedProvider{err: errors.Join(ErrIsolationUnavailable, ErrGenerationRestricted, errors.New("secret native output"))}
	s := NewService(noteTestRepo(t), func(string) (Provider, error) { return p, nil }, ServiceOptions{Enabled: true})
	r, err := s.Note(context.Background(), NoteRequest{Text: "original text", Tags: []string{"work"}}, "")
	require.NoError(t, err)
	require.Equal(t, "original text", r.Entry.Body)
	require.Equal(t, reason, r.FallbackReason)
}

type exactPreflightProvider struct {
	constrainedProvider
	preflightRequest Request
	calls            int
}

func (p *exactPreflightProvider) PreflightImages(_ context.Context, r Request) error {
	p.calls++
	p.preflightRequest = r
	return errors.New("secret exact transport rejection")
}

func TestServiceWholeBatchRetentionAndExactPreflight(t *testing.T) {
	for _, exact := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggregate", true: "exact framing"}[exact], func(t *testing.T) {
			im := aiImage(t)
			caps := Capabilities{ImageInput: Supported, MaxTotalImageBytes: len(im.Data)}
			base := constrainedProvider{caps: caps, response: Response{Text: `{"action":"create_note","title":"T","body":"Text generation survived","tags":["work"]}`}}
			var provider Provider
			var exactProvider *exactPreflightProvider
			if exact {
				base.caps.MaxTotalImageBytes = 0
				exactProvider = &exactPreflightProvider{constrainedProvider: base}
				provider = exactProvider
			} else {
				provider = &base
			}
			registry := NewRegistry(time.Minute)
			defer registry.Close()
			managed, err := registry.Resolve("batch", func() (Provider, error) { return provider, nil })
			require.NoError(t, err)
			s := NewService(noteTestRepo(t), func(string) (Provider, error) { return managed, nil }, ServiceOptions{Enabled: true, Images: imagecontext.NewStore(blobstore.NewFileStore(t.TempDir()))})
			r, err := s.Note(context.Background(), NoteRequest{Text: "original", Images: []imagecontext.Image{im, im}}, "")
			require.NoError(t, err)
			require.Empty(t, r.FallbackReason)
			require.Contains(t, r.ImageStatus, "skipped")
			require.Len(t, r.Entry.Images(), 2)
			if exact {
				require.Contains(t, r.ImageStatus, "provider image preflight failed")
				require.NotContains(t, r.ImageStatus, "secret")
				require.Equal(t, 1, exactProvider.calls)
				require.Len(t, exactProvider.preflightRequest.Images, 2)
				require.NotEmpty(t, exactProvider.preflightRequest.OutputSchema)
				require.Contains(t, exactProvider.preflightRequest.Prompt, `"image_count":2`)
				base = exactProvider.constrainedProvider
			}
			require.Empty(t, base.request.Images)
			require.NotContains(t, base.request.Prompt, "image_count")
			require.Equal(t, "Text generation survived", r.Entry.Body)
		})
	}
}
func boolPointer(v bool) *bool { return &v }

func TestNoteProposalSchemaStrictObjectCompatibility(t *testing.T) {
	var schema struct {
		Type                 string                     `json:"type"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(NoteProposalSchema), &schema))
	require.Equal(t, "object", schema.Type)
	require.False(t, schema.AdditionalProperties)
	require.Len(t, schema.Required, len(schema.Properties))
	for name := range schema.Properties {
		require.Contains(t, schema.Required, name, "strict native schemas require every property")
	}
	provider := &constrainedProvider{response: Response{Text: `{"action":"create_note","target_id":null,"title":"T","body":"B","tags":["journal"]}`}}
	service := NewService(noteTestRepo(t), func(string) (Provider, error) { return provider, nil }, ServiceOptions{Enabled: true})
	result, err := service.Note(context.Background(), NoteRequest{Text: "original", Action: "create"}, "")
	require.NoError(t, err)
	require.Empty(t, result.FallbackReason)
	require.Equal(t, "B", result.Entry.Body)
}

func TestImagePolicyRemainsLastWithCustomSystemPrompt(t *testing.T) {
	provider := &constrainedProvider{
		caps:     Capabilities{ImageInput: Supported},
		response: Response{Text: `{"action":"create_note","title":"T","body":"B","tags":["journal"]}`},
	}
	service := NewService(noteTestRepo(t), func(string) (Provider, error) { return provider, nil }, ServiceOptions{
		Enabled:      true,
		SystemPrompt: ImageContextSystemPrompt + "\nAdditional custom instructions.",
		Images:       imagecontext.NewStore(blobstore.NewFileStore(t.TempDir())),
	})
	result, err := service.Note(context.Background(), NoteRequest{Text: "A nonvisual thought", Images: []imagecontext.Image{aiImage(t)}}, "")
	require.NoError(t, err)
	require.Empty(t, result.FallbackReason)
	require.True(t, strings.HasSuffix(provider.request.System, ImageContextSystemPrompt))
	require.Len(t, provider.request.Images, 1, "the relevance policy controls use, not upload")
}

func TestOpenAIDiscoveryTransientFailureRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"id":"model","input_modalities":["text","image"]}`))
	}))
	defer server.Close()
	p, err := NewOpenAI(OpenAIOptions{BaseURL: server.URL, Model: "model"})
	require.NoError(t, err)
	_, err = p.Capabilities(context.Background())
	require.Error(t, err)
	caps, err := p.Capabilities(context.Background())
	require.NoError(t, err)
	require.Equal(t, Supported, caps.ImageInput)
	require.Equal(t, "model", caps.ResolvedModel)
	_, err = p.Capabilities(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}
