package ollama

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

// mockOllama returns an httptest.Server that mimics Ollama's native /api/chat endpoint.
func mockOllama(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Ollama should NOT require auth headers
		if r.Header.Get("Authorization") != "" {
			t.Error("ollama should not receive Authorization header")
		}

		var body ollamaRequest
		json.NewDecoder(r.Body).Decode(&body)

		resp := ollamaResponse{
			Model:     body.Model,
			CreatedAt: "2024-01-01T00:00:00Z",
			Message:   api.Message{Role: "assistant", Content: "mock ollama response"},
			Done:      true,
			DoneReason:      "stop",
			TotalDuration:   500000000,
			LoadDuration:    100000000,
			PromptEvalCount: 8,
			EvalCount:       4,
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestAdapter_ChatCompletion(t *testing.T) {
	mock := mockOllama(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
	}, noopLogger())

	req := &api.ProviderRequest{
		UpstreamModel: "llama3:8b",
		Request: api.ChatRequest{
			Model:    "llama3",
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}

	resp, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if resp.Model != "llama3:8b" {
		t.Errorf("model: got %q, want llama3:8b", resp.Model)
	}
	if resp.Choices[0].Message.Content != "mock ollama response" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 8 {
		t.Errorf("prompt_tokens: got %d, want 8", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 4 {
		t.Errorf("completion_tokens: got %d, want 4", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("total_tokens: got %d, want 12", resp.Usage.TotalTokens)
	}
}

func TestAdapter_Options(t *testing.T) {
	var receivedBody ollamaRequest

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		resp := ollamaResponse{
			Model:   "test",
			Message: api.Message{Role: "assistant", Content: "ok"},
			Done:    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{BaseURL: mock.URL}, noopLogger())

	temp := 0.7
	maxTok := 100
	req := &api.ProviderRequest{
		UpstreamModel: "test",
		Request: api.ChatRequest{
			Messages:    []api.Message{{Role: "user", Content: "hello"}},
			Temperature: &temp,
			MaxTokens:   &maxTok,
		},
	}

	_, err := adapter.ChatCompletion(t.Context(), req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if receivedBody.Stream != false {
		t.Error("stream should be false")
	}
	if receivedBody.Options == nil {
		t.Fatal("options should be set")
	}
	if *receivedBody.Options.Temperature != 0.7 {
		t.Errorf("temperature: got %v", *receivedBody.Options.Temperature)
	}
	if *receivedBody.Options.NumPredict != 100 {
		t.Errorf("num_predict: got %v", *receivedBody.Options.NumPredict)
	}
}

func TestAdapter_NATSSubscription(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	mock := mockOllama(t)
	defer mock.Close()

	adapter := NewAdapter(config.ProviderConfig{
		BaseURL: mock.URL,
	}, noopLogger())

	sub, err := adapter.Subscribe(nc)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Drain()

	provReq := api.ProviderRequest{
		UpstreamModel: "llama3:8b",
		Request: api.ChatRequest{
			Messages: []api.Message{{Role: "user", Content: "hello"}},
		},
	}
	data, _ := json.Marshal(provReq)

	msg, err := nc.Request("llm.provider.ollama.llama3-8b", data, 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var resp api.ChatResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Model != "llama3:8b" {
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

	msg, err := nc.Request("llm.provider.ollama.llama3-8b", []byte("{bad json"), 5e9)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var errResp api.ErrorResponse
	json.Unmarshal(msg.Data, &errResp)
	if errResp.Error.Code != "invalid_request" {
		t.Errorf("code: got %q", errResp.Error.Code)
	}
}

func TestAdapter_LongTimeout(t *testing.T) {
	adapter := NewAdapter(config.ProviderConfig{}, noopLogger())
	if adapter.client.Timeout.Seconds() != 120 {
		t.Errorf("timeout: got %v, want 120s", adapter.client.Timeout)
	}
}

