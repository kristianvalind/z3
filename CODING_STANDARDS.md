# Z3 Go Port - Coding Standards

## Overview

This document defines coding standards and conventions for the Z3 Go port to ensure consistency, maintainability, and quality across the codebase.

## Go Language Standards

### General Guidelines

- Follow the official [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- Use `gofmt` for code formatting (enforced by `make format`)
- Use `golangci-lint` for static analysis (enforced by `make lint`)
- Write idiomatic Go code following the [Effective Go](https://golang.org/doc/effective_go.html) guide

### Package Organization

```go
// Package declaration with meaningful documentation
// Package config provides configuration management for Z3.
//
// It supports multiple configuration sources with precedence:
// 1. Command line flags
// 2. Environment variables  
// 3. Configuration files
// 4. Default values
package config
```

### Import Organization

```go
import (
    // Standard library first
    "context"
    "fmt"
    "os"
    
    // Third-party packages
    "github.com/aws/aws-sdk-go-v2/service/s3"
    "github.com/spf13/viper"
    
    // Local packages last
    "github.com/kristianvalind/z3/internal/config"
    "github.com/kristianvalind/z3/pkg/snapshot"
)
```

## Naming Conventions

### Variables and Functions

```go
// Use camelCase for unexported identifiers
var maxRetries = 3
func parseSnapshot(name string) (*Snapshot, error) { }

// Use PascalCase for exported identifiers
type SnapshotManager interface { }
func NewS3Client(config *Config) (*S3Client, error) { }

// Use descriptive names
var s3UploadConcurrency = 64  // Good
var c = 64                    // Bad

// Use verb-noun pattern for functions
func validateSnapshot(s *Snapshot) error { }  // Good
func snapshotValidation(s *Snapshot) error { } // Bad
```

### Constants

```go
// Use PascalCase for exported constants
const (
    DefaultConcurrency     = 64
    DefaultChunkSize       = 5 * 1024 * 1024
    MaxRetries            = 3
)

// Use camelCase for unexported constants
const (
    defaultTimeout        = 30 * time.Second
    maxPartSize          = 5 * 1024 * 1024 * 1024
)
```

### Types and Interfaces

```go
// Use PascalCase and descriptive names
type SnapshotManager interface {
    List() ([]*Snapshot, error)
    Get(name string) (*Snapshot, error)
}

// Interface names should describe behavior
type Uploader interface { }     // Good
type S3Interface interface { }  // Bad
```

## Error Handling

### Error Types

```go
// Define custom error types for different categories
type ZFSError struct {
    Operation string
    Dataset   string
    Err       error
}

func (e ZFSError) Error() string {
    return fmt.Sprintf("zfs %s failed for dataset %s: %v", e.Operation, e.Dataset, e.Err)
}

func (e ZFSError) Unwrap() error {
    return e.Err
}
```

### Error Wrapping

```go
// Always wrap errors with context
func backupSnapshot(name string) error {
    snap, err := getSnapshot(name)
    if err != nil {
        return fmt.Errorf("getting snapshot %s: %w", name, err)
    }
    
    if err := uploadSnapshot(snap); err != nil {
        return fmt.Errorf("uploading snapshot %s: %w", name, err)
    }
    
    return nil
}
```

### Error Checking

```go
// Check errors immediately
file, err := os.Open(filename)
if err != nil {
    return fmt.Errorf("opening file %s: %w", filename, err)
}
defer file.Close()

// Don't use underscore to ignore errors unless truly appropriate
_, err = fmt.Println("message")  // Bad - check the error
if err != nil {
    return fmt.Errorf("printing message: %w", err)
}
```

## Testing Standards

### Test File Organization

```go
// File: internal/config/config_test.go
package config

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)
```

### Test Function Names

```go
// Use descriptive test names following TestFunction_Scenario_ExpectedBehavior
func TestLoadConfig_ValidFile_ReturnsConfig(t *testing.T) { }
func TestLoadConfig_MissingFile_ReturnsError(t *testing.T) { }
func TestS3Upload_NetworkError_RetriesWithBackoff(t *testing.T) { }
```

### Table-Driven Tests

```go
func TestParseSize(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected int64
        wantErr  bool
    }{
        {
            name:     "megabytes",
            input:    "100M",
            expected: 100 * 1024 * 1024,
            wantErr:  false,
        },
        {
            name:     "invalid format",
            input:    "invalid",
            expected: 0,
            wantErr:  true,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result, err := parseSize(tt.input)
            if tt.wantErr {
                assert.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.expected, result)
        })
    }
}
```

### Mocking and Test Helpers

```go
// Use interfaces for testability
type S3Client interface {
    Upload(ctx context.Context, key string, data io.Reader) error
}

// Create test helpers for common setup
func createTestSnapshot(name string) *Snapshot {
    return &Snapshot{
        Name:         name,
        IsFullBackup: false,
        Size:         1024,
        CreatedAt:    time.Now(),
    }
}
```

## Documentation Standards

### Package Documentation

```go
// Package snap provides ZFS snapshot management and backup operations.
//
// The package supports both full and incremental backups to S3 with
// configurable compression and encryption. It maintains compatibility
// with the original Python Z3 implementation.
//
// Basic usage:
//
//    manager := snap.NewManager(config)
//    snapshots, err := manager.List()
//    if err != nil {
//        return err
//    }
//
package snap
```

### Function Documentation

```go
// NewS3Client creates a new S3 client with the provided configuration.
//
// The client is configured with appropriate timeouts, retry policies,
// and region settings based on the provided config. If no region is
// specified, it defaults to us-east-1.
//
// Returns an error if the AWS credentials are invalid or if the
// S3 service is unreachable.
func NewS3Client(config *Config) (*S3Client, error) {
    // Implementation
}
```

### Type Documentation

```go
// Snapshot represents a ZFS or S3 snapshot with its metadata.
//
// Snapshots can be either full backups or incremental backups that
// depend on a parent snapshot. The health checking system validates
// that incremental snapshots have valid parent chains.
type Snapshot struct {
    // Name is the full snapshot name including filesystem (e.g., "tank/data@snap1")
    Name string
    
    // IsFullBackup indicates whether this is a full backup or incremental
    IsFullBackup bool
    
    // ParentName is the name of the parent snapshot for incremental backups
    ParentName string
    
    // Size is the uncompressed size of the snapshot in bytes
    Size int64
}
```

## Code Organization Patterns

### Constructor Pattern

```go
// Use New* functions for constructors
func NewSnapshotManager(filesystem, prefix string) *SnapshotManager {
    return &SnapshotManager{
        filesystem: filesystem,
        prefix:     prefix,
        snapshots:  make(map[string]*Snapshot),
    }
}
```

### Options Pattern

```go
// Use functional options for complex configuration
type ClientOption func(*Client)

func WithTimeout(timeout time.Duration) ClientOption {
    return func(c *Client) {
        c.timeout = timeout
    }
}

func NewClient(opts ...ClientOption) *Client {
    c := &Client{
        timeout: defaultTimeout,
    }
    
    for _, opt := range opts {
        opt(c)
    }
    
    return c
}
```

### Interface Segregation

```go
// Define small, focused interfaces
type Reader interface {
    Read([]byte) (int, error)
}

type Writer interface {
    Write([]byte) (int, error)
}

// Compose larger interfaces from smaller ones
type ReadWriter interface {
    Reader
    Writer
}
```

## Concurrency Patterns

### Goroutine Management

```go
// Use context for cancellation
func processSnapshots(ctx context.Context, snapshots []*Snapshot) error {
    for _, snap := range snapshots {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
            if err := processSnapshot(ctx, snap); err != nil {
                return err
            }
        }
    }
    return nil
}
```

### Worker Pools

```go
// Use worker pools for controlled concurrency
func (u *Uploader) startWorkers(ctx context.Context, concurrency int) {
    for i := 0; i < concurrency; i++ {
        go func() {
            defer u.wg.Done()
            for {
                select {
                case <-ctx.Done():
                    return
                case job := <-u.jobs:
                    u.processJob(job)
                }
            }
        }()
    }
}
```

### Channel Usage

```go
// Use channels for communication
type Result struct {
    Data interface{}
    Err  error
}

func processAsync(input <-chan *Job) <-chan Result {
    results := make(chan Result)
    
    go func() {
        defer close(results)
        for job := range input {
            result := process(job)
            results <- result
        }
    }()
    
    return results
}
```

## Performance Guidelines

### Memory Management

```go
// Reuse buffers where possible
var bufferPool = sync.Pool{
    New: func() interface{} {
        return make([]byte, 64*1024)
    },
}

func processData(data []byte) error {
    buf := bufferPool.Get().([]byte)
    defer bufferPool.Put(buf)
    
    // Use buf for processing
    return nil
}
```

### String Building

```go
// Use strings.Builder for efficient string concatenation
func buildKey(parts ...string) string {
    var builder strings.Builder
    for i, part := range parts {
        if i > 0 {
            builder.WriteByte('/')
        }
        builder.WriteString(part)
    }
    return builder.String()
}
```

## Compatibility Requirements

### Python Port Compatibility

- Maintain identical CLI interfaces
- Preserve configuration file format compatibility
- Ensure S3 metadata format compatibility
- Support all compression formats from Python version

### Cross-Platform Support

```go
// Use filepath package for cross-platform path handling
import "path/filepath"

configPath := filepath.Join(os.Getenv("HOME"), ".z3", "config")

// Use os.PathSeparator for platform-specific separators
// Use filepath.Clean() to normalize paths
```

## Quality Assurance

### Mandatory Checks

All code must pass:

1. `go fmt` - Code formatting
2. `golangci-lint run` - Static analysis
3. `go test -race ./...` - Race condition detection
4. `go test -cover ./...` - Test coverage (>90% target)

### Pre-commit Checklist

- [ ] Code formatted with `gofmt`
- [ ] All linter warnings addressed
- [ ] Tests written and passing
- [ ] Documentation updated
- [ ] Error handling implemented
- [ ] Race conditions checked
- [ ] Performance impact considered

### Code Review Guidelines

- Focus on correctness, performance, and maintainability
- Ensure error handling is comprehensive
- Verify test coverage for new functionality
- Check for potential race conditions
- Validate compatibility requirements

This coding standard ensures the Z3 Go port maintains high quality, consistency, and compatibility with the original Python implementation while leveraging Go's strengths for performance and reliability.