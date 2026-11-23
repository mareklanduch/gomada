# Everyday commands. Run `make check` before opening a pull request; it is
# what CI runs.

.PHONY: build test cover lint fmt fuzz golden check

build:
	go build -o gomada ./cmd/gomada

test:
	go test -race ./...
	bash scripts/next-version_test.sh

# Tests with the coverage floor CI enforces.
cover:
	bash scripts/check-coverage.sh

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

# Fuzz the API response decoder for a short while.
fuzz:
	go test -run '^$$' -fuzz FuzzDecode -fuzztime 30s ./internal/omada/

# Rewrite the golden files after an intended change to output or screens.
# Review the diff like code.
golden:
	go test ./internal/cli/ ./internal/tui/ -update

check: lint cover
	bash scripts/next-version_test.sh
	go mod tidy -diff
	GOOS=windows go build ./...
	GOOS=darwin go build ./...
