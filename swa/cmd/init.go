package cmd

import (
	"database/sql"
	"io/ioutil"
	"log"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

func InitDB() {
	db, err := sql.Open("sqlite3", "audio_storage.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	schemaPath := filepath.Join("swa", "cmd", "schema.sql")
	schema, err := ioutil.ReadFile(schemaPath)
	if err != nil {
		log.Fatal(err)
	}

	_, err = db.Exec(string(schema))
	if err != nil {
		log.Fatal(err)
	}

}
