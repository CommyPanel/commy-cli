// Command commy controls a CommyPanel server from your own machine or from CI, through the panel's API.
package main

import (
	"os"

	"github.com/commypanel/commy-cli/internal/cli"
)

// version is set when the binary is built (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Environment{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Getenv:  os.Getenv,
		Version: version,
	}))
}
