.PHONY: server run

server:
	go build -o $@ main.go

run: server
	PORT=3002 ./server
