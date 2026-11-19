package adapters

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	grantCmdWordRe = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)
	grantSubcmdRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// CheckGrant reports whether a tool grant is acceptable for an agent session (not too broad, not
// hard-denied). The API uses it before storing a permission decision so a stored grant never makes the
// next session fail to start.
func CheckGrant(g string) error { return checkGrant(strings.TrimSpace(g)) }

// GrantRule turns one permission denial (tool name plus its input) into the narrowest grant rule.
// A Bash denial becomes a command-prefix rule such as "Bash(go test:*)" from the denied command's first
// word and, when it looks like a subcommand, its second word. Other tools are granted by name. ok is
// false when no safe rule can be derived (a bare Bash denial without a command, or shell syntax in the
// command word). The result must still pass CheckGrant.
func GrantRule(tool string, input json.RawMessage) (rule string, ok bool) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return "", false
	}
	if tool != "Bash" {
		return tool, true
	}
	var in struct {
		Command string `json:"command"`
	}
	if len(input) == 0 || json.Unmarshal(input, &in) != nil {
		return "", false
	}
	line, _, _ := strings.Cut(strings.TrimSpace(in.Command), "\n")
	words := strings.Fields(line)
	if len(words) == 0 || !grantCmdWordRe.MatchString(words[0]) {
		return "", false
	}
	prefix := words[0]
	if len(words) > 1 && grantSubcmdRe.MatchString(words[1]) {
		prefix += " " + words[1]
	}
	return "Bash(" + prefix + ":*)", true
}
