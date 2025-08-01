# Z3 ZFS Backup Tool - Go Port
# Makefile for build automation

.PHONY: all build test clean install deps lint format

# Build configuration
BINARY_NAME=z3
BUILD_DIR=bin
GO_VERSION=1.24.4
LDFLAGS=-ldflags "-X main.version=$(shell git describe --tags --always --dirty)"

# Default target
all: deps format lint test build

# Install dependencies
deps:
	go mod download
	go mod tidy

# Build all binaries
build: build-z3 build-pput build-z3get build-z3sync

build-z3:
	go build $(LDFLAGS) -o $(BUILD_DIR)/z3 ./cmd/z3

build-pput:
	go build $(LDFLAGS) -o $(BUILD_DIR)/pput ./cmd/pput

build-z3get:
	go build $(LDFLAGS) -o $(BUILD_DIR)/z3get ./cmd/z3get

build-z3sync:
	go build $(LDFLAGS) -o $(BUILD_DIR)/z3sync ./cmd/z3sync

# Run tests
test:
	go test -v ./...

test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Benchmarks
bench:
	go test -bench=. -benchmem ./...

# Format code
format:
	go fmt ./...

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Install binaries to GOPATH/bin
install: build
	go install $(LDFLAGS) ./cmd/z3
	go install $(LDFLAGS) ./cmd/pput
	go install $(LDFLAGS) ./cmd/z3get
	go install $(LDFLAGS) ./cmd/z3sync

# Cross-compile for different platforms
build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/z3-linux-amd64 ./cmd/z3
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/pput-linux-amd64 ./cmd/pput

build-freebsd:
	GOOS=freebsd GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/z3-freebsd-amd64 ./cmd/z3
	GOOS=freebsd GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/pput-freebsd-amd64 ./cmd/pput

# Quick development build (no optimization)
dev: format
	go build -o $(BUILD_DIR)/z3 ./cmd/z3

# Run z3 in development mode
run: dev
	./$(BUILD_DIR)/z3

# Help target
help:
	@echo "Available targets:"
	@echo "  all          - Run deps, format, lint, test, and build"
	@echo "  build        - Build all binaries"
	@echo "  test         - Run all tests"
	@echo "  test-coverage- Run tests with coverage report"
	@echo "  bench        - Run benchmarks"
	@echo "  lint         - Run linter"
	@echo "  format       - Format code"
	@echo "  clean        - Clean build artifacts"
	@echo "  install      - Install binaries to GOPATH/bin"
	@echo "  dev-setup    - Set up development tools"
	@echo "  dev          - Quick development build"
	@echo "  run          - Build and run z3"
	@echo "  help         - Show this help"