package cmd

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

var ListStreamsCmd = &cobra.Command{
	Use:   "list-streams",
	Short: "List the streams in the database",
	Run: func(cmd *cobra.Command, args []string) {
		dbPath := filepath.Join(xdg.DataHome, "swash.db")
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()

		rows, err := db.Query("SELECT id, start_time, end_time FROM opus_streams")
		if err != nil {
			log.Fatal(err)
		}
		defer rows.Close()

		for rows.Next() {
			var id int
			var startTime, endTime time.Time
			err := rows.Scan(&id, &startTime, &endTime)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Stream %d: %s - %s\n", id, startTime, endTime)
		}
	},
}
