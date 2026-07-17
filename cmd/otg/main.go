package main

import "github.com/ndzuki/obsidian-task-runner/internal/cli"

// Version is set at build time via -ldflags "-X main.Version=...".
var Version string

func main() {
	cli.Execute(Version)
}
