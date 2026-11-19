package workspace

import (
	"strings"
	"testing"
)

func TestGitEnvScrubsCredentials(t *testing.T) {
	t.Setenv("RHYMEHUB_TEST_TOKEN", "s3cret")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	env := gitEnv()
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"RHYMEHUB_TEST_TOKEN", "AWS_ACCESS_KEY_ID"} {
		if strings.Contains(joined, bad+"=") {
			t.Fatalf("%s leaked into git env", bad)
		}
	}
	for _, want := range []string{"SSH_AUTH_SOCK=/tmp/agent.sock", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
