package ai

import (
	"context"
	"fmt"
)

// Generate performs bounded, generation-only inference without saving a note.
// It shares provider ownership and admission with note generation.
func (s *Service) Generate(ctx context.Context, request Request, providerName string) (Response, error) {
	if !s.options.Enabled || s.resolve == nil {
		return Response{}, fmt.Errorf("AI is disabled or unavailable")
	}
	if len(request.Prompt) > s.options.MaxInputBytes+s.options.MaxContextBytes || len(request.Images) != 0 {
		return Response{}, ErrInputLimit
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	provider, err := s.resolve(providerName)
	if err != nil || provider == nil {
		return Response{}, fmt.Errorf("%s", providerFallbackReason(err))
	}
	response, err := provider.Complete(ctx, request)
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if err != nil {
		return Response{}, fmt.Errorf("%s", providerFallbackReason(err))
	}
	if len(response.Text) > 32<<10 {
		return Response{}, ErrOutputLimit
	}
	return response, nil
}
