// Package main is the jj-trim entrypoint.
package main

import (
	"os"
)

// version is set via -ldflags at build time (see .goreleaser.yml).
var version = "dev"

func main() {
	os.Exit(Run(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
