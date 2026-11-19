// Wire types for the managed-app SDK protocol (docs/API_SPEC.md, "SDK endpoints").
// Field names are camelCase exactly as the server returns/accepts them.

/** Request types accepted by the Inbox (DATA_MODEL `requests.type`). */
export const REQUEST_TYPES = [
  "bug",
  "feature",
  "improvement",
  "refactor",
  "performance",
  "security",
  "docs",
  "release",
] as const;
export type SdkRequestType = (typeof REQUEST_TYPES)[number];

/** Request status as mirrored from the task state (DATA_MODEL "Domain notes"). */
export type SdkRequestStatus =
  | "new"
  | "triaged"
  | "in_progress"
  | "waiting_input"
  | "review"
  | "done"
  | "rejected"
  | "failed";

/** Statuses after which polling can stop (no further progress expected). */
export const TERMINAL_REQUEST_STATUSES: readonly SdkRequestStatus[] = ["done", "rejected", "failed"];

/**
 * Progress mapping is computed server-side so every SDK agrees. `state` is an
 * opaque machine value; render `label` and `percent`.
 */
export interface SdkProgress {
  state: string;
  label: string;
  /** 0..100 */
  percent: number;
}

export interface SdkResult {
  summary: string;
  diffStat: string;
  /** "" | "passed" | "failed" | "skipped" */
  testStatus: string;
}

export interface SdkRelease {
  version: string;
  status: string;
}

/** A request as seen by a reporter. No worktree paths, prompts, diffs or internal errors. */
export interface SdkRequest {
  id: string;
  projectId: string;
  type: SdkRequestType;
  title: string;
  description: string;
  status: SdkRequestStatus;
  createdAt: string;
  updatedAt: string;
  progress: SdkProgress;
  /** Pending agent question; present when status is `waiting_input`. */
  question?: string;
  result?: SdkResult;
  release?: SdkRelease;
}

export type SdkDiagnosticKind = "log" | "error" | "context";

export interface SdkDiagnostic {
  kind: SdkDiagnosticKind;
  name: string;
  content: string;
}

export type SdkAttachmentKind = "screenshot" | "log";

/** Attachment MIME allowlist (SECURITY control 2: png/jpeg/webp/text). */
export const ATTACHMENT_MIME_TYPES = ["image/png", "image/jpeg", "image/webp", "text/plain"] as const;
export type SdkAttachmentMime = (typeof ATTACHMENT_MIME_TYPES)[number];

/** Attachment as sent on the wire (JSON, base64 payload). */
export interface SdkAttachmentPayload {
  kind: SdkAttachmentKind;
  filename: string;
  mime: string;
  dataBase64: string;
}

/** Body of `POST /api/v1/sdk/requests`. */
export interface CreateSdkRequestBody {
  type: SdkRequestType;
  title?: string;
  description: string;
  installationId: string;
  appVersion?: string;
  platform?: string;
  diagnostics?: SdkDiagnostic[];
  attachments?: SdkAttachmentPayload[];
}

/** Body of `POST /api/v1/sdk/requests/{id}/answer`. */
export interface AnswerSdkRequestBody {
  installationId: string;
  text: string;
}

/** Response of `GET /api/v1/sdk/requests?installationId=`. */
export interface ListSdkRequestsResponse {
  requests: SdkRequest[];
}

export type SdkUpdateKind = "web-reload" | "store" | "electron-feed" | "none";

/** Response of `GET /api/v1/sdk/releases/latest`. */
export interface ReleaseCheckResult {
  latestVersion: string;
  updateAvailable: boolean;
  notes: string;
  publishedAt: string;
  update: {
    kind: SdkUpdateKind;
    url?: string;
  };
}

export type ApiErrorCode =
  | "not_found"
  | "bad_request"
  | "unauthorized"
  | "forbidden"
  | "conflict"
  | "illegal_transition"
  | "tls_required"
  | "rate_limited"
  | "internal";

/** Error body: `{"error": {"code", "message"}}`. */
export interface ApiErrorBody {
  error: {
    code: ApiErrorCode | (string & {});
    message: string;
  };
}

/** Size limits from SECURITY control 2 and API_SPEC (rate limit / body cap). */
export const SDK_LIMITS = {
  /** Maximum request body, including base64 attachments. */
  bodyBytes: 6 * 1024 * 1024,
  /** Maximum single attachment (decoded bytes). */
  attachmentBytes: 5 * 1024 * 1024,
  /** Maximum description (UTF-8 bytes). */
  descriptionBytes: 64 * 1024,
} as const;
