# nats-llm-gateway

A lightweight, high-performance LLM gateway that uses [NATS](https://nats.io) as its messaging backbone.

**Expose a single OpenAI-compatible API → route to any LLM provider.**

```
Client ──► Gateway (HTTP) ──► NATS ──► Provider Adapters ──► OpenAI / Anthropic / Ollama / ...
```

## Features

- **OpenAI-compatible API** — drop-in replacement; any OpenAI SDK client works unchanged
- **Multi-provider routing** — route to OpenAI, Anthropic, Ollama, and more via pluggable adapters
- **NATS-powered** — decoupled, scalable architecture with queue-group load balancing
- **Streaming** — SSE streaming support out of the box
- **Rate limiting** — per-key, per-model, and global rate limits
- **Authentication** — API-key auth with per-key model restrictions
- **Observable** — structured logging, Prometheus metrics
- **Single binary** — one `go build`, one container, done

## Quick Start

```bash
# Prerequisites: Go 1.22+, NATS server running on localhost:4222

# Clone and build
git clone https://github.com/kamalgs/nats-llm-gateway.git
cd nats-llm-gateway
go build -o gateway ./cmd/gateway

# Configure (edit configs/gateway.yaml with your API keys)
cp configs/gateway.yaml.example configs/gateway.yaml

# Run
./gateway --config configs/gateway.yaml
```

Then use it like any OpenAI endpoint:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

## Documentation

- [Design & Requirements](docs/DESIGN.md) — architecture, requirements, and milestones

## License

MIT
