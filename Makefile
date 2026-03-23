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
	go test -count=1 -v ./...

.PHONY: test-race
test-race:
	CGO_ENABLED=1 go test -count=1 -race -v ./...

.PHONY: test-integration
test-integration:
	go test -count=1 -v ./e2e/...

.PHONY: format fmt
format fmt:
	golangci-lint fmt

.PHONY: clean
clean:
	go clean
	rm -f muxtail
