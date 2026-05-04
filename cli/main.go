package main

import (
	"fmt"
	"os"

	"github.com/latent-advisory/moorpost/cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
