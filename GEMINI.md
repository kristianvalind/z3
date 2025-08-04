# Z3 ZFS Backup Tool - Go Port Project

## Project Overview

Z3 is a ZFS to S3 backup tool that provides efficient snapshot backup and restore capabilities with encryption support. This document provides comprehensive guidance for the Go implementation.

## Go Implementation Analysis

### Core Components

1.  **Main CLI (`cmd/z3/main.go`)** - Primary command-line interface using Cobra.
2.  **Configuration (`internal/config/config.go`)** - Multi-layer configuration management.
3.  **Core Libraries** - S3 upload/download, SSH sync capabilities, ZFS operations.

### Key Features

-   Full and incremental ZFS backups
-   GPG encryption support
-   Multipart S3 uploads with configurable concurrency
-   Health checking of snapshot chains
-   Dry-run mode for all operations
-   Recursive restore of snapshots

### Go Libraries

#### AWS S3 Operations

```go
// Primary S3 SDK
github.com/aws/aws-sdk-go-v2/service/s3

// High-level upload/download manager
github.com/aws/aws-sdk-go-v2/feature/s3/manager

// AWS configuration
github.com/aws/aws-sdk-go-v2/config
```

#### Command Line Interface

```go
// Comprehensive CLI framework
github.com/spf13/cobra
```

#### Standard Library Usage

-   `os/exec` - Shell command execution
-   `io`, `os`, `filepath` - System operations
-   Built-in goroutines and channels for concurrency

### Project Structure

```
.
├── cmd/
│   └── z3/           # Main CLI application
├── internal/
│   ├── backup/       # Backup and restore orchestration
│   ├── config/       # Configuration management
│   ├── zfs/          # ZFS operations
│   ├── s3/           # S3 operations
│   ├── compress/     # Compression handling
│   └── ssh/          # SSH operations
├── pkg/
│   └── snapshot/     # Snapshot types and operations
├── go.mod
├── go.sum
└── README.md
```

### Core Types

```go
// Snapshot represents a ZFS or S3 snapshot
type Snapshot struct {
    Name           string
    IsFullBackup   bool
    ParentName     string
    Size           int64
    CompressedSize int64
    Compressor     string
    CreatedAt      time.Time
    Metadata       map[string]string
}

// Manager orchestrates backup and restore operations
type Manager struct {
    config           *config.Config
    zfsManager       *zfs.Manager
    s3Client         *s3.Client
    compressPipeline *compress.Pipeline
}
```

### Key Implementation Considerations

#### Concurrency Model

-   Goroutines are used for concurrent operations like S3 multipart uploads.
-   Channels are used for communication between goroutines.

#### Error Handling

-   Custom error types are defined for different parts of the application (e.g., `zfs.ZFSError`).
-   Explicit error handling is used throughout the codebase.

#### Command Execution

-   `os/exec` is used to run ZFS commands.
-   Pipes are used to connect command inputs and outputs.

### Testing Strategy

#### Unit Tests

-   Each component is tested in isolation.
-   External dependencies (S3, ZFS commands) are mocked.
-   Table-driven tests are used for multiple scenarios.

#### Integration Tests

-   Tests are run with real S3 buckets and ZFS test pools.

### Development Commands

```bash
# Run tests
go test ./...

# Build binaries
go build -o bin/z3 ./cmd/z3

# Cross-compile for different platforms
GOOS=linux GOARCH=amd64 go build -o bin/z3-linux ./cmd/z3
```
