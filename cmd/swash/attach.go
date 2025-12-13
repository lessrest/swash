package main

import "github.com/mbrock/swash/internal/attach"

func cmdAttach(sessionID string) {
	initBackend()
	defer bk.Close()

	session, err := attach.New(sessionID, bk.ConnectTTYSession)
	if err != nil {
		fatal("%v", err)
	}
	defer session.Close()

	result := session.Run()
	result.PrintResult()
}
