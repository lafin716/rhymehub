// Package skills embeds the agent procedure documents that `rhymehub init` copies into a project under
// .rhymehub/skills/. Skills are markdown instructions for agents. They are not execution engines and contain
// no commands that rhymehub runs on its own.
package skills

import "embed"

// FS holds every skill document at the top level of this directory.
//
//go:embed *.md
var FS embed.FS
