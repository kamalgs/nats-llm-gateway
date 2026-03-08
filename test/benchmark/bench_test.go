// Package benchmark measures proxy and provider performance.
package benchmark

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

// fastMockLLM returns a mock that responds instantly with minimal JSON.
func fastMockLLM() *httptest.Server {
	resp := api.ChatResponse{
		ID:      "bench",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "bench-model",
		Choices: []api.Choice{{
			Index:        0,
			Message:      &api.Message{Role: "assistant", Content: "ok"},
			FinishReason: "stop",
		}},
		Usage: &api.Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
	}
	respBytes, _ := json.Marshal(resp)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}))
}

type benchStack struct {
	nc       *nats.Conn
	proxyURL string
	reqData  []byte
}

func setupBenchStack(b *testing.B) *benchStack {
	b.Helper()

	mock := fastMockLLM()
	b.Cleanup(mock.Close)

	ns, nc := testutil.StartNATS(&testing.T{})
	natsURL := testutil.NATSUrl(ns)
	b.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})

	log := silentLogger()

	adapter := openaiAdapter.NewAdapter(config.ProviderConfig{BaseURL: mock.URL, APIKey: "key"}, log)
	sub, _ := adapter.Subscribe(nc)
	b.Cleanup(func() { sub.Drain() })

	// Model uses provider.model convention
	req := api.ChatRequest{
		Model:    "openai.bench-model",
		Messages: []api.Message{{Role: "user", Content: "hi"}},
	}
	reqData, _ := json.Marshal(req)

	// Start proxy
	nc2, _ := nats.Connect(natsURL)
	b.Cleanup(func() { nc2.Close() })

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	proxyURL := fmt.Sprintf("http://%s", listener.Addr().String())
	listener.Close()
	p := proxy.New(nc2, listener.Addr().(*net.TCPAddr).String(), log)
	go p.Start()
	time.Sleep(100 * time.Millisecond)

	return &benchStack{nc: nc, proxyURL: proxyURL, reqData: reqData}
}

// BenchmarkNATSRequestReply measures raw NATS request/reply to a provider.
// This is the SDK path latency.
func BenchmarkNATSRequestReply(b *testing.B) {
	s := setupBenchStack(b)

	provReq := api.ProviderRequest{
		UpstreamModel: "bench-model",
		Request: api.ChatRequest{
			Model:    "openai.bench-model",
			Messages: []api.Message{{Role: "user", Content: "hi"}},
		},
	}
	provData, _ := json.Marshal(provReq)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		msg, err := s.nc.Request("llm.provider.openai", provData, 10*time.Second)
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		if len(msg.Data) == 0 {
			b.Fatal("empty response")
		}
	}
}

// BenchmarkNATSRequestReplyParallel measures concurrent NATS throughput.
func BenchmarkNATSRequestReplyParallel(b *testing.B) {
	s := setupBenchStack(b)

	provReq := api.ProviderRequest{
		UpstreamModel: "bench-model",
		Request: api.ChatRequest{
			Model:    "openai.bench-model",
			Messages: []api.Message{{Role: "user", Content: "hi"}},
		},
	}
	provData, _ := json.Marshal(provReq)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		nc2, err := nats.Connect(s.nc.ConnectedUrl())
		if err != nil {
			b.Fatalf("connect: %v", err)
		}
		defer nc2.Close()

		for pb.Next() {
			msg, err := nc2.Request("llm.provider.openai", provData, 10*time.Second)
			if err != nil {
				b.Fatalf("request: %v", err)
			}
			if len(msg.Data) == 0 {
				b.Fatal("empty response")
			}
		}
	})
}

// BenchmarkHTTPProxy measures the full HTTP proxy path latency.
func BenchmarkHTTPProxy(b *testing.B) {
	s := setupBenchStack(b)

	client := &http.Client{Timeout: 10 * time.Second}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		resp, err := client.Post(
			s.proxyURL+"/v1/chat/completions",
			"application/json",
			bytes.NewReader(s.reqData),
		)
		if err != nil {
			b.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			b.Fatalf("status: %d", resp.StatusCode)
		}
	}
}

// BenchmarkHTTPProxyParallel measures concurrent HTTP proxy throughput.
func BenchmarkHTTPProxyParallel(b *testing.B) {
	s := setupBenchStack(b)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 100,
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Post(
				s.proxyURL+"/v1/chat/completions",
				"application/json",
				bytes.NewReader(s.reqData),
			)
			if err != nil {
				b.Fatalf("request: %v", err)
			}
			resp.Body.Close()
		}
	})
}

// BenchmarkJSONMarshal measures the serialization overhead.
func BenchmarkJSONMarshal(b *testing.B) {
	req := api.ChatRequest{
		Model: "openai.gpt-4o",
		Messages: []api.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Explain quantum computing in simple terms."},
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		var decoded api.ChatRequest
		json.Unmarshal(data, &decoded)
	}
}
