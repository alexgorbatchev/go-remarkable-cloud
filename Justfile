set dotenv-load := false

# Run unit tests
test:
    go test -v ./...

# Run linter
lint:
    golangci-lint run

# Run linter and test suite
check: lint test
