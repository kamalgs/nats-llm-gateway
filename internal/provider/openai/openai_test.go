package openai

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
	"github.com/nats-io/nats.go"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockOpenAI returns an httptest.Server that mimics OpenAI's chat completion endpoint.
func mockOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "invalid api key", "type": "auth_error"},
			})
			return
		}

		// Parse request to echo back the model
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)

		resp := api.ChatResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 1700000000,
			Model:   model,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: "mock response"},
				FinishReason: "stop",
			}},
			Usage: &api.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestAdapter_ChatCompletion(t *testing.T) {
	mock := mockOpenAI(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
		APIKey:  "test-key",
	}, noopLogger())

	req := &api.ProviderRequest{
		UpstreamModel: "gpt-4o-test",
		Request: api.ChatRequest{
			Model:    "gpt-4o",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}

	resp, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if resp.Model != "gpt-4o-test" {
		t.Errorf("model: got %q, want gpt-4o-test", resp.Model)
	}
	if resp.Choices[0].Message.Content != "mock response" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("total_tokens: got %d", resp.Usage.TotalTokens)
	}
}

func TestAdapter_BadAPIKey(t *testing.T) {
	mock := mockOpenAI(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
		APIKey:  "wrong-key",
	}, noopLogger())

	req := &api.ProviderRequest{
		UpstreamModel: "gpt-4o",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}

	_, err := adapter.ChatCompletion(t.Context(), req)
	if err == nil {
		t.Fatal("expected error for bad API key")
	}
}

func TestAdapter_NATSSubscription(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	mock := mockOpenAI(t)
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

	// Send a provider request via NATS
	provReq := api.ProviderRequest{
		UpstreamModel: "gpt-4o-test",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(provReq)

	msg, err := nc.Request("llm.provider.openai.gpt-4o-test", data, 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var resp api.ChatResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Model != "gpt-4o-test" {
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

	msg, err := nc.Request("llm.provider.openai.gpt-4o-test", []byte("{bad json"), 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var errResp api.ErrorResponse
	json.Unmarshal(msg.Data, &errResp)
	if errResp.Error.Code != "invalid_request" {
		t.Errorf("code: got %q", errResp.Error.Code)
	}
}

func TestAdapter_MultipleSubscribers(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	mock := mockOpenAI(t)
	defer mock.Close()

	cfg := config.ProviderConfig{BaseURL: mock.URL, APIKey: "test-key"}

	// Start two adapter instances on the same queue group
	a1 := NewAdapter(cfg, noopLogger())
	a2 := NewAdapter(cfg, noopLogger())
	s1, _ := a1.Subscribe(nc)
	defer s1.Drain()

	nc2, _ := nats.Connect(nc.ConnectedUrl())
	defer nc2.Close()
	s2, _ := a2.Subscribe(nc2)
	defer s2.Drain()

	// Send multiple requests — they should be distributed
	provReq := api.ProviderRequest{
		UpstreamModel: "gpt-4o",
		Request:       api.ChatRequest{Messages: []api.Message{{Role: "user", Content: "hi"}}},
	}
	data, _ := json.Marshal(provReq)

	for i := 0; i < 10; i++ {
		msg, err := nc.Request("llm.provider.openai.gpt-4o-test", data, 5e9)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		var resp api.ChatResponse
		json.Unmarshal(msg.Data, &resp)
		if resp.ID != "chatcmpl-test" {
			t.Errorf("request %d: unexpected response", i)
		}
	}
}
