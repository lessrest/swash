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
	rootCmd.AddCommand(initCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the SQLite database",
	Run: func(x *cobra.Command, args []string) {
		cmd.InitDB()
	},
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
