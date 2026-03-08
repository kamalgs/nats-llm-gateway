# nats-llm-gateway

**Dynamic geographic load shifting for LLM inference** — powered by NATS global clustering.

GPU nodes, cloud API adapters, and clients connect to a NATS mesh from anywhere. The routing layer shifts inference load across regions based on capacity, latency, cost, and availability — in real time.

```
  São Paulo         US-East           Frankfurt          Tokyo
 ┌─────────┐    ┌──────────┐       ┌──────────┐     ┌─────────┐
 │GPU Nodes├──┐ │ Gateway  │       │ Gateway  │  ┌──┤GPU Nodes│
 └─────────┘  │ └────┬─────┘       └────┬─────┘  │  └─────────┘
 ┌─────────┐  │ ┌────┴─────┐       ┌────┴─────┐  │  ┌─────────┐
 │ Clients ├──┼─┤  NATS    ├───────┤  NATS    ├──┼──┤ Clients │
 └─────────┘  │ │  Cluster │       │  Cluster │  │  └─────────┘
 ┌─────────┐  │ └────┬─────┘       └────┬─────┘  │  ┌─────────┐
 │HTTP Proxy├─┘ ┌────┴─────┐       ┌────┴─────┐  └──┤HTTP Proxy│
 └─────────┘    │Cloud API │       │GPU Nodes │     └─────────┘
                │Adapter   │       │(on-prem) │
                └──────────┘       └──────────┘
```

## Why NATS?

- **Geographic routing** — requests flow to the nearest available inference node
- **Elastic scaling** — adding GPU capacity = starting a NATS subscriber. No reconfiguration.
- **Automatic failover** — if a region goes down, NATS routes to the next available
- **Leaf nodes** — on-prem GPU clusters connect via outbound connections. No public IPs, no VPNs.
- **Multi-tenancy** — NATS accounts provide hard isolation between tenants

## Client Onramps

The OpenAI-compatible API makes adoption frictionless:

| Onramp | Migration effort | Who it's for |
|---|---|---|
| **HTTP Proxy** | Change `baseURL` — zero code changes | Existing apps, any language |
| **JS/TS SDK** | Swap constructor — 1-2 lines | Node.js/browser apps wanting direct NATS |
| **Raw NATS** | Publish JSON to a subject | Advanced users, other languages |

### HTTP Proxy (existing apps — zero code changes)

```typescript
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
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-my-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello!"}]}'
```

### JS SDK (direct NATS — lower latency)

```typescript
import { NATSLLMClient } from 'nats-llm-client';

const client = await NATSLLMClient.connect({ servers: 'localhost:4222' });

const resp = await client.chat.completions.create({
  model: 'gpt-4o',
  messages: [{ role: 'user', content: 'Hello!' }],
});

await client.close();
```

## Quick Start

```bash
# Prerequisites: Go 1.22+, Node.js 18+, Docker (for NATS)

git clone https://github.com/kamalgs/nats-llm-gateway.git
cd nats-llm-gateway

# Start NATS
docker-compose up -d

# Configure and run
cp configs/gateway.yaml.example configs/gateway.yaml
# Edit configs/gateway.yaml with your provider API keys
make build
./gateway -config configs/gateway.yaml
```

## Testing

```bash
make test          # Unit + integration tests (embedded NATS, no external deps)
make bench         # Benchmarks
make test-sdk      # JS SDK tests
make test-all      # Everything
```

## Documentation

- [Design & Architecture](docs/DESIGN.md) — global routing architecture, requirements, NATS subject layout, milestones

## License

MIT
