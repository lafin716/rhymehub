package adapters

import (
	"encoding/json"
	"testing"
)

func TestGrantRule(t *testing.T) {
	cases := []struct {
		tool, input, want string
		ok                bool
	}{
		{"Bash", `{"command":"go test ./..."}`, "Bash(go test:*)", true},
		{"Bash", `{"command":"go build -o x ."}`, "Bash(go build:*)", true},
		{"Bash", `{"command":"make"}`, "Bash(make:*)", true},
		{"Bash", `{"command":"npm run test -- --watch=false"}`, "Bash(npm run:*)", true},
		{"Bash", `{"command":"./gradlew test"}`, "Bash(./gradlew test:*)", true},
		{"Bash", `{"command":"cd /tmp && ls"}`, "Bash(cd:*)", true},
		{"Bash", `{"command":"$(evil) x"}`, "", false},
		{"Bash", `{}`, "", false},
		{"Bash", ``, "", false},
		{"WebFetch", `{"url":"x"}`, "WebFetch", true},
		{"", `{}`, "", false},
	}
	for _, c := range cases {
		got, ok := GrantRule(c.tool, json.RawMessage(c.input))
		if got != c.want || ok != c.ok {
			t.Errorf("GrantRule(%q, %s) = %q,%v want %q,%v", c.tool, c.input, got, ok, c.want, c.ok)
		}
	}
}

func TestCheckGrantRejectsDerivedDangerousRules(t *testing.T) {
	for _, g := range []string{"WebFetch", "Bash", "Bash(curl:*)", "Bash(git push:*)", "Bash(sudo:*)"} {
		if CheckGrant(g) == nil {
			t.Errorf("CheckGrant(%q) = nil, want error", g)
		}
	}
	for _, g := range []string{"Bash(go test:*)", "Bash(make:*)"} {
		if err := CheckGrant(g); err != nil {
			t.Errorf("CheckGrant(%q) = %v", g, err)
		}
	}
}
