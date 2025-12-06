// swash-server - D-Bus session server (internal, launched by systemd)
package main

import (
	"fmt"
	"os"

	"github.com/mbrock/swash/internal/swash"
)

func main() {
	if err := swash.RunServer(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
