// Command rhymehub is the rhymehub server, worker and operator CLI.
// The version is injected at build time with -ldflags "-X main.version=...".
package main

import (
	"context"
	"os"

	"github.com/lafin716/rhymehub/apps/server/internal/cli"
)

var version = "0.1.0-dev"

func main() {
	code := cli.Run(context.Background(), cli.Env{
		Version: version,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}, os.Args[1:])
	os.Exit(code)
}
