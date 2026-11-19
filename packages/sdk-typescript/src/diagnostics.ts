import type { SdkDiagnostic } from "@rhymehub/shared";

export type ConsoleLevel = "log" | "info" | "warn" | "error" | "debug";

export interface CaptureConsoleOptions {
  /** Ring buffer size in lines. Default 200. */
  max?: number;
  /** Which console methods to wrap. Default: all five. */
  levels?: ConsoleLevel[];
}

export interface CaptureErrorsOptions {
  /** Ring buffer size in entries. Default 50. */
  max?: number;
}

/** Hard cap per entry and per uploaded diagnostic (keeps the newest content). */
export const MAX_ENTRY_CHARS = 4 * 1024;
export const MAX_DIAGNOSTIC_CHARS = 64 * 1024;

const ALL_LEVELS: ConsoleLevel[] = ["log", "info", "warn", "error", "debug"];

export class RingBuffer<T> {
  private items: T[] = [];
  private readonly capacity: number;

  constructor(capacity: number) {
    this.capacity = Math.max(1, Math.floor(capacity));
  }

  push(item: T): void {
    this.items.push(item);
    if (this.items.length > this.capacity) {
      this.items.splice(0, this.items.length - this.capacity);
    }
  }

  toArray(): T[] {
    return this.items.slice();
  }

  clear(): void {
    this.items = [];
  }

  get size(): number {
    return this.items.length;
  }
}

function formatValue(value: unknown): string {
  if (typeof value === "string") return value;
  if (value instanceof Error) return value.stack || `${value.name}: ${value.message}`;
  if (typeof value === "object" && value !== null) {
    try {
      return JSON.stringify(value) ?? String(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

function clip(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max)}…` : text;
}

/** Keep the most recent `max` characters (the end of a log is the interesting part). */
export function tailChars(text: string, max: number): string {
  return text.length > max ? text.slice(text.length - max) : text;
}

type ConsoleFn = (...args: unknown[]) => void;

/**
 * Opt-in capture of console output and uncaught errors into ring buffers.
 * Every capture returns a stop function that restores the original methods.
 */
export class DiagnosticsCollector {
  private lines = new RingBuffer<string>(200);
  private errors = new RingBuffer<string>(50);
  private consoleStop: (() => void) | null = null;
  private errorStop: (() => void) | null = null;

  /** Wrap console methods; originals keep being called. */
  captureConsole(options: CaptureConsoleOptions = {}): () => void {
    if (this.consoleStop) return this.consoleStop;
    this.lines = new RingBuffer<string>(options.max ?? 200);
    const target = globalThis.console as unknown as Record<ConsoleLevel, ConsoleFn>;
    const levels = options.levels ?? ALL_LEVELS;
    let active = true;
    const installed: Array<{ level: ConsoleLevel; original: ConsoleFn; wrapper: ConsoleFn }> = [];

    for (const level of levels) {
      const original = target[level];
      if (typeof original !== "function") continue;
      const wrapper: ConsoleFn = (...args: unknown[]) => {
        if (active) {
          const text = args.map(formatValue).join(" ");
          this.lines.push(clip(`${new Date().toISOString()} [${level}] ${text}`, MAX_ENTRY_CHARS));
        }
        original.apply(target, args);
      };
      target[level] = wrapper;
      installed.push({ level, original, wrapper });
    }

    const stop = () => {
      active = false;
      for (const { level, original, wrapper } of installed) {
        // Only restore when nobody wrapped on top of us; otherwise the inactive wrapper just forwards.
        if (target[level] === wrapper) target[level] = original;
      }
      this.consoleStop = null;
    };
    this.consoleStop = stop;
    return stop;
  }

  /** Record uncaught errors and unhandled promise rejections. */
  captureErrors(options: CaptureErrorsOptions = {}): () => void {
    if (this.errorStop) return this.errorStop;
    this.errors = new RingBuffer<string>(options.max ?? 50);
    const g = globalThis as unknown as {
      addEventListener?: (type: string, listener: (event: unknown) => void) => void;
      removeEventListener?: (type: string, listener: (event: unknown) => void) => void;
    };
    if (typeof g.addEventListener !== "function" || typeof g.removeEventListener !== "function") {
      return () => {};
    }

    const onError = (event: unknown) => {
      const e = event as { message?: string; filename?: string; lineno?: number; colno?: number; error?: unknown };
      const where = e.filename ? ` at ${e.filename}:${e.lineno ?? 0}:${e.colno ?? 0}` : "";
      const stack = e.error ? `\n${formatValue(e.error)}` : "";
      this.errors.push(clip(`${new Date().toISOString()} ${e.message ?? "error"}${where}${stack}`, MAX_ENTRY_CHARS));
    };
    const onRejection = (event: unknown) => {
      const reason = (event as { reason?: unknown }).reason;
      this.errors.push(clip(`${new Date().toISOString()} unhandledrejection: ${formatValue(reason)}`, MAX_ENTRY_CHARS));
    };

    g.addEventListener("error", onError);
    g.addEventListener("unhandledrejection", onRejection);
    const stop = () => {
      g.removeEventListener?.("error", onError);
      g.removeEventListener?.("unhandledrejection", onRejection);
      this.errorStop = null;
    };
    this.errorStop = stop;
    return stop;
  }

  /** Stop every active capture and clear buffers. */
  dispose(): void {
    this.consoleStop?.();
    this.errorStop?.();
    this.lines.clear();
    this.errors.clear();
  }

  /** Current buffers as SDK diagnostics. Empty when nothing is captured. */
  snapshot(): SdkDiagnostic[] {
    const out: SdkDiagnostic[] = [];
    const lines = this.lines.toArray();
    if (lines.length > 0) {
      out.push({ kind: "log", name: "console", content: tailChars(lines.join("\n"), MAX_DIAGNOSTIC_CHARS) });
    }
    const errors = this.errors.toArray();
    if (errors.length > 0) {
      out.push({ kind: "error", name: "runtime", content: tailChars(errors.join("\n\n"), MAX_DIAGNOSTIC_CHARS) });
    }
    return out;
  }

  get consoleActive(): boolean {
    return this.consoleStop !== null;
  }

  get errorsActive(): boolean {
    return this.errorStop !== null;
  }
}
