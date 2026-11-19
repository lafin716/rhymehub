import { describe, expect, it } from "vitest";
import { redact } from "./redact";

describe("redact", () => {
  it("leaves ordinary text untouched and counts nothing", () => {
    const input = "Bluetooth reconnect should retry 3 times after 2026-11-19 on Pixel 8";
    expect(redact(input)).toEqual({ text: input, redactions: 0 });
  });

  it("redacts email addresses", () => {
    const r = redact("contact me at jane.doe+test@example.co.kr or a@b.com");
    expect(r.text).toBe("contact me at [REDACTED:email] or [REDACTED:email]");
    expect(r.redactions).toBe(2);
  });

  it("redacts JWTs and bearer tokens", () => {
    const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U";
    const r = redact(`token ${jwt} and Authorization: Bearer abcdefghijklmnop`);
    expect(r.text).not.toContain(jwt);
    expect(r.text).toContain("[REDACTED:jwt]");
    expect(r.text).toContain("Bearer [REDACTED:token]");
    expect(r.redactions).toBeGreaterThanOrEqual(2);
  });

  it("redacts AWS, GitHub, Slack, OpenAI, Anthropic and rhymehub keys", () => {
    const text = [
      "AKIAIOSFODNN7EXAMPLE",
      "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
      "github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
      "xoxb-1234567890-abcdefghijk",
      "sk-proj-abcdefghijklmnopqrstuvwxyz",
      "sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
      "rhk_droidpad_0123456789abcdef",
    ].join(" | ");
    const r = redact(text);
    expect(r.text).toBe(
      [
        "[REDACTED:aws-key]",
        "[REDACTED:github-token]",
        "[REDACTED:github-token]",
        "[REDACTED:slack-token]",
        "[REDACTED:openai-key]",
        "[REDACTED:anthropic-key]",
        "[REDACTED:rhymehub-key]",
      ].join(" | "),
    );
    expect(r.redactions).toBe(7);
  });

  it("redacts private key blocks", () => {
    const pem = "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj\n-----END RSA PRIVATE KEY-----";
    expect(redact(`key:\n${pem}\ndone`)).toEqual({ text: "key:\n[REDACTED:private-key]\ndone", redactions: 1 });
  });

  it("redacts password=, secret= and token= values but keeps the name", () => {
    const r = redact("password=hunter2&secret: 'abc def' token=xyz");
    expect(r.text).toBe("password=[REDACTED:secret]&secret: [REDACTED:secret] token=[REDACTED:secret]");
    expect(r.redactions).toBe(3);
  });

  it("redacts JSON style secret fields", () => {
    const r = redact('{"password": "hunter2", "name": "x"}');
    expect(r.text).toBe('{"password": [REDACTED:secret], "name": "x"}');
  });

  it("redacts cookie headers and rh_session pairs", () => {
    const r = redact("Cookie: rh_session=abc123; theme=dark\nrh_session=zzz");
    expect(r.text).toBe("Cookie: [REDACTED:cookie]\nrh_session=[REDACTED:cookie]");
  });

  it("redacts Luhn-valid card numbers but not arbitrary digit runs", () => {
    const card = redact("card 4111 1111 1111 1111 done");
    expect(card.text).toBe("card [REDACTED:card] done");
    // Same length, fails Luhn: must not be treated as a card.
    const notCard = redact("order 4111 1111 1111 1112 done");
    expect(notCard.text).not.toContain("[REDACTED:card]");
  });

  it("redacts phone numbers", () => {
    const r = redact("call +82 10-1234-5678 now");
    expect(r.text).toBe("call [REDACTED:phone] now");
    expect(r.redactions).toBe(1);
  });

  it("does not redact short numbers such as versions or counts", () => {
    const input = "v1.4.2 build 42, 3 retries, 2026-11-19T10:00:00Z";
    expect(redact(input).text).toBe(input);
  });
});
