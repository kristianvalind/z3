# Z3 ZFS Backup Tool - Go Port Project

## Project Overview

Z3 is a ZFS to S3 backup tool that provides efficient snapshot backup and restore capabilities with encryption support. This document provides comprehensive guidance for porting the Python implementation to Go.

## Current Python Implementation Analysis

### Core Components

1. **Main CLI (`z3/snap.py`)** - Primary command-line interface
2. **Configuration (`z3/config.py`)** - Multi-layer configuration management
3. **Core Libraries** - S3 upload/download, SSH sync capabilities

### Key Features
- Full and incremental ZFS backups
- GPG encryption support (pigz1, pigz4, gpg compressors)
- Multipart S3 uploads with configurable concurrency
- Health checking of snapshot chains
- Dry-run mode for all operations
- SSH-based snapshot synchronization
- Progress reporting with `pv` integration

### Dependencies
- **boto/boto3**: AWS S3 operations
- **subprocess**: ZFS command execution (`zfs send`, `zfs recv`)
- **threading**: Concurrent S3 uploads
- **configparser**: Configuration file parsing
- **hashlib**: MD5 checksums for multipart uploads
- **argparse**: Command-line interface

## Go Port Strategy

### Recommended Go Libraries

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
// Comprehensive CLI framework (recommended)
github.com/spf13/cobra

// Python argparse equivalent (alternative)
github.com/akamensky/argparse
```

#### Configuration Management
```go
// Multi-format configuration (recommended)
github.com/spf13/viper

// Python ConfigParser equivalent
github.com/bigkevmcd/go-configparser
```

#### Standard Library Usage
- `os/exec` - Shell command execution
- `crypto/md5` - MD5 checksums
- `io`, `os`, `filepath` - System operations
- Built-in goroutines and channels for concurrency

### Project Structure

```
z3-go/
├── cmd/
│   ├── z3/           # Main CLI application
│   └── z3/           # Main CLI application
├── internal/
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

### Core Types to Implement

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

// SnapshotManager handles snapshot operations
type SnapshotManager interface {
    List() ([]*Snapshot, error)
    Get(name string) (*Snapshot, error)
    IsHealthy(snapshot *Snapshot) (bool, error)
}

// BackupManager orchestrates backup operations
type BackupManager struct {
    S3Manager  SnapshotManager
    ZFSManager SnapshotManager
    Config     *Config
}
```

### Migration Priorities

#### Phase 1: Core Infrastructure
1. **Configuration system** - Port `config.py` functionality
2. **ZFS operations** - Implement `zfs send`/`zfs recv` execution
3. **S3 basic operations** - Single-part uploads/downloads
4. **Snapshot types** - Core data structures

#### Phase 2: Advanced Features  
1. **Multipart S3 uploads** - Integrated S3 upload functionality
2. **Compression support** - Implement pigz and GPG pipelines
3. **Health checking** - Port snapshot integrity verification
4. **CLI interface** - Port all subcommands (`status`, `backup`, `restore`)

#### Phase 3: Additional Tools
1. **SSH sync** - Port `ssh_sync.py` functionality
2. **Progress reporting** - Implement progress bars
3. **Error handling** - Comprehensive error types and handling
4. **Testing** - Unit and integration tests

### Key Implementation Considerations

#### Concurrency Model
- Replace Python threads with goroutines for better performance
- Use channels for communication between goroutines
- Implement worker pools for S3 multipart uploads
- Leverage Go's built-in race detection during development

#### Error Handling
```go
// Define custom error types
type ZFSError struct {
    Operation string
    Dataset   string
    Err       error
}

type S3Error struct {
    Bucket string
    Key    string
    Err    error
}

// Use Go's explicit error handling
if err := manager.Backup(snapshot); err != nil {
    return fmt.Errorf("backup failed: %w", err)
}
```

#### Configuration Compatibility
- Maintain compatibility with existing `.conf` files
- Support environment variable overrides
- Implement per-filesystem configuration sections

#### Command Execution
```go
// Replace subprocess calls with os/exec
func runZFSCommand(args ...string) (*exec.Cmd, error) {
    cmd := exec.Command("zfs", args...)
    return cmd, nil
}

// Implement piping similar to Python's pipe operations
func pipeCommands(cmd1, cmd2 *exec.Cmd) error {
    pipe, err := cmd1.StdoutPipe()
    if err != nil {
        return err
    }
    cmd2.Stdin = pipe
    // ... error handling and execution
}
```

### Testing Strategy

#### Unit Tests
- Test each component in isolation
- Mock external dependencies (S3, ZFS commands)
- Use table-driven tests for multiple scenarios

#### Integration Tests
- Test with real S3 buckets (using test credentials)
- Use ZFS test pools for end-to-end testing
- Implement test fixtures for snapshot chains

#### Performance Tests
- Benchmark multipart upload performance
- Compare memory usage with Python version
- Test concurrent operation limits

### Development Commands

```bash
# Initialize Go module
go mod init github.com/your-org/z3-go

# Add dependencies
go get github.com/aws/aws-sdk-go-v2/service/s3
go get github.com/spf13/cobra
go get github.com/spf13/viper

# Run tests
go test ./...

# Build binaries
go build -o bin/z3 ./cmd/z3
go build -o bin/z3 ./cmd/z3

# Cross-compile for different platforms
GOOS=linux GOARCH=amd64 go build -o bin/z3-linux ./cmd/z3
```

### Expected Benefits of Go Port

1. **Performance**: Compiled binary with no runtime overhead
2. **Concurrency**: True parallelism without GIL limitations  
3. **Memory**: Lower memory usage for large operations
4. **Deployment**: Single binary with no dependency management
5. **Reliability**: Strong typing and comprehensive error handling
6. **Cross-platform**: Easy compilation for multiple architectures

### Compatibility Considerations

- Maintain command-line interface compatibility
- Preserve configuration file format
- Ensure S3 metadata compatibility for existing backups
- Support same compression formats (pigz, GPG)

This port should result in a more performant, reliable, and maintainable ZFS backup solution while preserving all existing functionality.