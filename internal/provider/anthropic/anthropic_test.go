package anthropic

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/config"
	"github.com/kamalgs/infermesh/internal/testutil"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockAnthropic returns an httptest.Server that mimics the Anthropic Messages API.
func mockAnthropic(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify Anthropic-specific headers
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "invalid api key", "type": "authentication_error"},
			})
			return
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "missing anthropic-version header"},
			})
			return
		}

		var body anthropicRequest
		json.NewDecoder(r.Body).Decode(&body)

		resp := anthropicResponse{
			ID:    "msg-test-123",
			Type:  "message",
			Model: body.Model,
			Role:  "assistant",
			Content: []anthropicContent{{
				Type: "text",
				Text: "mock anthropic response",
			}},
			StopReason: "end_turn",
			Usage: anthropicUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestAdapter_ChatCompletion(t *testing.T) {
	mock := mockAnthropic(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
		APIKey:  "test-key",
	}, noopLogger())

	temp := 0.7
	req := &api.ProviderRequest{
		UpstreamModel: "claude-sonnet-4-20250514",
		Request: api.ChatRequest{
			Model: "claude-sonnet",
			Messages: []api.Message{
				{Role: "system", Content: "You are helpful."},
				{Role: "user", Content: "hello"},
			},
			Temperature: &temp,
		},
	}

	resp, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model: got %q, want claude-sonnet-4-20250514", resp.Model)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("object: got %q, want chat.completion", resp.Object)
	}
	if resp.Choices[0].Message.Content != "mock anthropic response" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("prompt_tokens: got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("completion_tokens: got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens: got %d", resp.Usage.TotalTokens)
	}
}

func TestAdapter_BadAPIKey(t *testing.T) {
	mock := mockAnthropic(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
		APIKey:  "wrong-key",
	}, noopLogger())

	req := &api.ProviderRequest{
		UpstreamModel: "claude-sonnet-4-20250514",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}

	_, err := adapter.ChatCompletion(t.Context(), req)
	if err == nil {
		t.Fatal("expected error for bad API key")
	}
}

func TestAdapter_DefaultMaxTokens(t *testing.T) {
	// Verify that max_tokens defaults to 4096 when not set
	var capturedBody anthropicRequest
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		resp := anthropicResponse{
			ID:      "msg-test",
			Type:    "message",
			Model:   capturedBody.Model,
			Role:    "assistant",
			Content: []anthropicContent{{Type: "text", Text: "ok"}},
			Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 1},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{BaseURL: mock.URL}, noopLogger())
	req := &api.ProviderRequest{
		UpstreamModel: "claude-haiku",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hi"}},
		},
	}

	_, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if capturedBody.MaxTokens != 4096 {
		t.Errorf("max_tokens: got %d, want 4096", capturedBody.MaxTokens)
	}
}

func TestAdapter_SystemMessageExtraction(t *testing.T) {
	var capturedBody anthropicRequest
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		resp := anthropicResponse{
			ID:      "msg-test",
			Type:    "message",
			Model:   capturedBody.Model,
			Role:    "assistant",
			Content: []anthropicContent{{Type: "text", Text: "ok"}},
			Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 1},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{BaseURL: mock.URL}, noopLogger())
	req := &api.ProviderRequest{
		UpstreamModel: "claude-sonnet",
		Request: api.ChatRequest{
			Messages: []api.Message{
				{Role: "system", Content: "Be concise."},
				{Role: "user", Content: "hello"},
			},
		},
	}

	_, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if capturedBody.System != "Be concise." {
		t.Errorf("system: got %q, want %q", capturedBody.System, "Be concise.")
	}
	// System message should be extracted, only user message remains
	if len(capturedBody.Messages) != 1 {
		t.Errorf("messages count: got %d, want 1", len(capturedBody.Messages))
	}
	if capturedBody.Messages[0].Role != "user" {
		t.Errorf("first message role: got %q, want user", capturedBody.Messages[0].Role)
	}
}

func TestAdapter_NATSSubscription(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	mock := mockAnthropic(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
		APIKey:  "test-key",
	}, noopLogger())

	sub, err := adapter.Subscribe(nc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Drain()

	provReq := api.ProviderRequest{
		UpstreamModel: "claude-sonnet-4-20250514",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(provReq)

	msg, err := nc.Request("llm.provider.anthropic.claude-sonnet-4-20250514", data, 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var resp api.ChatResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model: got %q", resp.Model)
	}
}

func TestAdapter_NATSInvalidPayload(t *testing.T) {
	_, nc := testutil.StartNATS(t)

	adapter := NewAdapter(config.ProviderConfig{}, noopLogger())
	sub, err := adapter.Subscribe(nc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Drain()

	msg, err := nc.Request("llm.provider.anthropic.claude-sonnet-4-20250514", []byte("{bad json"), 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var errResp api.ErrorResponse
	json.Unmarshal(msg.Data, &errResp)
	if errResp.Error.Code != "invalid_request" {
		t.Errorf("code: got %q", errResp.Error.Code)
	}
}
