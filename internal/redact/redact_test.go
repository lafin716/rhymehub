package redact

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  string
		count int
	}{
		{"anthropic key", "key sk-ant-api03-abcdefghijklmnopqrstuvwx end", "key [REDACTED] end", 1},
		{"openai key", "use sk-proj-abcdefghijklmnopqrstuv", "use [REDACTED]", 1},
		{"github pat", "token ghp_abcdefghijklmnopqrstuvwxyz0123", "token [REDACTED]", 1},
		{"github fine-grained", "github_pat_11ABCDEFG0123456789_abcdef", "[REDACTED]", 1},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE", "[REDACTED]", 1},
		{"slack", "xoxb-1234567890-abcdefghij", "[REDACTED]", 1},
		{"rhymehub key", "rhk_abcdefghijklmnopqr", "[REDACTED]", 1},
		{"jwt", "eyJhbGciOi.eyJzdWIiOi.abcdefSIG", "[REDACTED]", 1},
		{"private key block", "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----", "[REDACTED]", 1},
		{"keyed password keeps name", "password=hunter2 ok", "password=[REDACTED] ok", 1},
		{"keyed quoted", `api_key: "abc123xyz"`, `api_key: [REDACTED]`, 1},
		{"bearer", "Authorization: Bearer abcdefgh12345678", "Authorization: Bearer [REDACTED]", 1},
		{"cookie line", "Cookie: session=abc; other=1", "Cookie: [REDACTED]", 1},
		{"email", "mail me at jane.doe@example.com please", "mail me at [REDACTED] please", 1},
		{"valid card", "card 4111 1111 1111 1111 here", "card [REDACTED] here", 1},
		{"valid card dashes", "4111-1111-1111-1111", "[REDACTED]", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := Redact(tc.in)
			if !strings.Contains(got, tc.want) && got != tc.want {
				t.Fatalf("Redact(%q) = %q, want contains %q", tc.in, got, tc.want)
			}
			if n != tc.count {
				t.Fatalf("count = %d, want %d (out %q)", n, tc.count, got)
			}
		})
	}
}

func TestRedactNoFalsePositives(t *testing.T) {
	inputs := []string{
		"plain prose with no secrets at all",
		"version 1.22.3 released on 2026-11-19",
		"func main() { fmt.Println(\"hello\") }",
		"the password field is required",
		"order 12345 total 99",
		"set token count to zero",
		"pwd is /Users/lafin/workspace",
		"sk-short",
	}
	for _, in := range inputs {
		got, n := Redact(in)
		if n != 0 || got != in {
			t.Errorf("Redact(%q) = %q (%d), want unchanged", in, got, n)
		}
	}
}

func TestLuhn(t *testing.T) {
	if !luhnValid("4111111111111111") {
		t.Error("expected valid test card to pass Luhn")
	}
	if luhnValid("4111111111111112") {
		t.Error("expected invalid card to fail Luhn")
	}
	if luhnValid("12a4") {
		t.Error("non-digit input must fail")
	}
}

func TestIsDeniedEnv(t *testing.T) {
	cases := map[string]bool{
		"GH_TOKEN":          true,
		"github_token":      true,
		"GITHUB_TOKEN":      true,
		"GIT_ASKPASS":       true,
		"MY_SECRET":         true,
		"DB_PASSWORD":       true,
		"AWS_ACCESS_KEY_ID": true,
		"AWS_REGION":        true,
		"PATH":              false,
		"HOME":              false,
		"EDITOR":            false,
	}
	for name, want := range cases {
		if got := IsDeniedEnv(name); got != want {
			t.Errorf("IsDeniedEnv(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestScrubEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/Users/x",
		"GH_TOKEN=ghp_secret",
		"AWS_SECRET_ACCESS_KEY=abc",
		"RANDOM_VAR=1",
		"CUSTOM_OK=yes",
		"GIT_ASKPASS=/bin/askpass",
		"malformed",
		"=novalue",
		"DENIED_TOKEN=t",
	}
	got := ScrubEnv(env, []string{"CUSTOM_OK", "GH_TOKEN", "DENIED_TOKEN"})
	want := []string{"PATH=/usr/bin", "HOME=/Users/x", "CUSTOM_OK=yes"}
	if len(got) != len(want) {
		t.Fatalf("ScrubEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ScrubEnv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScrubEnvDenylistWinsOverExtraAllow(t *testing.T) {
	got := ScrubEnv([]string{"GITHUB_TOKEN=x"}, []string{"GITHUB_TOKEN"})
	if len(got) != 0 {
		t.Fatalf("credential re-enabled via extraAllow: %v", got)
	}
}
