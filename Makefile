.PHONY: check build run

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }
	go vet ./...
	go test -race ./...
	go build -o bin/v2v-bot ./cmd/v2v-bot

build:
	go build -o bin/v2v-bot ./cmd/v2v-bot

run: build
	./bin/v2v-bot
