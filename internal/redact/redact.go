// Package redact removes secrets and personal data from diagnostic text (docs/SECURITY.md control 13) and
// scrubs environment variables before an agent process is started (control 5).
package redact

import (
	"regexp"
	"strconv"
	"strings"
)

// Placeholder replaces every redacted span.
const Placeholder = "[REDACTED]"

type rule struct {
	name string
	re   *regexp.Regexp
	// repl, when non-nil, builds the replacement from the match submatches (keeps the key name visible).
	repl func(m []string) string
}

var rules = []rule{
	{name: "private_key", re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)},
	{name: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)},
	{name: "aws_key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "github_token", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)},
	{name: "slack_token", re: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`)},
	{name: "anthropic_key", re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{name: "openai_key", re: regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{name: "rhymehub_key", re: regexp.MustCompile(`\brh[kt]_[A-Za-z0-9_-]{16,}`)},
	{
		name: "keyed_secret",
		re:   regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|client[_-]?secret)(\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&"'}]+)`),
		repl: func(m []string) string { return m[1] + m[2] + Placeholder },
	},
	{
		name: "bearer",
		re:   regexp.MustCompile(`(?i)\b(bearer)(\s+)[A-Za-z0-9._~+/=-]{8,}`),
		repl: func(m []string) string { return m[1] + m[2] + Placeholder },
	},
	{
		name: "cookie",
		re:   regexp.MustCompile(`(?im)^(\s*(?:set-)?cookie\s*:\s*)[^\r\n]+`),
		repl: func(m []string) string { return m[1] + Placeholder },
	},
	{name: "email", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)},
	// card runs before phone so grouped card numbers reach the Luhn check intact.
	{name: "card", re: regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`), repl: cardRepl},
	{name: "phone", re: regexp.MustCompile(`(?:\+\d{1,3}[\s.-]?)?\(?\d{2,4}\)?[\s.-]\d{3,4}[\s.-]\d{4}\b`)},
}

// cardRepl redacts a digit run only when it is 13 to 19 digits and passes the Luhn checksum.
func cardRepl(m []string) string {
	digits := strings.NewReplacer(" ", "", "-", "").Replace(m[0])
	if len(digits) < 13 || len(digits) > 19 || !luhnValid(digits) {
		return m[0]
	}
	return Placeholder
}

func luhnValid(s string) bool {
	sum := 0
	double := false
	for i := len(s) - 1; i >= 0; i-- {
		d, err := strconv.Atoi(string(s[i]))
		if err != nil {
			return false
		}
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// Redact replaces secrets and personal data in s and returns the result with the number of spans replaced.
// Plain prose, code identifiers, dates, versions and short numbers are left untouched.
func Redact(s string) (string, int) {
	total := 0
	for _, r := range rules {
		var n int
		s = replaceAll(r, s, &n)
		total += n
	}
	return s, total
}

func replaceAll(r rule, s string, count *int) string {
	idx := r.re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		match := make([]string, len(loc)/2)
		for i := range match {
			if loc[2*i] >= 0 {
				match[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		var repl string
		switch {
		case r.repl != nil:
			repl = r.repl(match)
		default:
			repl = Placeholder
		}
		if repl == match[0] {
			continue // not a secret (e.g. failed Luhn check)
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(repl)
		last = loc[1]
		*count++
	}
	b.WriteString(s[last:])
	return b.String()
}

// allowedEnv is the scrub allowlist from SECURITY.md control 5.
var allowedEnv = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true,
	"LC_CTYPE": true, "TERM": true, "TMPDIR": true, "SHELL": true,
	"CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true,
}

// deniedEnvNames are always removed, even when listed in extraAllow.
var deniedEnvNames = map[string]bool{
	"GIT_ASKPASS": true, "GH_TOKEN": true, "GITHUB_TOKEN": true,
}

// deniedEnvSuffixes and deniedEnvPrefixes remove credential-shaped variables.
var (
	deniedEnvSuffixes = []string{"_TOKEN", "_SECRET", "_PASSWORD"}
	deniedEnvPrefixes = []string{"AWS_"}
)

// IsDeniedEnv reports whether a variable name is a credential that must never reach an agent.
func IsDeniedEnv(name string) bool {
	upper := strings.ToUpper(name)
	if deniedEnvNames[upper] {
		return true
	}
	for _, p := range deniedEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	for _, s := range deniedEnvSuffixes {
		if strings.HasSuffix(upper, s) {
			return true
		}
	}
	return false
}

// ScrubEnv filters a KEY=VALUE environment list. A variable is kept only if it is on the allowlist (or in
// extraAllow) and is not on the denylist. The denylist always wins, so extraAllow cannot re-enable a
// credential. Input order is preserved; malformed entries (no "=") are dropped.
func ScrubEnv(environ []string, extraAllow []string) []string {
	extra := make(map[string]bool, len(extraAllow))
	for _, n := range extraAllow {
		extra[n] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		if IsDeniedEnv(name) {
			continue
		}
		if allowedEnv[name] || extra[name] {
			out = append(out, kv)
		}
	}
	return out
}
