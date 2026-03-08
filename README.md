# nats-llm-gateway

A **NATS-native** LLM gateway. Route to any LLM provider through NATS with two integration paths:

- **HTTP Proxy** — change `baseURL`, zero code changes. Works with any OpenAI SDK, LangChain, Vercel AI SDK, curl.
- **NATS SDK** — direct NATS connection for lower latency and native streaming.

```
┌─────────────────┐                                   ┌───────────────┐
│ Existing app    │──HTTP──► HTTP Proxy ──► NATS ──►  │   Gateway     │──► OpenAI
│ (just change    │                          │        │   Service     │──► Anthropic
│  baseURL)       │                          │        │               │──► Ollama
└─────────────────┘                          │        └───────────────┘
┌─────────────────┐                          │
│ New app         │──NATS (TCP/WS) ──────────┘
│ (JS SDK)        │
└─────────────────┘
```

## Features

- **Zero-migration HTTP proxy** — `POST /v1/chat/completions` with SSE streaming
- **NATS-native JS SDK** — OpenAI-compatible `client.chat.completions.create()` over NATS
- **Multi-provider** — route to OpenAI, Anthropic, Ollama, and more
- **Streaming** — SSE for HTTP clients, async iterables for SDK clients
- **Rate limiting** — per-key, per-model, and global limits (NATS KV backed)
- **Auth** — NATS native auth (NKeys/JWTs) + gateway API key validation

## Quick Start

```bash
# Prerequisites: Go 1.22+, Node.js 18+, Docker (for NATS)

git clone https://github.com/kamalgs/nats-llm-gateway.git
cd nats-llm-gateway

# Start NATS + gateway + HTTP proxy
cp configs/gateway.yaml.example configs/gateway.yaml
# Edit configs/gateway.yaml with your provider API keys
docker-compose up
```

### Option 1: HTTP Proxy (existing apps — zero code changes)

```typescript
// Just change baseURL — works with OpenAI SDK, LangChain, anything
import OpenAI from 'openai';

const client = new OpenAI({
  baseURL: 'http://localhost:8080/v1',
  apiKey: 'sk-my-key',
});

const resp = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});
```

```bash
# Works with curl too
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-my-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}'
```

### Option 2: NATS SDK (new apps — full NATS benefits)

```typescript
import { NATSLLMClient } from 'nats-llm-client';

const client = new NATSLLMClient({
  natsUrl: 'nats://localhost:4222',
  apiKey: 'sk-my-key',
});

// Same API as OpenAI SDK
const resp = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});

// Streaming
const stream = await client.chat.completions.create({
  model: 'claude-sonnet',
  messages: [{ role: 'user', content: 'Write a poem' }],
  stream: true,
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content || '');
}

await client.close();
```

## Documentation

- [Design & Requirements](docs/DESIGN.md) — architecture, integration tiers, NATS subject layout, and milestones

## License

MIT
