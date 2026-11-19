import type { ReleaseCheckResult } from "@rhymehub/shared";
import { HttpClient } from "./http";

export interface ReleasesContext {
  http: HttpClient;
  appVersion?: string;
  platform?: string;
}

export interface CheckForUpdateOptions {
  /** Defaults to the client's `appVersion`. */
  currentVersion?: string;
  /** Defaults to the client's `platform`. */
  platform?: string;
}

export class ReleasesApi {
  constructor(private readonly ctx: ReleasesContext) {}

  /** `GET /sdk/releases/latest`. Returns the API shape unchanged. */
  checkForUpdate(options: CheckForUpdateOptions = {}): Promise<ReleaseCheckResult> {
    return this.ctx.http.request<ReleaseCheckResult>({
      method: "GET",
      path: "/sdk/releases/latest",
      query: {
        currentVersion: options.currentVersion ?? this.ctx.appVersion,
        platform: options.platform ?? this.ctx.platform,
      },
      retries: 2,
    });
  }
}
