import type { NatsConnection } from "nats";
import { StringCodec, createInbox } from "nats";
import type {
  ChatCompletionRequest,
  ChatCompletionResponse,
  ErrorResponse,
  Message,
} from "./types.js";

const sc = StringCodec();

// ProviderRequest matches the Go api.ProviderRequest wire format.
interface ProviderRequest {
  upstream_model: string;
  request: ChatCompletionRequest;
}

export interface ChatCompletionResult {
  response: ChatCompletionResponse;
  bytesSent: number;
  bytesReceived: number;
}

export class ChatCompletions {
  constructor(private nc: NatsConnection) {}

  async createWithStats(req: ChatCompletionRequest): Promise<ChatCompletionResult> {
    return this._send(req);
  }

  async create(req: ChatCompletionRequest): Promise<ChatCompletionResponse> {
    const result = await this._send(req);
    return result.response;
  }

  /**
   * Send to a specific subject (used for session-based messaging).
   */
  async sendToSubject(
    subject: string,
    req: ChatCompletionRequest,
  ): Promise<ChatCompletionResult> {
    const providerReq: ProviderRequest = {
      upstream_model: req.model,
      request: req,
    };

    return this._publish(subject, providerReq);
  }

  private async _send(req: ChatCompletionRequest): Promise<ChatCompletionResult> {
    const subject = `llm.chat.${req.model}`;

    const providerReq: ProviderRequest = {
      upstream_model: req.model,
      request: req,
    };

    return this._publish(subject, providerReq);
  }

  private async _publish(
    subject: string,
    providerReq: ProviderRequest,
  ): Promise<ChatCompletionResult> {
    const payload = sc.encode(JSON.stringify(providerReq));

    const replySubject = createInbox();
    const sub = this.nc.subscribe(replySubject, { max: 1 });

    this.nc.publish(subject, payload, { reply: replySubject });

    for await (const msg of sub) {
      const bytesReceived = msg.data.length;
      let data: any;
      try {
        data = JSON.parse(sc.decode(msg.data));
      } catch {
        throw new Error("no response received");
      }

      if (data.error) {
        const err = data as ErrorResponse;
        throw new Error(`[${err.error.code}] ${err.error.message}`);
      }

      return {
        response: data as ChatCompletionResponse,
        bytesSent: payload.length,
        bytesReceived,
      };
    }

    throw new Error("no response received");
  }
}

export interface ChatSessionOptions {
  debug?: boolean;
}

/**
 * ChatSession maintains a sticky session with a provider.
 * Sends only new (delta) messages on subsequent turns — the server
 * accumulates full history via the session subject.
 */
export class ChatSession {
  private sessionId: string | null = null;
  private sessionSubject: string | null = null;
  private history: Message[] = [];
  private completions: ChatCompletions;
  private model: string;
  private debug: boolean;

  constructor(completions: ChatCompletions, model: string, opts?: ChatSessionOptions) {
    this.completions = completions;
    this.model = model;
    this.debug = opts?.debug ?? false;
  }

  private log(...args: unknown[]): void {
    if (this.debug) console.log("[ChatSession]", ...args);
  }

  async send(
    content: string,
    opts?: { temperature?: number; max_tokens?: number },
  ): Promise<ChatCompletionResult> {
    this.history.push({ role: "user", content });

    this.log(`send: "${content.slice(0, 80)}${content.length > 80 ? "..." : ""}"`);

    try {
      let result: ChatCompletionResult;

      const reqBase: Partial<ChatCompletionRequest> = {
        model: this.model,
        ...opts,
      };

      if (this.sessionId && this.sessionSubject) {
        // Session mode: send only the new message (delta).
        try {
          result = await this.completions.sendToSubject(
            this.sessionSubject,
            {
              ...reqBase,
              messages: [{ role: "user", content }],
              session_id: this.sessionId,
            } as ChatCompletionRequest,
          );
        } catch (err: any) {
          const isSessionError =
            err.message.includes("session_expired") ||
            err.message.includes("TIMEOUT") ||
            err.message.includes("no response received");
          if (isSessionError) {
            this.log("session lost, falling back to full history");
            this.sessionId = null;
            this.sessionSubject = null;
            result = await this.completions.createWithStats({
              ...reqBase,
              messages: this.history,
            } as ChatCompletionRequest);
          } else {
            throw err;
          }
        }
      } else {
        // No session yet: send full history.
        result = await this.completions.createWithStats({
          ...reqBase,
          messages: this.history,
        } as ChatCompletionRequest);
      }

      // Track session from response.
      if (result.response.session_id) {
        this.sessionId = result.response.session_id;
      }
      if (result.response.session_subject) {
        this.sessionSubject = result.response.session_subject;
      }

      const replyMsg = result.response.choices[0]?.message;
      if (replyMsg) {
        this.history.push({ role: "assistant", content: replyMsg.content ?? "" });
      }

      return result;
    } catch (err) {
      this.history.pop(); // remove failed user message
      throw err;
    }
  }

  clear(): void {
    this.sessionId = null;
    this.sessionSubject = null;
    this.history = [];
  }

  getSessionId(): string | null {
    return this.sessionId;
  }

  getHistory(): Message[] {
    return [...this.history];
  }
}

export class Chat {
  completions: ChatCompletions;

  constructor(nc: NatsConnection) {
    this.completions = new ChatCompletions(nc);
  }

  createSession(model: string, opts?: ChatSessionOptions): ChatSession {
    return new ChatSession(this.completions, model, opts);
  }
}
