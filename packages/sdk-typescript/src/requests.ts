import {
  ATTACHMENT_MIME_TYPES,
  SDK_LIMITS,
  TERMINAL_REQUEST_STATUSES,
  redact,
  type AnswerSdkRequestBody,
  type CreateSdkRequestBody,
  type ListSdkRequestsResponse,
  type SdkAttachmentKind,
  type SdkAttachmentPayload,
  type SdkDiagnostic,
  type SdkRequest,
  type SdkRequestType,
} from "@rhymehub/shared";
import { RhymehubError } from "./errors";
import { HttpClient } from "./http";
import { bytesToBase64, decodeUtf8, encodeUtf8, randomUuid, readBlob, utf8ByteLength } from "./encoding";
import { MAX_DIAGNOSTIC_CHARS, tailChars } from "./diagnostics";

export type SdkAttachmentInput =
  | {
      kind: SdkAttachmentKind;
      /** Browser File/Blob, e.g. from `<input type="file">`. MIME is taken from `file.type`. */
      file: Blob;
      filename?: string;
    }
  | {
      kind: SdkAttachmentKind;
      filename: string;
      mime: string;
      /** Text (UTF-8 encoded), bytes, or an ArrayBuffer. */
      data: string | Uint8Array | ArrayBuffer;
    };

export interface CreateRequestInput {
  type: SdkRequestType;
  title?: string;
  description: string;
  diagnostics?: SdkDiagnostic[];
  attachments?: SdkAttachmentInput[];
}

export interface WatchOptions {
  /** Polling interval. Default 3000 ms. Polling stops automatically on done/rejected/failed. */
  intervalMs?: number;
  onError?: (error: unknown) => void;
}

export interface RequestsContext {
  http: HttpClient;
  installationId: () => string;
  appVersion?: string;
  platform?: string;
}

function bad(message: string): RhymehubError {
  return new RhymehubError(400, "bad_request", message);
}

function sanitizeFilename(name: string): string {
  const cleaned = name.replace(/[^A-Za-z0-9._-]+/g, "_").replace(/^[._]+/, "").slice(0, 120);
  return cleaned || "attachment";
}

async function prepareAttachment(input: SdkAttachmentInput): Promise<SdkAttachmentPayload> {
  let bytes: Uint8Array;
  let filename: string;
  let mime: string;
  if ("file" in input) {
    filename = input.filename ?? (input.file as { name?: string }).name ?? input.kind;
    mime = input.file.type;
    bytes = await readBlob(input.file);
  } else {
    filename = input.filename;
    mime = input.mime;
    bytes =
      typeof input.data === "string"
        ? encodeUtf8(input.data)
        : input.data instanceof Uint8Array
          ? input.data
          : new Uint8Array(input.data);
  }

  mime = mime.split(";")[0].trim().toLowerCase();
  if (!(ATTACHMENT_MIME_TYPES as readonly string[]).includes(mime)) {
    throw bad(`Unsupported attachment type "${mime || "unknown"}". Allowed: ${ATTACHMENT_MIME_TYPES.join(", ")}`);
  }
  if (bytes.byteLength > SDK_LIMITS.attachmentBytes) {
    throw bad(`Attachment "${filename}" is larger than ${SDK_LIMITS.attachmentBytes / (1024 * 1024)} MiB`);
  }
  if (mime === "text/plain") {
    // Text attachments (logs) are scrubbed the same way as descriptions.
    bytes = encodeUtf8(redact(decodeUtf8(bytes)).text);
  }
  return {
    kind: input.kind,
    filename: sanitizeFilename(filename),
    mime,
    dataBase64: bytesToBase64(bytes),
  };
}

export class RequestsApi {
  constructor(private readonly ctx: RequestsContext) {}

  /**
   * Create a request. Description, title and diagnostics are redacted client-side
   * before upload. The POST carries a fresh Idempotency-Key, so transient failures
   * are retried with the same key and the server dedupes them.
   */
  async create(input: CreateRequestInput): Promise<SdkRequest> {
    const rawDescription = (input.description ?? "").trim();
    if (rawDescription === "") throw bad("description is required");
    if (utf8ByteLength(rawDescription) > SDK_LIMITS.descriptionBytes) {
      throw bad("description exceeds 64 KiB");
    }

    const description = redact(rawDescription).text;
    const title = input.title?.trim() ? redact(input.title.trim()).text : undefined;

    const diagnostics = (input.diagnostics ?? []).map((d) => ({
      kind: d.kind,
      name: d.name,
      content: redact(tailChars(d.content, MAX_DIAGNOSTIC_CHARS)).text,
    }));
    const attachments = await Promise.all((input.attachments ?? []).map(prepareAttachment));

    const body: CreateSdkRequestBody = {
      type: input.type,
      description,
      installationId: this.ctx.installationId(),
      ...(this.ctx.appVersion ? { appVersion: this.ctx.appVersion } : {}),
      ...(this.ctx.platform ? { platform: this.ctx.platform } : {}),
      ...(title !== undefined ? { title } : {}),
      ...(diagnostics.length > 0 ? { diagnostics } : {}),
      ...(attachments.length > 0 ? { attachments } : {}),
    };

    const serialized = JSON.stringify(body);
    if (utf8ByteLength(serialized) > SDK_LIMITS.bodyBytes) {
      throw new RhymehubError(413, "payload_too_large", "request with attachments exceeds the 6 MiB limit");
    }

    return this.ctx.http.request<SdkRequest>({
      method: "POST",
      path: "/sdk/requests",
      body,
      headers: { "Idempotency-Key": randomUuid() },
      retries: 2,
    });
  }

  /** Requests submitted by this installation only. */
  async list(): Promise<SdkRequest[]> {
    const res = await this.ctx.http.request<ListSdkRequestsResponse>({
      method: "GET",
      path: "/sdk/requests",
      query: { installationId: this.ctx.installationId() },
      retries: 2,
    });
    return res.requests ?? [];
  }

  async get(id: string): Promise<SdkRequest> {
    return this.ctx.http.request<SdkRequest>({
      method: "GET",
      path: `/sdk/requests/${encodeURIComponent(id)}`,
      query: { installationId: this.ctx.installationId() },
      retries: 2,
    });
  }

  /** Answer the agent's pending question. The server accepts this only while the task is WAITING_INPUT. */
  async answer(id: string, text: string): Promise<SdkRequest> {
    const trimmed = (text ?? "").trim();
    if (trimmed === "") throw bad("answer text is required");
    if (utf8ByteLength(trimmed) > SDK_LIMITS.descriptionBytes) throw bad("answer exceeds 64 KiB");
    const body: AnswerSdkRequestBody = {
      installationId: this.ctx.installationId(),
      text: redact(trimmed).text,
    };
    return this.ctx.http.request<SdkRequest>({
      method: "POST",
      path: `/sdk/requests/${encodeURIComponent(id)}/answer`,
      body,
      retries: 0,
    });
  }

  /**
   * Poll a request and call `onUpdate` whenever its contents change (first call is immediate).
   * Returns an unsubscribe function.
   */
  watch(id: string, onUpdate: (request: SdkRequest) => void, options: WatchOptions = {}): () => void {
    const intervalMs = options.intervalMs ?? 3000;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let lastSignature: string | undefined;

    const tick = async (): Promise<void> => {
      if (stopped) return;
      try {
        const request = await this.get(id);
        if (stopped) return;
        const signature = JSON.stringify(request);
        if (signature !== lastSignature) {
          lastSignature = signature;
          onUpdate(request);
        }
        if ((TERMINAL_REQUEST_STATUSES as readonly string[]).includes(request.status)) {
          stopped = true;
          return;
        }
      } catch (error) {
        options.onError?.(error);
      }
      if (!stopped) timer = setTimeout(() => void tick(), intervalMs);
    };

    void tick();
    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
    };
  }
}
