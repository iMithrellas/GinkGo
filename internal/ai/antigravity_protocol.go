//go:build linux

package ai

import (
	"context"
	"encoding/json"
)

type antigravityEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Step           *struct {
		ConversationID string `json:"conversation_id"`
		Type           string `json:"step_type"`
		Text           string `json:"text_delta"`
		State          string `json:"state"`
		ToolInfo       *struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"tool_info"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string  `json:"conversation_id"`
		Status         string  `json:"status"`
		Response       *string `json:"response"`
		Error          string  `json:"error"`
	} `json:"result"`
}

func (p *AntigravityProvider) readTurn(ctx context.Context, s *antigravitySession, emit func(string) error) (Response, error) {
	used := 0
	emitted := false
	for {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case line, ok := <-s.lines:
			if !ok {
				// Join stderr collection before classifying an early native exit.
				select {
				case <-s.done:
				case <-ctx.Done():
					return Response{}, ctx.Err()
				}
				return Response{}, p.failure(ctx, s)
			}
			if line.err != nil {
				return Response{}, line.err
			}
			used += len(line.data) + 1
			if used > p.options.MaxOutputBytes {
				return Response{}, ErrOutputLimit
			}
			var event antigravityEvent
			if json.Unmarshal(line.data, &event) != nil || event.Event == "" {
				return Response{}, ErrProtocol
			}
			switch event.Event {
			case "init":
				if event.ConversationID == "" || (s.id != "" && s.id != event.ConversationID) {
					return Response{}, ErrProtocol
				}
				s.id = event.ConversationID
			case "step_update":
				if event.Step == nil || (event.Step.ConversationID != "" && event.Step.ConversationID != s.id) {
					return Response{}, ErrProtocol
				}
				if event.Step.Type == "tool" && event.Step.State != "ACTIVE" {
					// Confirm the scoped policy blocked the call. Never treat a
					// successful action (or an action failing after execution) as
					// generation-only success. This check cannot undo native effects.
					info := event.Step.ToolInfo
					if event.Step.State != "ERROR" || info == nil || info.Error == nil || info.Error.Message != "tool call denied by pre-tool hook: Ginkgo generation-only tool policy" {
						return Response{}, ErrIsolationUnavailable
					}
				}
				if event.Step.Type == "agent_response" && event.Step.Text != "" && emit != nil {
					if err := emit(event.Step.Text); err != nil {
						return Response{}, err
					}
					emitted = true
				}
			case "result":
				r := event.Result
				if r == nil {
					return Response{}, ErrProtocol
				}
				if r.Status != "SUCCESS" {
					return Response{}, antigravityFailure(r.Error)
				}
				if s.id == "" || r.ConversationID != s.id || r.Response == nil || r.Error != "" {
					return Response{}, ErrProtocol
				}
				if emit != nil && !emitted && *r.Response != "" {
					if err := emit(*r.Response); err != nil {
						return Response{}, err
					}
				}
				return Response{Text: *r.Response}, nil
			}
		}
	}
}
