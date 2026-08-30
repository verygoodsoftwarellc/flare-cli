package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/verygoodsoftwarellc/flare-cli/internal/command"
)

func main() {
	name := filepath.Base(os.Args[0])
	if err := command.Execute(name); err != nil {
		fmt.Fprintln(os.Stderr, formatError(name, err))
		os.Exit(1)
	}
}

func formatError(name string, err error) string {
	return fmt.Sprintf("%s: %v", name, err)
}
