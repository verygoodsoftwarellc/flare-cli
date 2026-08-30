package main

import (
	"fmt"
	"os"

	"github.com/verygoodsoftwarellc/flare-cli/internal/command"
)

func main() {
	if err := command.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "flare:", err)
		os.Exit(1)
	}
}
