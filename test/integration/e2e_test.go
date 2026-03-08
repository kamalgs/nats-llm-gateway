// Package integration tests the full request path:
// HTTP proxy → NATS → Provider Adapter → mock upstream
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/config"
	openaiAdapter "github.com/kamalgs/infermesh/internal/provider/openai"
	"github.com/kamalgs/infermesh/internal/proxy"
	"github.com/kamalgs/infermesh/internal/testutil"
	"github.com/nats-io/nats.go"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockLLM creates a mock LLM HTTP server that returns canned responses.
func mockLLM(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		messages, _ := body["messages"].([]any)

		content := "mock response"
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				if c, ok := m["content"].(string); ok {
					content = "echo: " + c
				}
			}
		}

		resp := api.ChatResponse{
			ID:      "chatcmpl-integration",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: content},
				FinishReason: "stop",
			}},
			Usage: &api.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

// startStack starts NATS, provider adapter, and HTTP proxy.
// Returns the proxy HTTP URL.
func startStack(t *testing.T) (proxyURL string) {
	t.Helper()

	mock := mockLLM(t)
	t.Cleanup(mock.Close)

	_, nc := testutil.StartNATS(t)
	natsURL := nc.ConnectedUrl()

	cfg := config.ProviderConfig{BaseURL: mock.URL, APIKey: "test-key"}
	log := silentLogger()

	// Start provider adapter
	adapter := openaiAdapter.NewAdapter(cfg, log)
	sub, err := adapter.Subscribe(nc)
	if err != nil {
		t.Fatalf("subscribe adapter: %v", err)
	}
	t.Cleanup(func() { sub.Drain() })

	// Start proxy on a random port
	nc2, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("connect proxy: %v", err)
	}
	t.Cleanup(func() { nc2.Close() })

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	proxyURL = fmt.Sprintf("http://%s", listener.Addr().String())
	p := proxy.New(nc2, listener.Addr().String(), log)
	listener.Close()
	go p.Start()
	time.Sleep(100 * time.Millisecond) // wait for bind

	return proxyURL
}

func TestE2E_HTTPProxyChatCompletion(t *testing.T) {
	proxyURL := startStack(t)

	// Test health
	resp, err := http.Get(proxyURL + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status: %d", resp.StatusCode)
	}

	// Test chat completion — model uses provider.model convention
	chatReq := api.ChatRequest{
		Model:    "openai.gpt-4o",
		Messages: []api.Message{{Role: "user", Content: "integration test"}},
	}
	body, _ := json.Marshal(chatReq)

	resp, err = http.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("chat status: %d, body: %s", resp.StatusCode, respBody)
	}

	var chatResp api.ChatResponse
	json.NewDecoder(resp.Body).Decode(&chatResp)

	if chatResp.Model != "gpt-4o" {
		t.Errorf("model: got %q, want gpt-4o", chatResp.Model)
	}
	if chatResp.Choices[0].Message.Content != "echo: integration test" {
		t.Errorf("content: got %q", chatResp.Choices[0].Message.Content)
	}
	if chatResp.Usage.TotalTokens != 15 {
		t.Errorf("total_tokens: got %d", chatResp.Usage.TotalTokens)
	}
}

func TestE2E_InvalidModelFormat(t *testing.T) {
	proxyURL := startStack(t)

	chatReq := api.ChatRequest{
		Model:    "nonexistent-model",
		Messages: []api.Message{{Role: "user", Content: "hello"}},
	}
	body, _ := json.Marshal(chatReq)

	resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}

	var errResp api.ErrorResponse
	json.NewDecoder(resp.Body).Decode(&errResp)
	if errResp.Error.Code != "invalid_request" {
		t.Errorf("code: got %q", errResp.Error.Code)
	}
}

func TestE2E_NATSDirectProviderRequest(t *testing.T) {
	mock := mockLLM(t)
	defer mock.Close()

	_, nc := testutil.StartNATS(t)

	cfg := config.ProviderConfig{BaseURL: mock.URL, APIKey: "test-key"}
	log := silentLogger()

	adapter := openaiAdapter.NewAdapter(cfg, log)
	sub, _ := adapter.Subscribe(nc)
	defer sub.Drain()

	// Send directly to provider subject (what SDK would do)
	req := api.ProviderRequest{
		UpstreamModel: "gpt-4o",
		Request: api.ChatRequest{
			Model:    "openai.gpt-4o",
			Messages: []api.Message{{Role: "user", Content: "direct nats"}},
		},
	}
	data, _ := json.Marshal(req)

	msg, err := nc.Request("llm.provider.openai", data, 5*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var resp api.ChatResponse
	json.Unmarshal(msg.Data, &resp)

	if resp.Choices[0].Message.Content != "echo: direct nats" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
}
