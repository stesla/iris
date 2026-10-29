package main

import (
	"os"

	"github.com/stesla/iris/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
