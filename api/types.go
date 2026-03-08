// Package api defines the wire format types shared across all components.
// All NATS messages use these types serialized as JSON.
package api

// ChatRequest is the wire format for chat completion requests.
// Published by clients (HTTP proxy or SDK) to llm.chat.complete.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      *Message `json:"message,omitempty"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ErrorResponse wraps errors in OpenAI-compatible format.
type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// ProviderRequest is the wire format on llm.provider.<name>.
// The proxy splits the "provider.model" name and sends the upstream model name.
type ProviderRequest struct {
	UpstreamModel string      `json:"upstream_model"`
	Request       ChatRequest `json:"request"`
}
