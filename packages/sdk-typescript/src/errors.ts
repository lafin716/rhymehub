/**
 * Error thrown by every SDK call. `status` is the HTTP status (0 for client-side
 * validation and network failures); `code` is the API error code when present,
 * otherwise an SDK code such as `invalid_key`, `bad_request` or `network_error`.
 */
export class RhymehubError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "RhymehubError";
    this.status = status;
    this.code = code;
  }
}
