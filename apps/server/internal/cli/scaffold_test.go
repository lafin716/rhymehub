package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestScaffoldCreatesThenSkipsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	opts := ScaffoldOptions{Name: "demo", ServerURL: "https://rhymehub.example.ts.net:7780", Stack: "go", TestCmd: "go test ./..."}

	created, skipped, err := scaffoldProject(dir, opts)
	if err != nil {
		t.Fatalf("first scaffold: %v", err)
	}
	if len(skipped) != 0 {
		t.Fatalf("first run skipped %v", skipped)
	}
	for _, want := range []string{scaffoldProjectFile, scaffoldCIFile, scaffoldSDKFile, scaffoldSkillsDir + "/code-review.md"} {
		if !slices.Contains(created, want) {
			t.Fatalf("created %v does not contain %s", created, want)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Fatalf("missing %s: %v", want, err)
		}
	}

	// Second run must not overwrite anything: every file is skipped.
	sentinel := filepath.Join(dir, filepath.FromSlash(scaffoldProjectFile))
	if err := os.WriteFile(sentinel, []byte(`{"name":"mine"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	created2, skipped2, err := scaffoldProject(dir, opts)
	if err != nil {
		t.Fatalf("second scaffold: %v", err)
	}
	if len(created2) != 0 {
		t.Fatalf("second run created %v", created2)
	}
	if len(skipped2) != len(created) {
		t.Fatalf("second run skipped %d files, want %d", len(skipped2), len(created))
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"name":"mine"}` {
		t.Fatalf("existing project.json was overwritten: %s", got)
	}
}

func TestScaffoldProjectJSONParses(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := scaffoldProject(dir, ScaffoldOptions{Name: `quote "me"`, ServerURL: "http://100.64.0.5:7780", Stack: "node"}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(scaffoldProjectFile)))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("project.json is not valid JSON: %v\n%s", err, raw)
	}
	if doc["name"] != `quote "me"` {
		t.Fatalf("name = %v", doc["name"])
	}
	if doc["server"] != "http://100.64.0.5:7780" {
		t.Fatalf("server = %v", doc["server"])
	}
	if doc["stack"] != "node" {
		t.Fatalf("stack = %v", doc["stack"])
	}
}

// TestScaffoldOutputsHaveNoTokens walks every generated file and fails on anything that looks like a real
// RhymeHub API token or SDK key.
func TestScaffoldOutputsHaveNoTokens(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := scaffoldProject(dir, ScaffoldOptions{Name: "demo", ServerURL: "https://host.ts.net"}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	tokenLike := regexp.MustCompile(`rht_[A-Za-z0-9_-]{16,}|rhk_[A-Za-z0-9_-]{16,}`)
	count := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		count++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := tokenLike.Find(b); m != nil {
			t.Fatalf("%s contains token-like value %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("no files were generated")
	}
}

func TestScaffoldRejectsCredentialsAndBadInput(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		dir  string
		opts ScaffoldOptions
	}{
		"relative dir":       {"relative/path", ScaffoldOptions{Name: "x"}},
		"url with userinfo":  {dir, ScaffoldOptions{Name: "x", ServerURL: "https://user:pw@host.ts.net"}},
		"non-http url":       {dir, ScaffoldOptions{Name: "x", ServerURL: "ftp://host"}},
		"token in name":      {dir, ScaffoldOptions{Name: "rht_abcdefghijklmnop"}},
		"sdk key in testCmd": {dir, ScaffoldOptions{Name: "x", TestCmd: "echo rhk_abcdefghijklmnop"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := scaffoldProject(tc.dir, tc.opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	// A rejected call must not leave a partial project behind.
	if _, err := os.Stat(filepath.Join(dir, ".rhymehub")); err == nil {
		t.Fatal("rejected scaffold wrote files")
	}
}

func TestScaffoldUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := scaffoldProject(dir, ScaffoldOptions{Name: "d"}); err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(scaffoldProjectFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"defaultBranch": "main"`) && !strings.Contains(string(raw), `"defaultBranch":"main"`) {
		t.Fatalf("defaultBranch default missing:\n%s", raw)
	}
}
