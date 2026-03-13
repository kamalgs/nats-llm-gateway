# NATS Transport Binding for InferMesh Protocol v1.0

This document maps the [InferMesh wire protocol](PROTOCOL.md) to NATS subjects
and messaging patterns.

---

## 1. Subject Mapping

| Protocol Concept | NATS Subject | Example |
|------------------|--------------|---------|
| Model address `"gpt-4o"` | `llm.chat.gpt-4o` | `llm.chat.gpt-4o` |
| Model address `"qwen2.5:0.5b"` | `llm.chat.qwen2.5:0.5b` | Multi-token subject |
| Session address | `llm.session.{session_id}` | `llm.session.a1b2c3d4...` |

**Pattern:** `llm.chat.{model}` for model routing, `llm.session.{session_id}`
for session continuations.

---

## 2. Request-Reply Pattern

InferMesh uses the NATS request-reply pattern:

1. Client publishes a JSON-encoded `ProviderRequest` to the model or session
   subject, with a reply inbox.
2. Server processes the request and responds on the reply subject.
3. Response payload is a JSON-encoded `ChatResponse` or `ErrorResponse`.

```
Client                          NATS                          Provider
  │                               │                              │
  │  PUB llm.chat.gpt-4o         │                              │
  │  reply: _INBOX.xxx           │                              │
  │  payload: ProviderRequest    │                              │
  │ ─────────────────────────────►│                              │
  │                               │  MSG llm.chat.gpt-4o        │
  │                               │ ─────────────────────────────►│
  │                               │                              │
  │                               │  PUB _INBOX.xxx             │
  │                               │◄──────────────────────────── │
  │  MSG _INBOX.xxx              │                              │
  │◄──────────────────────────────│                              │
```

---

## 3. Load Balancing

- **Model subjects** (`llm.chat.{model}`): Providers subscribe using NATS
  **queue groups** (e.g., `"provider-openai"`). NATS distributes requests
  across group members automatically.
- **Session subjects** (`llm.session.{session_id}`): Providers use **plain
  subscriptions** (no queue group). Sessions are pinned to the creating
  provider instance — only that instance subscribes to the session subject.

---

## 4. Model Name Encoding

Model names are used directly in NATS subjects. Names containing dots
(e.g., `qwen2.5:0.5b`) create multi-token NATS subjects:

```
llm.chat.qwen2.5:0.5b  →  tokens: ["llm", "chat", "qwen2", "5:0", "5b"]
```

To match all models regardless of token depth, providers subscribe with the
`>` wildcard:

```
llm.chat.>  →  matches llm.chat.gpt-4o, llm.chat.qwen2.5:0.5b, etc.
```

When a provider serves a specific model, it subscribes to the exact subject:

```
llm.chat.gpt-4o  →  matches only gpt-4o
```

---

## 5. Reserved Subjects

| Subject Pattern | Purpose | Status |
|-----------------|---------|--------|
| `llm.chat.>` | Chat completion requests | Active |
| `llm.session.>` | Session continuation requests | Active |
| `llm.stream.>` | Reserved for future streaming | Reserved |
| `llm.health.>` | Reserved for health checks | Reserved |
| `llm.status.>` | Reserved for capacity signaling | Reserved |

Implementations MUST NOT use reserved subjects for other purposes.

---

## 6. Timeout Semantics

NATS request timeouts indicate that no provider responded within the allowed
window. Clients SHOULD treat a NATS request timeout the same as a
`session_expired` error for recovery purposes:

1. Discard the current session ID and session address.
2. Retransmit full conversation history to the model address.
3. Resume using the new session.

This ensures recovery from both explicit session expiry and transient
infrastructure failures (provider crash, network partition, NATS reconnection).
