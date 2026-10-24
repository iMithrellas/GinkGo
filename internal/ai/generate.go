package ai

import (
	"context"
	"fmt"
)

// generationError keeps provider diagnostics private while preserving the cause
// for callers that distinguish cancellation, limits and retryable failures.
type generationError struct{ cause error }

func (e *generationError) Error() string { return providerFallbackReason(e.cause) }
func (e *generationError) Unwrap() error { return e.cause }

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
	if err != nil {
		return Response{}, &generationError{cause: err}
	}
	if provider == nil {
		return Response{}, &generationError{cause: ErrProviderUnavailable}
	}
	response, err := provider.Complete(ctx, request)
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	if err != nil {
		return Response{}, &generationError{cause: err}
	}
	if len(response.Text) > 32<<10 {
		return Response{}, ErrOutputLimit
	}
	return response, nil
}
