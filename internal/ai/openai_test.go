package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("wrong request route/auth")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body) != 3 || string(body["stream"]) != "false" || string(body["model"]) != `"test-model"` {
			t.Errorf("unsafe request fields: %v", body)
		}
		var messages []map[string]string
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Error(err)
		}
		if len(messages) != 2 || messages[0]["role"] != "system" || messages[1]["content"] != "untrusted note" {
			t.Errorf("wrong messages: %v", messages)
		}
		fmt.Fprint(w, `{"id":"metadata","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":" untouched "}}]}`)
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIOptions{BaseURL: server.URL + "/v1/", Model: "test-model", APIKey: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.Complete(context.Background(), Request{System: "rules", Prompt: "untrusted note"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != " untouched " {
		t.Fatalf("text altered: %q", response.Text)
	}
}

func TestOpenAIFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"auth", "secret", 401, ErrAuthentication},
		{"rate limit", "secret", 429, nil},
		{"bad json", `data: {}`, 200, ErrProtocol},
		{"missing", `{}`, 200, ErrProtocol},
		{"truncated", `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`, 200, ErrProtocol},
		{"tools", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pretend text","tool_calls":[{}]}}]}`, 200, ErrProtocol},
		{"function", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"pretend text","function_call":{}}}]}`, 200, ErrProtocol},
		{"oversized", strings.Repeat("x", 4096), 200, ErrOutputLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			provider, _ := NewOpenAI(OpenAIOptions{BaseURL: server.URL, Model: "test", Options: Options{MaxOutputBytes: 512}})
			_, err := provider.Complete(context.Background(), Request{Prompt: "note"})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestOpenAICancellation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	cleanup := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-cleanup:
		}
		close(stopped)
	}))
	defer server.Close()
	defer close(cleanup)
	provider, _ := NewOpenAI(OpenAIOptions{BaseURL: server.URL, Model: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := provider.Complete(ctx, Request{}); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("HTTP request not cancelled")
	}
}

func TestOpenAIValidationAndInputLimit(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/model", "http://user:secret@localhost/v1", "http://localhost/v1?secret=1"} {
		if _, err := NewOpenAI(OpenAIOptions{BaseURL: endpoint, Model: "test"}); err == nil {
			t.Fatalf("unsafe URL accepted: %s", endpoint)
		}
	}
	provider, _ := NewOpenAI(OpenAIOptions{BaseURL: "http://localhost:1", Model: "test", Options: Options{MaxInputBytes: 16}})
	_, err := provider.Complete(context.Background(), Request{Prompt: strings.Repeat("x", 17)})
	if !errors.Is(err, ErrInputLimit) {
		t.Fatalf("unexpected error: %v", err)
	}
}
