// Copyright (c) Axiom Studio AI (axiomstudio.ai)

package main

import (
	"fmt"
	"os"

	"vibeflow-cli/internal/vibeflowcli"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	vibeflowcli.SetVersionInfo(version, commit, date)
	if err := vibeflowcli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
