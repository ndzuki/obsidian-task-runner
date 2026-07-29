package main

import (
	"fmt"
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/cli"
)

// Version and Commit are set at build time via -ldflags.
var Version string
var Commit string

func main() {
	if err := cli.Execute(Version, Commit); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
