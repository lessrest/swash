package main

import (
	"fmt"

	"github.com/mbrock/swash/pkg/journalfile"
)

func main() {
	var machineID, bootID journalfile.ID128
	copy(machineID[:], []byte("0123456789abcdef"))
	copy(bootID[:], []byte("fedcba9876543210"))

	jf, err := journalfile.Create("/tmp/mini.journal", machineID, bootID)
	if err != nil {
		panic(err)
	}

	for i := range 5 {
		err = jf.AppendEntry(map[string]string{
			"MESSAGE":           fmt.Sprintf("Hello from Go journal writer #%d", i),
			"PRIORITY":          "6",
			"_PID":              "12345",
			"SYSLOG_IDENTIFIER": "mini-test",
		})
		if err != nil {
			panic(err)
		}
	}

	jf.Close()
	fmt.Println("Created /tmp/mini.journal")
}
