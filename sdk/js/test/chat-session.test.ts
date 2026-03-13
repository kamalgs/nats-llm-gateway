import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { connect, type NatsConnection, StringCodec } from "nats";
import { InferMeshClient } from "../src/index.js";
import type { ChatCompletionResponse } from "../src/types.js";

const sc = StringCodec();
const NATS_URL = process.env.NATS_URL || "nats://localhost:4222";

describe("ChatSession", () => {
  let mockNc: NatsConnection;

  beforeAll(async () => {
    try {
      mockNc = await connect({ servers: NATS_URL });
    } catch {
      console.warn("NATS not available, skipping ChatSession tests");
      return;
    }

    // Mock provider that echoes back and supports sessions.
    let sessionCounter = 0;

    // Subscribe to specific models used in this test file
    // (avoid wildcard to prevent races with client.test.ts mock)
    const handler = (_err: unknown, msg: any) => {
      const provReq = JSON.parse(sc.decode(msg.data));
      const req = provReq.request;

      const sessionId = `test-session-${++sessionCounter}`;
      const sessionSubject = `llm.session.${sessionId}`;

      // Subscribe to the session subject for continuation.
      mockNc.subscribe(sessionSubject, {
        callback: (_err2, sessMsg) => {
          const sessReq = JSON.parse(sc.decode(sessMsg.data));
          const sessReqInner = sessReq.request;

          const resp: ChatCompletionResponse = {
            id: "sess-resp",
            object: "chat.completion",
            created: Date.now(),
            model: provReq.upstream_model,
            choices: [
              {
                index: 0,
                message: {
                  role: "assistant",
                  content: `session echo: ${sessReqInner.messages[0]?.content ?? ""}`,
                },
                finish_reason: "stop",
              },
            ],
            usage: {
              prompt_tokens: 5,
              completion_tokens: 3,
              total_tokens: 8,
            },
            session_id: sessReqInner.session_id,
            session_subject: sessionSubject,
          };
          sessMsg.respond(sc.encode(JSON.stringify(resp)));
        },
      });

      const resp: ChatCompletionResponse = {
        id: "test-mock",
        object: "chat.completion",
        created: Date.now(),
        model: provReq.upstream_model,
        choices: [
          {
            index: 0,
            message: {
              role: "assistant",
              content: `mock echo: ${req.messages[0]?.content ?? ""}`,
            },
            finish_reason: "stop",
          },
        ],
        usage: {
          prompt_tokens: 5,
          completion_tokens: 3,
          total_tokens: 8,
        },
        session_id: sessionId,
        session_subject: sessionSubject,
      };
      msg.respond(sc.encode(JSON.stringify(resp)));
    };

    for (const model of ["test", "model"]) {
      mockNc.subscribe(`llm.chat.${model}`, { callback: handler });
    }
  });

  afterAll(async () => {
    if (mockNc) await mockNc.drain();
  });

  it("text mode session works end-to-end", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("test");

    const result = await session.send("hello");
    expect(result.response.choices[0].message?.content).toBe(
      "mock echo: hello",
    );
    expect(result.bytesSent).toBeGreaterThan(0);
    expect(result.bytesReceived).toBeGreaterThan(0);

    await client.close();
  });

  it("tracks session_id from response", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("model");

    expect(session.getSessionId()).toBeNull();

    await session.send("hello");
    expect(session.getSessionId()).toBeTruthy();
    expect(session.getSessionId()!.startsWith("test-session-")).toBe(true);

    await client.close();
  });

  it("sends delta on session continuation", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("model");

    await session.send("first");
    const result = await session.send("second");

    // Second message should go via session subject and echo the delta.
    expect(result.response.choices[0].message?.content).toBe(
      "session echo: second",
    );

    await client.close();
  });

  it("maintains history", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("model");

    await session.send("first");
    await session.send("second");

    const history = session.getHistory();
    expect(history).toHaveLength(4); // 2 user + 2 assistant
    expect(history[0]).toEqual({ role: "user", content: "first" });
    expect(history[1].role).toBe("assistant");
    expect(history[2]).toEqual({ role: "user", content: "second" });
    expect(history[3].role).toBe("assistant");

    await client.close();
  });

  it("recovers from timeout by retransmitting full history", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("model");

    // First message establishes session.
    await session.send("hello");
    expect(session.getSessionId()).toBeTruthy();

    // Manually break the session by pointing to a non-existent subject.
    // This simulates a transport timeout (no subscriber on the subject).
    (session as any).sessionSubject = "llm.session.nonexistent";

    // Second message should timeout on the broken subject, then recover
    // by retransmitting full history to the model address.
    const result = await session.send("world");
    expect(result.response.choices[0].message?.content).toBe(
      "mock echo: hello",
    );

    // Session should be re-established.
    expect(session.getSessionId()).toBeTruthy();
    expect(session.getHistory()).toHaveLength(4);

    await client.close();
  });

  it("clear resets session state", async () => {
    if (!mockNc) return;
    const client = await InferMeshClient.connect({ natsUrl: NATS_URL });
    const session = client.chat.createSession("model");

    await session.send("hello");
    expect(session.getSessionId()).toBeTruthy();
    expect(session.getHistory()).toHaveLength(2);

    session.clear();
    expect(session.getSessionId()).toBeNull();
    expect(session.getHistory()).toHaveLength(0);

    await client.close();
  });
});
