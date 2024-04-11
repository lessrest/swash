server:
	go build -o $@ main.go

run: server
	PORT=300$(notdir $(CURDIR)) ./server