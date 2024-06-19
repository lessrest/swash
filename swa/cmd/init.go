package cmd

import (
	"database/sql"
	_ "embed"

	"path/filepath"

	"github.com/charmbracelet/log"

	"github.com/adrg/xdg"
	_ "github.com/mattn/go-sqlite3"
)

var (
	//go:embed schema.sql
	schema string
)

func InitDB() {
	dbPath := filepath.Join(xdg.DataHome, "swash.db")
	log.Info("initializing database", "path", dbPath)

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	log.Info("creating tables")
	_, err = db.Exec(schema)
	if err != nil {
		log.Fatal(err)
	}
}
