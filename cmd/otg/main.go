package main

import (
	"fmt"
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/cli"
)

// Version is set at build time via -ldflags "-X main.Version=...".
var Version string

func main() {
	if err := cli.Execute(Version); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
