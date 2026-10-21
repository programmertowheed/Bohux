// Command bohux is the single Bohux binary. Modes (controlplane, agent, cli) are added
// slice by slice; see docs/plan/03-progress.md.
package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/bohumix/bohux/internal/version"
)

var errUsage = errors.New("usage: bohux <command>\n\ncommands:\n  version   print the version")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		slog.Error("bohux failed", "err", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "version":
		if _, err := fmt.Fprintln(stdout, version.Version); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}
