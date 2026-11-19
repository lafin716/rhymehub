// Client-side mirror of the server redaction (docs/SECURITY.md control 13).
// The server remains authoritative; this is defence in depth so secrets are
// scrubbed before they leave the app. Pure TypeScript, no dependencies.

export interface RedactResult {
  text: string;
  /** Number of redacted items (matches the server's `requests.redactions` semantics). */
  redactions: number;
}

/** Returns null to leave the match untouched. */
type Replacer = (match: string, ...groups: (string | undefined)[]) => string | null;

interface Rule {
  name: string;
  pattern: RegExp;
  replace: Replacer;
}

const constant = (label: string): Replacer => () => label;

function luhnValid(digits: string): boolean {
  let sum = 0;
  let alt = false;
  for (let i = digits.length - 1; i >= 0; i--) {
    let n = digits.charCodeAt(i) - 48;
    if (alt) {
      n *= 2;
      if (n > 9) n -= 9;
    }
    sum += n;
    alt = !alt;
  }
  return sum % 10 === 0;
}

const RULES: Rule[] = [
  {
    name: "private-key",
    pattern: /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----/g,
    replace: constant("[REDACTED:private-key]"),
  },
  {
    // Cookie / Set-Cookie header lines: keep the header name, drop the value.
    name: "cookie-header",
    pattern: /\b(?:set-)?cookie\s*:[^\r\n]*/gi,
    replace: (m) => `${m.slice(0, m.indexOf(":") + 1)} [REDACTED:cookie]`,
  },
  {
    name: "cookie-pair",
    pattern: /\b(rh_session|sessionid|session|sid)=[^\s;&,]+/gi,
    replace: (_m, name) => `${name}=[REDACTED:cookie]`,
  },
  {
    name: "jwt",
    pattern: /\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}/g,
    replace: constant("[REDACTED:jwt]"),
  },
  {
    name: "bearer",
    pattern: /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi,
    replace: constant("Bearer [REDACTED:token]"),
  },
  {
    name: "anthropic-key",
    pattern: /\bsk-ant-[A-Za-z0-9_-]{16,}/g,
    replace: constant("[REDACTED:anthropic-key]"),
  },
  {
    name: "openai-key",
    pattern: /\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}/g,
    replace: constant("[REDACTED:openai-key]"),
  },
  {
    name: "github-token",
    pattern: /\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})/g,
    replace: constant("[REDACTED:github-token]"),
  },
  {
    name: "slack-token",
    pattern: /\bxox[abposr]-[A-Za-z0-9-]{10,}/g,
    replace: constant("[REDACTED:slack-token]"),
  },
  {
    name: "aws-key",
    pattern: /\b(?:AKIA|ASIA)[0-9A-Z]{16}\b/g,
    replace: constant("[REDACTED:aws-key]"),
  },
  {
    // rhymehub's own keys: reporter keys (rhk_) and API tokens (rht_).
    name: "rhymehub-key",
    pattern: /\brh[kt]_[A-Za-z0-9_-]{16,}/g,
    replace: constant("[REDACTED:rhymehub-key]"),
  },
  {
    // password=/secret=/token= style assignments, including JSON "key": "value".
    name: "secret-assignment",
    pattern:
      /(["']?\b(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s&,;"'}]+)/gi,
    replace: (_m, prefix) => `${prefix}[REDACTED:secret]`,
  },
  {
    name: "email",
    pattern: /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/g,
    replace: constant("[REDACTED:email]"),
  },
  {
    name: "credit-card",
    pattern: /(?<!\d)(?:\d[ -]?){12,18}\d(?!\d)/g,
    replace: (m) => {
      const digits = m.replace(/\D/g, "");
      return digits.length >= 13 && digits.length <= 19 && luhnValid(digits) ? "[REDACTED:card]" : null;
    },
  },
  {
    name: "phone",
    pattern: /(?<![\w.])\+?\d[\d\s().-]{6,}\d(?!\w)/g,
    replace: (m) => {
      const digits = m.replace(/\D/g, "");
      return digits.length >= 10 && digits.length <= 15 ? "[REDACTED:phone]" : null;
    },
  },
];

/**
 * Redact secrets and personal data from free text. Rules run in a fixed order
 * (see RULES) so that, e.g., card numbers are classified before phone numbers.
 */
export function redact(input: string): RedactResult {
  let redactions = 0;
  let text = input;
  for (const rule of RULES) {
    text = text.replace(rule.pattern, (...args: unknown[]) => {
      // String.replace passes: match, ...captureGroups, offset, input, [namedGroups].
      const match = args[0] as string;
      const last = args[args.length - 1];
      const hasNamedGroups = typeof last === "object" && last !== null;
      const end = args.length - (hasNamedGroups ? 3 : 2);
      const groups = args.slice(1, end).map((g) => (typeof g === "string" ? g : undefined));
      const out = rule.replace(match, ...groups);
      if (out === null) return match;
      redactions++;
      return out;
    });
  }
  return { text, redactions };
}

/** Names of the redaction rules, in execution order. Useful for docs and tests. */
export const REDACTION_RULE_NAMES: readonly string[] = RULES.map((r) => r.name);
