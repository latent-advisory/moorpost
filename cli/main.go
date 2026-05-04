package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/latent-advisory/moorpost/cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		// ErrConflictsPresent: the table printed by `moorpost conflicts`
		// is the message. Don't double-print to stderr; just exit 1.
		if !errors.Is(err, cmd.ErrConflictsPresent) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
