// Package templates embeds the files that `rhymehub init` writes into a project: the CI workflow, the
// project.json template and the SDK install guide. Templates are rendered or copied by apps/server/internal/cli.
package templates

import "embed"

// FS holds the template files. Paths are relative to this directory (for example "ci/github-actions.yml").
//
//go:embed ci/github-actions.yml project.json.tmpl sdk-install.md
var FS embed.FS
