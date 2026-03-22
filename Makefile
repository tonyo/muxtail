.PHONY: build
build:
	go build ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test -v .

.PHONY: test-race
test-race:
	CGO_ENABLED=1 go test -race -v .

.PHONY: test-integration
test-integration:
	go test ./integration/...

.PHONY: test-all
test-all: test-race test-integration

.PHONY: format fmt
format fmt:
	golangci-lint fmt

.PHONY: clean
clean:
	go clean
	rm -f muxtail
