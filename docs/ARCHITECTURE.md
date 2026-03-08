# InferMesh Architecture

## What InferMesh Does

InferMesh is a unified LLM API gateway. You send one HTTP request to one endpoint,
and it routes to the right LLM provider (OpenAI, Anthropic, Ollama, etc.) based on
the model name. Geographic distribution happens through NATS leaf nodes — no
application code needed for that part.

## The Three Application Components

There are exactly three application-level components. Everything else is NATS
infrastructure.

```
┌─────────────────────────────────────────────────────────────────┐
│                     Application Components                      │
│                                                                 │
│   ┌───────────┐        ┌────────────┐        ┌──────────────┐  │
│   │   Proxy   │──NATS──│   Router   │──NATS──│   Provider   │  │
│   │  (HTTP→   │        │  (model    │        │  (NATS→HTTP  │  │
│   │   NATS)   │        │  resolver) │        │   to LLM)    │  │
│   └───────────┘        └────────────┘        └──────────────┘  │
│                                                                 │
│   cmd/proxy            cmd/gateway            cmd/provider      │
│   internal/proxy       internal/gateway       internal/provider │
└─────────────────────────────────────────────────────────────────┘
```

### 1. Proxy (`cmd/proxy`, `internal/proxy`)

**Responsibility:** HTTP-to-NATS bridge. Nothing else.

- Exposes `POST /v1/chat/completions` (OpenAI-compatible)
- Exposes `GET /health`
- Receives HTTP request, publishes it to NATS subject `llm.chat.complete`
- Waits for NATS reply, writes it back as HTTP response
- Maps error codes to HTTP status codes (404, 429, 400, 500)
- Has zero knowledge of models, providers, or configs

**NATS interaction:**
```
Publishes to:  llm.chat.complete  (request-reply)
```

**Why it exists:** So existing OpenAI SDKs and `curl` work unchanged — just
point `base_url` at the proxy.

---

### 2. Router (`cmd/gateway`, `internal/gateway`)

> Note: The code calls this "gateway" but it's really a **router** — it resolves
> model aliases and forwards to the right provider. The actual NATS gateway/leaf
> node topology is pure infrastructure (see below).

**Responsibility:** Model-to-provider resolution and request forwarding.

- Subscribes to `llm.chat.complete` (queue group: `gateway`)
- Looks up the requested model name in config (e.g., `"gpt-4o"` → provider
  `"openai"`, upstream model `"gpt-4o-2024-08-06"`)
- Wraps the request in a `ProviderRequest` with the resolved upstream model name
- Publishes to `llm.provider.<provider-name>` (e.g., `llm.provider.openai`)
- Waits for the provider's reply, passes it back to the proxy

**NATS interaction:**
```
Subscribes to: llm.chat.complete         (queue group: "gateway")
Publishes to:  llm.provider.<provider>   (request-reply, 30s timeout)
```

**What it knows:** The model→provider mapping from config. That's it. It does not
know how to call any LLM API. It does not start providers.

---

### 3. Provider (`cmd/provider`, `internal/provider/*`)

**Responsibility:** NATS-to-HTTP bridge for a specific LLM API.

Each provider is a NATS queue subscriber that:
- Subscribes to `llm.provider.<name>` (e.g., `llm.provider.anthropic`)
- Receives a `ProviderRequest` from the router
- Translates it into the provider's native HTTP API format
- Calls the upstream LLM API over HTTP
- Translates the response back into the unified `ChatResponse` format
- Replies on NATS

**NATS interaction (per provider):**
```
openai:    subscribes to llm.provider.openai     (queue: "provider-openai")
anthropic: subscribes to llm.provider.anthropic  (queue: "provider-anthropic")
ollama:    subscribes to llm.provider.ollama     (queue: "provider-ollama")
```

**What each adapter does differently:**

| Adapter   | Upstream endpoint          | Auth header         | Translate? | Timeout |
|-----------|----------------------------|---------------------|------------|---------|
| OpenAI    | `{base}/chat/completions`  | `Authorization: Bearer` | No — native format | 60s |
| Anthropic | `{base}/v1/messages`       | `x-api-key` + `anthropic-version` | Yes — system messages, max_tokens, response mapping | 60s |
| Ollama    | `{base}/chat/completions`  | None                | No — OpenAI-compatible | 120s |

**Anthropic translation details:**
- Extracts `system` role messages → Anthropic `system` top-level field
- Defaults `max_tokens` to 4096 (Anthropic requires it)
- Maps response: `content[0].text` → `choices[0].message.content`
- Maps `stop_reason` → `finish_reason` ("max_tokens" → "length")
- Maps `input_tokens`/`output_tokens` → `prompt_tokens`/`completion_tokens`

---

## The Complete Request Flow

```
curl POST /v1/chat/completions {model: "claude-sonnet", messages: [...]}
  │
  ▼
┌──────────┐  HTTP
│  Proxy   │  Validates JSON, publishes to NATS
└────┬─────┘
     │  NATS publish: "llm.chat.complete"
     ▼
┌──────────┐
│  Router  │  Looks up "claude-sonnet" in config
│          │  → provider: "anthropic", upstream: "claude-sonnet-4-20250514"
│          │  Wraps in ProviderRequest
└────┬─────┘
     │  NATS publish: "llm.provider.anthropic"
     ▼
┌──────────┐
│ Provider │  Translates to Anthropic Messages API format
│(anthropic)│  POST https://api.anthropic.com/v1/messages
└────┬─────┘
     │  HTTP response from Anthropic
     │  Translates back to unified ChatResponse
     │
     ▼  (NATS reply chain unwinds)
Router receives reply → passes through → Proxy receives reply → HTTP 200
```

Every arrow between application components is a NATS request-reply message.
No component calls another directly. They only share NATS subjects.

---

## NATS Infrastructure (No Application Code)

NATS leaf nodes handle geographic distribution. This is pure infrastructure —
config files, no Go code.

```
                    ┌─────────────────────┐
                    │      NATS Hub       │
                    │   :4222  leaf:7422  │
                    └──┬──────────────┬───┘
          leaf connect │              │ leaf connect
    ┌──────────────────┘              └───────────────────┐
    │                                                     │
┌───▼──────────────┐                          ┌───────────▼────────┐
│  Leaf: Client    │                          │  Leaf: Provider    │
│  (US-East)       │                          │  (EU-West)         │
│  :4225           │                          │  :4223             │
└──────────────────┘                          └────────────────────┘
 Proxy connects here                          Providers connect here
```

**What leaf nodes do:** Transparently extend the NATS subject space across
geographic regions. When the proxy publishes `llm.chat.complete` on the client
leaf, NATS automatically routes it to the hub, which routes it to whichever
leaf has a subscriber for that subject.

**What leaf nodes DON'T do:** No application logic. No model resolution. No
request transformation. They're just NATS servers with a `leafnodes.remotes`
config pointing at the hub.

**Config files** (in `demo/`):
- `nats-hub.conf` — central hub, accepts leaf connections on port 7422
- `nats-leaf-client.conf` — client-side leaf, connects to hub
- `nats-leaf-cloud.conf` — provider-side leaf for cloud APIs
- `nats-leaf-edge.conf` — provider-side leaf for local models (Ollama)

---

## Code Organization

```
cmd/                          # Entrypoints — thin main() wrappers
├── proxy/main.go             #   Starts the HTTP proxy
├── gateway/main.go           #   Starts the router (and optionally providers)
├── provider/main.go          #   Starts a single provider adapter
└── mockllm/main.go           #   Starts the mock LLM server for testing

internal/                     # Core logic — libraries used by cmd/
├── proxy/proxy.go            #   HTTP server, request forwarding
├── gateway/gateway.go        #   Model resolution, NATS routing
├── provider/
│   ├── provider.go           #   Provider interface definition
│   ├── openai/openai.go      #   OpenAI adapter
│   ├── anthropic/anthropic.go#   Anthropic adapter (with format translation)
│   └── ollama/ollama.go      #   Ollama adapter
├── config/config.go          #   YAML config loader
└── testutil/nats.go          #   Embedded NATS server for tests

api/                          # Wire format types shared by all components
└── types.go                  #   ChatRequest, ChatResponse, ProviderRequest, etc.
```

**Why `cmd/` vs `internal/`?** Standard Go project layout:
- `cmd/` — each subdirectory is a `main` package that produces a binary.
  Contains only startup/wiring code: parse env vars, create dependencies,
  call into `internal/`.
- `internal/` — the actual logic. Can only be imported by code in this module
  (Go enforces this). This is where the real work happens.
- `api/` — shared types. Not in `internal/` because external code (SDKs, tests)
  may need these types.

---

## Deployment Modes

### Mode 1: All-in-one (local dev)

`cmd/gateway/main.go` can start the router AND provider adapters in one process.
This is a convenience for `go run ./cmd/gateway` during development.

```
┌─────────────────────────────────────────┐
│  Single process (cmd/gateway)           │
│                                         │
│  Router + OpenAI + Anthropic + Ollama   │
│  all subscribe on same NATS connection  │
└─────────────────────────────────────────┘
```

### Mode 2: Separate processes (production / demo)

Each component runs as its own container/process, connected to different NATS
leaf nodes.

```
Container: proxy         → connects to client leaf
Container: gateway       → connects to hub (runs router only, no providers in config)
Container: provider-openai    → connects to cloud leaf
Container: provider-anthropic → connects to cloud leaf
Container: provider-ollama    → connects to edge leaf
```

The demo docker-compose (`demo/docker-compose.yaml`) uses Mode 2 with 10
services.

---

## Configuration

```yaml
# demo/demo.yaml
nats:
  url: "${NATS_URL:-nats://localhost:4222}"

models:                              # Model alias → provider + upstream name
  "gpt-4o":
    provider: openai
    upstream_model: "gpt-4o-2024-08-06"
  "claude-sonnet":
    provider: anthropic
    upstream_model: "claude-sonnet-4-20250514"
  "llama3":
    provider: ollama
    upstream_model: "llama3:8b"

providers:                           # Provider connection details
  openai:
    base_url: "${OPENAI_BASE_URL:-https://api.openai.com/v1}"
    api_key: "${OPENAI_API_KEY}"
  anthropic:
    base_url: "${ANTHROPIC_BASE_URL:-https://api.anthropic.com}"
    api_key: "${ANTHROPIC_API_KEY}"
  ollama:
    base_url: "${OLLAMA_BASE_URL:-http://localhost:11434/v1}"
```

Environment variables are expanded at load time (`${VAR}` syntax).

---

## Scaling

Because every component uses NATS queue groups, you scale by running more
instances:

- **Multiple proxies:** All subscribe to the same HTTP port behind a load
  balancer, all publish to the same NATS subject.
- **Multiple routers:** Queue group `"gateway"` ensures only one handles each
  request.
- **Multiple providers:** Queue group `"provider-openai"` distributes requests
  across OpenAI adapter instances. Run 5 instances to handle 5 concurrent
  OpenAI requests.

No coordination needed. NATS handles it.

---

## NATS Subjects Reference

| Subject                    | Publisher | Subscriber      | Payload          |
|----------------------------|-----------|-----------------|------------------|
| `llm.chat.complete`        | Proxy     | Router          | `ChatRequest`    |
| `llm.provider.openai`      | Router    | OpenAI adapter  | `ProviderRequest`|
| `llm.provider.anthropic`   | Router    | Anthropic adapter| `ProviderRequest`|
| `llm.provider.ollama`      | Router    | Ollama adapter  | `ProviderRequest`|

All use NATS request-reply pattern. Replies carry `ChatResponse` or `ErrorResponse`.
