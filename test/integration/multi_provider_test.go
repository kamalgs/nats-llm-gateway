package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/config"
	anthropicAdapter "github.com/kamalgs/infermesh/internal/provider/anthropic"
	ollamaAdapter "github.com/kamalgs/infermesh/internal/provider/ollama"
	openaiAdapter "github.com/kamalgs/infermesh/internal/provider/openai"
	"github.com/kamalgs/infermesh/internal/testutil"
)

// mockOpenAIServer returns a mock that serves OpenAI-compatible responses.
func mockOpenAIServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)

		resp := api.ChatResponse{
			ID:      "chatcmpl-multi",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: "openai: " + model},
				FinishReason: "stop",
			}},
			Usage: &api.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// mockAnthropicServer returns a mock that serves Anthropic Messages API responses.
func mockAnthropicServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)

		resp := map[string]any{
			"id":    "msg-multi",
			"type":  "message",
			"model": model,
			"role":  "assistant",
			"content": []map[string]any{{
				"type": "text",
				"text": "anthropic: " + model,
			}},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":  7,
				"output_tokens": 4,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// mockOllamaServer returns a mock that serves Ollama-compatible responses.
func mockOllamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)

		resp := api.ChatResponse{
			ID:      "ollama-multi",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: "ollama: " + model},
				FinishReason: "stop",
			}},
			Usage: &api.Usage{PromptTokens: 6, CompletionTokens: 3, TotalTokens: 9},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestMultiProvider_Routing(t *testing.T) {
	openaiMock := mockOpenAIServer(t)
	defer openaiMock.Close()
	anthropicMock := mockAnthropicServer(t)
	defer anthropicMock.Close()
	ollamaMock := mockOllamaServer(t)
	defer ollamaMock.Close()

	_, nc := testutil.StartNATS(t)
	log := silentLogger()

	// Start all three provider adapters
	oa := openaiAdapter.NewAdapter(config.ProviderConfig{BaseURL: openaiMock.URL, APIKey: "test-key"}, log)
	oaSub, err := oa.Subscribe(nc)
	if err != nil {
		t.Fatalf("subscribe openai: %v", err)
	}
	defer oaSub.Drain()

	aa := anthropicAdapter.NewAdapter(config.ProviderConfig{BaseURL: anthropicMock.URL, APIKey: "test-key"}, log)
	aaSub, err := aa.Subscribe(nc)
	if err != nil {
		t.Fatalf("subscribe anthropic: %v", err)
	}
	defer aaSub.Drain()

	ol := ollamaAdapter.NewAdapter(config.ProviderConfig{BaseURL: ollamaMock.URL}, log)
	olSub, err := ol.Subscribe(nc)
	if err != nil {
		t.Fatalf("subscribe ollama: %v", err)
	}
	defer olSub.Drain()

	tests := []struct {
		upstreamModel string
		provider      string
		subject       string
	}{
		{"gpt-4o", "openai", "llm.provider.openai.gpt-4o"},
		{"gpt-4o-mini", "openai", "llm.provider.openai.gpt-4o-mini"},
		{"claude-sonnet-4-20250514", "anthropic", "llm.provider.anthropic.claude-sonnet-4-20250514"},
		{"claude-haiku-4-5-20251001", "anthropic", "llm.provider.anthropic.claude-haiku-4-5-20251001"},
		{"llama3:8b", "ollama", "llm.provider.ollama.llama3:8b"},
		{"mistral:7b", "ollama", "llm.provider.ollama.mistral:7b"},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"."+tt.upstreamModel, func(t *testing.T) {
			// Client sends ProviderRequest directly to the provider subject
			// (this is what the proxy does after parsing the model name)
			req := api.ProviderRequest{
				UpstreamModel: tt.upstreamModel,
				Request: api.ChatRequest{
					Model:    tt.provider + "." + tt.upstreamModel,
					Messages: []api.Message{{Role: "user", Content: "test"}},
				},
			}
			data, _ := json.Marshal(req)

			msg, err := nc.Request(tt.subject, data, 5*time.Second)
			if err != nil {
				t.Fatalf("request: %v", err)
			}

			var resp api.ChatResponse
			if err := json.Unmarshal(msg.Data, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if resp.Model != tt.upstreamModel {
				t.Errorf("model: got %q, want %q", resp.Model, tt.upstreamModel)
			}

			wantContent := tt.provider + ": " + tt.upstreamModel
			if resp.Choices[0].Message.Content != wantContent {
				t.Errorf("content: got %q, want %q", resp.Choices[0].Message.Content, wantContent)
			}
		})
	}
}

func TestMultiProvider_UnifiedResponseFormat(t *testing.T) {
	openaiMock := mockOpenAIServer(t)
	defer openaiMock.Close()
	anthropicMock := mockAnthropicServer(t)
	defer anthropicMock.Close()
	ollamaMock := mockOllamaServer(t)
	defer ollamaMock.Close()

	_, nc := testutil.StartNATS(t)
	log := silentLogger()

	oa := openaiAdapter.NewAdapter(config.ProviderConfig{BaseURL: openaiMock.URL, APIKey: "test-key"}, log)
	oaSub, _ := oa.Subscribe(nc)
	defer oaSub.Drain()

	aa := anthropicAdapter.NewAdapter(config.ProviderConfig{BaseURL: anthropicMock.URL, APIKey: "test-key"}, log)
	aaSub, _ := aa.Subscribe(nc)
	defer aaSub.Drain()

	ol := ollamaAdapter.NewAdapter(config.ProviderConfig{BaseURL: ollamaMock.URL}, log)
	olSub, _ := ol.Subscribe(nc)
	defer olSub.Drain()

	// All responses should have the same unified format regardless of provider
	subjects := []struct {
		subject string
		model   string
	}{
		{"llm.provider.openai.gpt-4o", "gpt-4o"},
		{"llm.provider.anthropic.claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},
		{"llm.provider.ollama.llama3:8b", "llama3:8b"},
	}

	for _, s := range subjects {
		t.Run(s.subject, func(t *testing.T) {
			req := api.ProviderRequest{
				UpstreamModel: s.model,
				Request: api.ChatRequest{
					Messages: []api.Message{{Role: "user", Content: "test"}},
				},
			}
			data, _ := json.Marshal(req)

			msg, err := nc.Request(s.subject, data, 5*time.Second)
			if err != nil {
				t.Fatalf("request: %v", err)
			}

			var resp api.ChatResponse
			if err := json.Unmarshal(msg.Data, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if resp.Object != "chat.completion" {
				t.Errorf("object = %q, want chat.completion", resp.Object)
			}
			if len(resp.Choices) != 1 {
				t.Errorf("choices count = %d, want 1", len(resp.Choices))
			}
			if resp.Choices[0].Message == nil {
				t.Errorf("message is nil")
			}
			if resp.Choices[0].FinishReason != "stop" {
				t.Errorf("finish_reason = %q, want stop", resp.Choices[0].FinishReason)
			}
			if resp.Usage == nil {
				t.Errorf("usage is nil")
			}
		})
	}
}
