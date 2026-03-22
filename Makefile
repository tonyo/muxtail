.PHONY: build test test-race test-integration test-all clean

build:
	go build ./...

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
