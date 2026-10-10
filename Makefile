.PHONY: test fmt vet build check

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

build:
	go build ./cmd/worker

check: fmt test vet build
