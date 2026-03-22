.PHONY: build vet lint test test-race test-integration test-all clean

build:
	go build ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

test:
	go test -v .

test-race:
	CGO_ENABLED=1 go test -race -v .

test-integration:
	go test ./integration/...

test-all: test-race test-integration

clean:
	go clean
	rm -f muxtail
