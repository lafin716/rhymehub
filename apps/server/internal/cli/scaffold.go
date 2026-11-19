package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/lafin716/rhymehub/skills"
	"github.com/lafin716/rhymehub/templates"
)

// ScaffoldOptions are the values written into .rhymehub/project.json. None of them may carry a credential:
// scaffoldProject rejects token-shaped values and URLs with userinfo.
type ScaffoldOptions struct {
	Name              string
	ServerURL         string
	Stack             string
	DefaultBranch     string
	TestCmd           string
	ReleaseStrategy   string
	DefaultAgent      string
	UpdateChannel     string
	UpdateManifestURL string
}

// Relative paths (slash separated) of the generated files.
const (
	scaffoldProjectFile = ".rhymehub/project.json"
	scaffoldCIFile      = ".github/workflows/rhymehub.yml"
	scaffoldSDKFile     = ".rhymehub/sdk-install.md"
	scaffoldSkillsDir   = ".rhymehub/skills"
)

var projectTmpl = template.Must(template.New("project.json.tmpl").
	Funcs(template.FuncMap{"json": jsonString}).
	ParseFS(templates.FS, "project.json.tmpl"))

// jsonString quotes s as a JSON string so template values can never break the document.
func jsonString(s string) string {
	b, _ := json.Marshal(s) // marshalling a string cannot fail
	return string(b)
}

// scaffoldProject writes the RhymeHub files into dir: .rhymehub/project.json, the CI workflow, the SDK install
// guide and the skills. Existing files are never overwritten; they are reported in skipped. dir must be an
// existing absolute directory. created and skipped are relative slash paths.
func scaffoldProject(dir string, opts ScaffoldOptions) (created []string, skipped []string, err error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, nil, fmt.Errorf("scaffold: project dir must be absolute: %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: project dir: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("scaffold: project path is not a directory: %s", dir)
	}
	opts, err = normalizeScaffoldOptions(opts)
	if err != nil {
		return nil, nil, err
	}
	projectJSON, err := renderProjectJSON(opts)
	if err != nil {
		return nil, nil, err
	}
	ci, err := templates.FS.ReadFile("ci/github-actions.yml")
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: read ci template: %w", err)
	}
	sdk, err := templates.FS.ReadFile("sdk-install.md")
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: read sdk guide: %w", err)
	}
	type generated struct {
		rel  string
		data []byte
	}
	files := []generated{
		{scaffoldProjectFile, projectJSON},
		{scaffoldCIFile, ci},
		{scaffoldSDKFile, sdk},
	}
	entries, err := fs.ReadDir(skills.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: list skills: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".md" {
			continue
		}
		data, err := skills.FS.ReadFile(e.Name())
		if err != nil {
			return created, skipped, fmt.Errorf("scaffold: read skill %s: %w", e.Name(), err)
		}
		files = append(files, generated{scaffoldSkillsDir + "/" + e.Name(), data})
	}
	for _, f := range files {
		wrote, err := writeNewFile(dir, f.rel, f.data)
		if err != nil {
			return created, skipped, err
		}
		if wrote {
			created = append(created, f.rel)
		} else {
			skipped = append(skipped, f.rel)
		}
	}
	return created, skipped, nil
}

// normalizeScaffoldOptions fills defaults and rejects values that could leak credentials into project files.
func normalizeScaffoldOptions(opts ScaffoldOptions) (ScaffoldOptions, error) {
	if opts.DefaultBranch == "" {
		opts.DefaultBranch = "main"
	}
	if opts.ReleaseStrategy == "" {
		opts.ReleaseStrategy = "none"
	}
	if opts.DefaultAgent == "" {
		opts.DefaultAgent = "claude"
	}
	if opts.UpdateChannel == "" {
		opts.UpdateChannel = "stable"
	}
	if opts.ServerURL != "" {
		u, err := url.Parse(opts.ServerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return opts, fmt.Errorf("scaffold: server url must be an http(s) URL")
		}
		if u.User != nil {
			return opts, fmt.Errorf("scaffold: server url must not contain credentials")
		}
	}
	for _, v := range []string{opts.Name, opts.ServerURL, opts.Stack, opts.DefaultBranch, opts.TestCmd,
		opts.ReleaseStrategy, opts.DefaultAgent, opts.UpdateChannel, opts.UpdateManifestURL} {
		if containsTokenShape(v) {
			return opts, fmt.Errorf("scaffold: refusing to write a value that looks like a credential")
		}
	}
	return opts, nil
}

// containsTokenShape detects RhymeHub API token and SDK key prefixes.
func containsTokenShape(s string) bool {
	return strings.Contains(s, "rht_") || strings.Contains(s, "rhk_")
}

func renderProjectJSON(opts ScaffoldOptions) ([]byte, error) {
	var buf bytes.Buffer
	if err := projectTmpl.Execute(&buf, opts); err != nil {
		return nil, fmt.Errorf("scaffold: render project.json: %w", err)
	}
	if !json.Valid(buf.Bytes()) {
		return nil, fmt.Errorf("scaffold: rendered project.json is not valid JSON")
	}
	return buf.Bytes(), nil
}

// writeNewFile creates root/rel with O_EXCL so an existing file (or symlink) is never replaced. It reports
// false when the file already existed.
func writeNewFile(root, rel string, data []byte) (bool, error) {
	target := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, fmt.Errorf("scaffold: create directory for %s: %w", rel, err)
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("scaffold: create %s: %w", rel, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return true, fmt.Errorf("scaffold: write %s: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		return true, fmt.Errorf("scaffold: close %s: %w", rel, err)
	}
	return true, nil
}
