package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kamalgs/infermesh/api"
	"github.com/kamalgs/infermesh/internal/testutil"
	"github.com/nats-io/nats.go"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setupProxy(t *testing.T, nc *nats.Conn) *Proxy {
	t.Helper()
	return New(nc, ":0", noopLogger())
}

func TestProxy_HealthCheck(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	p := setupProxy(t, nc)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	p.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var body map[string]string
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("body: got %v", body)
	}
}

func TestProxy_ChatCompletion(t *testing.T) {
	_, nc := testutil.StartNATS(t)

	// Mock provider subscriber — wildcard matches all openai models
	nc2, _ := nats.Connect(nc.ConnectedUrl())
	t.Cleanup(func() { nc2.Close() })

	nc2.Subscribe("llm.provider.openai.>", func(msg *nats.Msg) {
		var provReq api.ProviderRequest
		json.Unmarshal(msg.Data, &provReq)

		resp := api.ChatResponse{
			ID:     "test-123",
			Object: "chat.completion",
			Model:  provReq.UpstreamModel,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: "proxy works"},
				FinishReason: "stop",
			}},
		}
		data, _ := json.Marshal(resp)
		msg.Respond(data)
	})
	nc2.Flush()

	p := setupProxy(t, nc)
	chatReq := api.ChatRequest{
		Model:    "openai.gpt-4o",
		Messages: []api.Message{{Role: "user", Content: "hello"}},
	}
	body, _ := json.Marshal(chatReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var resp api.ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Model != "gpt-4o" {
		t.Errorf("model: got %q, want gpt-4o", resp.Model)
	}
	if resp.Choices[0].Message.Content != "proxy works" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
}

func TestProxy_RoutesToCorrectProvider(t *testing.T) {
	_, nc := testutil.StartNATS(t)

	nc2, _ := nats.Connect(nc.ConnectedUrl())
	t.Cleanup(func() { nc2.Close() })

	// Track which subject received the message
	var receivedSubject string
	var receivedModel string

	nc2.Subscribe("llm.provider.anthropic.>", func(msg *nats.Msg) {
		receivedSubject = msg.Subject
		var provReq api.ProviderRequest
		json.Unmarshal(msg.Data, &provReq)
		receivedModel = provReq.UpstreamModel

		resp := api.ChatResponse{
			ID:     "test-anth",
			Object: "chat.completion",
			Model:  provReq.UpstreamModel,
			Choices: []api.Choice{{
				Index:        0,
				Message:      &api.Message{Role: "assistant", Content: "hello"},
				FinishReason: "stop",
			}},
		}
		data, _ := json.Marshal(resp)
		msg.Respond(data)
	})
	nc2.Flush()

	p := setupProxy(t, nc)
	chatReq := api.ChatRequest{
		Model:    "anthropic.claude-sonnet-4-20250514",
		Messages: []api.Message{{Role: "user", Content: "hello"}},
	}
	body, _ := json.Marshal(chatReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	wantSubject := "llm.provider.anthropic.claude-sonnet-4-20250514"
	if receivedSubject != wantSubject {
		t.Errorf("subject: got %q, want %q", receivedSubject, wantSubject)
	}
	if receivedModel != "claude-sonnet-4-20250514" {
		t.Errorf("upstream model: got %q, want claude-sonnet-4-20250514", receivedModel)
	}
}

func TestProxy_InvalidModelFormat(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	p := setupProxy(t, nc)

	tests := []struct {
		name  string
		model string
	}{
		{"no dot", "gpt4o"},
		{"leading dot", ".gpt4o"},
		{"trailing dot", "openai."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatReq := api.ChatRequest{
				Model:    tt.model,
				Messages: []api.Message{{Role: "user", Content: "hello"}},
			}
			body, _ := json.Marshal(chatReq)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			p.server.Handler.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
		})
	}
}

func TestProxy_ErrorPropagation(t *testing.T) {
	_, nc := testutil.StartNATS(t)

	nc2, _ := nats.Connect(nc.ConnectedUrl())
	t.Cleanup(func() { nc2.Close() })

	nc2.Subscribe("llm.provider.openai.>", func(msg *nats.Msg) {
		errResp := api.ErrorResponse{
			Error: api.APIError{Message: "model not found", Type: "error", Code: "model_not_found"},
		}
		data, _ := json.Marshal(errResp)
		msg.Respond(data)
	})
	nc2.Flush()

	p := setupProxy(t, nc)
	chatReq := api.ChatRequest{
		Model:    "openai.nonexistent",
		Messages: []api.Message{{Role: "user", Content: "hello"}},
	}
	body, _ := json.Marshal(chatReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestProxy_InvalidJSON(t *testing.T) {
	_, nc := testutil.StartNATS(t)
	p := setupProxy(t, nc)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.server.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestProxy_NATSTimeout(t *testing.T) {
	_, nc := testutil.StartNATS(t)

	// No subscriber — will timeout
	p := New(nc, ":0", noopLogger())

	chatReq := api.ChatRequest{
		Model:    "openai.gpt-4o",
		Messages: []api.Message{{Role: "user", Content: "hello"}},
	}
	body, _ := json.Marshal(chatReq)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	start := time.Now()
	p.server.Handler.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status: got %d, want 502", w.Code)
	}

	// Should fail fast with no responders, not wait full 30s
	if elapsed > 5*time.Second {
		t.Errorf("took too long: %v", elapsed)
	}
}

func TestParseModel(t *testing.T) {
	tests := []struct {
		input        string
		wantProvider string
		wantUpstream string
		wantErr      bool
	}{
		{"openai.gpt-4o", "openai", "gpt-4o", false},
		{"anthropic.claude-sonnet-4-20250514", "anthropic", "claude-sonnet-4-20250514", false},
		{"ollama.llama3:8b", "ollama", "llama3:8b", false},
		{"gpt4o", "", "", true},
		{".model", "", "", true},
		{"provider.", "", "", true},
		{"", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			provider, upstream, err := parseModel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("err: got %v, wantErr %v", err, tt.wantErr)
			}
			if provider != tt.wantProvider {
				t.Errorf("provider: got %q, want %q", provider, tt.wantProvider)
			}
			if upstream != tt.wantUpstream {
				t.Errorf("upstream: got %q, want %q", upstream, tt.wantUpstream)
			}
		})
	}
}
