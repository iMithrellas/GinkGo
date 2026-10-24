package ai

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratePreservesPrivateProviderErrors(t *testing.T) {
	for _, cause := range []error{ErrAuthentication, ErrRateLimited, ErrProviderUnavailable, ErrInputLimit, ErrOutputLimit, ErrProtocol, context.Canceled, context.DeadlineExceeded, errors.New("private upstream failure")} {
		for _, stage := range []string{"resolve", "complete"} {
			t.Run(stage+"/"+cause.Error(), func(t *testing.T) {
				upstream := fmt.Errorf("secret prompt and credentials: %w", cause)
				service := NewService(nil, func(string) (Provider, error) {
					if stage == "resolve" {
						return nil, upstream
					}
					return noteProviderFunc(func(context.Context, Request) (Response, error) {
						return Response{Text: "partial output"}, upstream
					}), nil
				}, ServiceOptions{Enabled: true})
				response, err := service.Generate(context.Background(), Request{Prompt: "note"}, "test")
				require.ErrorIs(t, err, cause)
				require.EqualError(t, err, providerFallbackReason(upstream))
				require.NotContains(t, err.Error(), "secret")
				require.Empty(t, response.Text)
			})
		}
	}
}

func TestGenerateMissingProvider(t *testing.T) {
	service := NewService(nil, func(string) (Provider, error) { return nil, nil }, ServiceOptions{Enabled: true})
	_, err := service.Generate(context.Background(), Request{}, "test")
	require.ErrorIs(t, err, ErrProviderUnavailable)
}
