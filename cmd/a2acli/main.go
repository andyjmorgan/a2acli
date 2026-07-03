// a2acli is a generic, scriptable command-line client for A2A agents.
package main

import (
	"os"

	"github.com/andyjmorgan/a2acli/internal/cli"
)

// version is stamped by goreleaser / -ldflags at build time.
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
