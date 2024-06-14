package main

import (
	"fmt"
	"os"

	"swa/cmd"

	"github.com/spf13/cobra"
)

func main() {
	var rootCmd = &cobra.Command{Use: "app"}
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(googleCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the server",
	Run: func(x *cobra.Command, args []string) {
		cmd.Serve()
	},
}

var googleCmd = &cobra.Command{
	Use:   "google",
	Short: "Interact with Google APIs",
	Run: func(x *cobra.Command, args []string) {
		cmd.Goog()
	},
}
