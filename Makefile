.PHONY: build clean run test help

# Binary name
BINARY_NAME=chrome-agent

# Build directory
BUILD_DIR=.

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build flags
LDFLAGS=-ldflags "-s -w"
BUILD_FLAGS=-o $(BUILD_DIR)/$(BINARY_NAME)

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	$(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for current platform (default)
build-local: build

# Build for Linux AMD64
build-linux:
	@echo "Building $(BINARY_NAME) for Linux AMD64..."
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for macOS AMD64
build-darwin:
	@echo "Building $(BINARY_NAME) for macOS AMD64..."
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for macOS ARM64 (Apple Silicon)
build-darwin-arm:
	@echo "Building $(BINARY_NAME) for macOS ARM64..."
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(BUILD_FLAGS) $(LDFLAGS) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

# Build for Windows
build-windows:
	@echo "Building $(BINARY_NAME) for Windows..."
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(BUILD_FLAGS).exe $(LDFLAGS) .
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME).exe"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -f $(BUILD_DIR)/$(BINARY_NAME)
	rm -f $(BUILD_DIR)/$(BINARY_NAME).exe
	@echo "Clean complete"

# Run the application
run:
	@echo "Running $(BINARY_NAME)..."
	$(GOBUILD) $(BUILD_FLAGS) .
	./$(BINARY_NAME)

# Run tests
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Format code
fmt:
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

# Run linter (requires golangci-lint)
lint:
	@echo "Running linter..."
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed. Install it from https://golangci-lint.run/"; \
	fi

# Install dependencies and build
install: deps build

# Show help
help:
	@echo "Available targets:"
	@echo "  build          - Build the binary for current platform"
	@echo "  build-linux    - Build for Linux AMD64"
	@echo "  build-darwin   - Build for macOS AMD64"
	@echo "  build-darwin-arm - Build for macOS ARM64"
	@echo "  build-windows  - Build for Windows"
	@echo "  clean          - Remove build artifacts"
	@echo "  run            - Build and run the application"
	@echo "  test           - Run tests"
	@echo "  deps           - Download and tidy dependencies"
	@echo "  fmt            - Format code"
	@echo "  lint           - Run linter (requires golangci-lint)"
	@echo "  install        - Download dependencies and build"
	@echo "  help           - Show this help message"

