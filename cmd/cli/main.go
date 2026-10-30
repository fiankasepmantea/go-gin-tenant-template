package main

import (
	"fmt"
	"os"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
