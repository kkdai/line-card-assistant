BINARY_NAME := line-card-assistant
CMD_PKG     := ./cmd/server

.PHONY: all build run test fmt clean

all: fmt test build

build:
	go build -o bin/$(BINARY_NAME) $(CMD_PKG)

run:
	go run $(CMD_PKG)

test:
	go test -v ./...

fmt:
	gofmt -s -w .

clean:
	rm -rf bin/
