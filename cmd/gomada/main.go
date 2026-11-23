// Command gomada manages TP-Link Omada controllers from the terminal.
package main

import (
	"os"

	"github.com/mareklanduch/gomada/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
