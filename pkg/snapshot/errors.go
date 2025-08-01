package snapshot

import (
	"fmt"
)

// Error types for snapshot operations
var (
	// ErrSnapshotNotFound indicates a snapshot was not found
	ErrSnapshotNotFound = &SnapshotError{Type: ErrorTypeNotFound, Message: "snapshot not found"}

	// ErrSnapshotExists indicates a snapshot already exists
	ErrSnapshotExists = &SnapshotError{Type: ErrorTypeExists, Message: "snapshot already exists"}

	// ErrInvalidSnapshot indicates a snapshot is invalid
	ErrInvalidSnapshot = &SnapshotError{Type: ErrorTypeInvalid, Message: "invalid snapshot"}

	// ErrUnhealthySnapshot indicates a snapshot is unhealthy
	ErrUnhealthySnapshot = &SnapshotError{Type: ErrorTypeUnhealthy, Message: "unhealthy snapshot"}

	// ErrCycleDetected indicates a cycle in the snapshot chain
	ErrCycleDetected = &SnapshotError{Type: ErrorTypeCycle, Message: "cycle detected in snapshot chain"}
)

// ErrorType represents different types of snapshot errors
type ErrorType string

const (
	// ErrorTypeNotFound indicates a resource was not found
	ErrorTypeNotFound ErrorType = "not_found"

	// ErrorTypeExists indicates a resource already exists
	ErrorTypeExists ErrorType = "exists"

	// ErrorTypeInvalid indicates invalid input or state
	ErrorTypeInvalid ErrorType = "invalid"

	// ErrorTypeUnhealthy indicates an unhealthy snapshot
	ErrorTypeUnhealthy ErrorType = "unhealthy"

	// ErrorTypeCycle indicates a cycle in dependencies
	ErrorTypeCycle ErrorType = "cycle"

	// ErrorTypeZFS indicates a ZFS-related error
	ErrorTypeZFS ErrorType = "zfs"

	// ErrorTypeS3 indicates an S3-related error
	ErrorTypeS3 ErrorType = "s3"

	// ErrorTypeCompression indicates a compression-related error
	ErrorTypeCompression ErrorType = "compression"

	// ErrorTypeNetwork indicates a network-related error
	ErrorTypeNetwork ErrorType = "network"

	// ErrorTypePermission indicates a permission-related error
	ErrorTypePermission ErrorType = "permission"

	// ErrorTypeInternal indicates an internal error
	ErrorTypeInternal ErrorType = "internal"
)

// SnapshotError represents an error in snapshot operations
type SnapshotError struct {
	// Type categorizes the error
	Type ErrorType

	// Message describes the error
	Message string

	// SnapshotName is the name of the snapshot involved (if applicable)
	SnapshotName string

	// Operation is the operation that failed
	Operation string

	// Cause is the underlying error that caused this error
	Cause error
}

// Error implements the error interface
func (e *SnapshotError) Error() string {
	if e.SnapshotName != "" && e.Operation != "" {
		return fmt.Sprintf("%s failed for snapshot %s: %s", e.Operation, e.SnapshotName, e.Message)
	} else if e.SnapshotName != "" {
		return fmt.Sprintf("snapshot %s: %s", e.SnapshotName, e.Message)
	} else if e.Operation != "" {
		return fmt.Sprintf("%s failed: %s", e.Operation, e.Message)
	}
	return e.Message
}

// Unwrap returns the underlying error
func (e *SnapshotError) Unwrap() error {
	return e.Cause
}

// Is checks if this error matches the target error
func (e *SnapshotError) Is(target error) bool {
	if t, ok := target.(*SnapshotError); ok {
		return e.Type == t.Type
	}
	return false
}

// ZFSError represents an error from ZFS operations
type ZFSError struct {
	// Operation is the ZFS operation that failed (send, recv, list, etc.)
	Operation string

	// Dataset is the ZFS dataset involved
	Dataset string

	// Snapshot is the snapshot name involved (if applicable)
	Snapshot string

	// Command is the ZFS command that was executed
	Command string

	// ExitCode is the exit code from the ZFS command
	ExitCode int

	// Stderr contains the error output from the command
	Stderr string

	// Cause is the underlying error
	Cause error
}

// Error implements the error interface
func (e *ZFSError) Error() string {
	if e.Snapshot != "" {
		return fmt.Sprintf("zfs %s failed for %s@%s (exit code %d): %s",
			e.Operation, e.Dataset, e.Snapshot, e.ExitCode, e.Stderr)
	} else if e.Dataset != "" {
		return fmt.Sprintf("zfs %s failed for %s (exit code %d): %s",
			e.Operation, e.Dataset, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("zfs %s failed (exit code %d): %s",
		e.Operation, e.ExitCode, e.Stderr)
}

// Unwrap returns the underlying error
func (e *ZFSError) Unwrap() error {
	return e.Cause
}

// S3Error represents an error from S3 operations
type S3Error struct {
	// Operation is the S3 operation that failed (upload, download, delete, etc.)
	Operation string

	// Bucket is the S3 bucket involved
	Bucket string

	// Key is the S3 key involved
	Key string

	// StatusCode is the HTTP status code (if applicable)
	StatusCode int

	// ErrorCode is the S3 error code
	ErrorCode string

	// Message is the error message from S3
	Message string

	// Cause is the underlying error
	Cause error
}

// Error implements the error interface
func (e *S3Error) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("s3 %s failed for %s/%s (status %d, code %s): %s",
			e.Operation, e.Bucket, e.Key, e.StatusCode, e.ErrorCode, e.Message)
	} else if e.Bucket != "" {
		return fmt.Sprintf("s3 %s failed for bucket %s (status %d, code %s): %s",
			e.Operation, e.Bucket, e.StatusCode, e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("s3 %s failed (status %d, code %s): %s",
		e.Operation, e.StatusCode, e.ErrorCode, e.Message)
}

// Unwrap returns the underlying error
func (e *S3Error) Unwrap() error {
	return e.Cause
}

// CompressionError represents an error from compression operations
type CompressionError struct {
	// Compressor is the compressor that failed (pigz, gpg, etc.)
	Compressor string

	// Operation is the operation that failed (compress, decompress)
	Operation string

	// Message describes the error
	Message string

	// Cause is the underlying error
	Cause error
}

// Error implements the error interface
func (e *CompressionError) Error() string {
	return fmt.Sprintf("%s %s failed: %s", e.Compressor, e.Operation, e.Message)
}

// Unwrap returns the underlying error
func (e *CompressionError) Unwrap() error {
	return e.Cause
}

// NewSnapshotError creates a new snapshot error
func NewSnapshotError(errorType ErrorType, message string) *SnapshotError {
	return &SnapshotError{
		Type:    errorType,
		Message: message,
	}
}

// NewSnapshotErrorWithSnapshot creates a new snapshot error for a specific snapshot
func NewSnapshotErrorWithSnapshot(errorType ErrorType, message, snapshotName string) *SnapshotError {
	return &SnapshotError{
		Type:         errorType,
		Message:      message,
		SnapshotName: snapshotName,
	}
}

// NewSnapshotErrorWithCause creates a new snapshot error with an underlying cause
func NewSnapshotErrorWithCause(errorType ErrorType, message string, cause error) *SnapshotError {
	return &SnapshotError{
		Type:    errorType,
		Message: message,
		Cause:   cause,
	}
}

// WrapZFSError wraps an error as a ZFS error
func WrapZFSError(operation, dataset string, err error) *ZFSError {
	return &ZFSError{
		Operation: operation,
		Dataset:   dataset,
		Cause:     err,
	}
}

// WrapS3Error wraps an error as an S3 error
func WrapS3Error(operation, bucket, key string, err error) *S3Error {
	return &S3Error{
		Operation: operation,
		Bucket:    bucket,
		Key:       key,
		Cause:     err,
	}
}

// WrapCompressionError wraps an error as a compression error
func WrapCompressionError(compressor, operation string, err error) *CompressionError {
	return &CompressionError{
		Compressor: compressor,
		Operation:  operation,
		Message:    err.Error(),
		Cause:      err,
	}
}

// IsNotFoundError checks if an error is a "not found" error
func IsNotFoundError(err error) bool {
	var snapErr *SnapshotError
	if AsSnapshotError(err, &snapErr) {
		return snapErr.Type == ErrorTypeNotFound
	}
	return false
}

// IsExistsError checks if an error is an "already exists" error
func IsExistsError(err error) bool {
	var snapErr *SnapshotError
	if AsSnapshotError(err, &snapErr) {
		return snapErr.Type == ErrorTypeExists
	}
	return false
}

// IsUnhealthyError checks if an error is due to an unhealthy snapshot
func IsUnhealthyError(err error) bool {
	var snapErr *SnapshotError
	if AsSnapshotError(err, &snapErr) {
		return snapErr.Type == ErrorTypeUnhealthy
	}
	return false
}

// AsSnapshotError attempts to convert an error to a SnapshotError
func AsSnapshotError(err error, target **SnapshotError) bool {
	if err == nil {
		return false
	}

	if snapErr, ok := err.(*SnapshotError); ok {
		*target = snapErr
		return true
	}

	// Try to unwrap and check again
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		return AsSnapshotError(unwrapper.Unwrap(), target)
	}

	return false
}

// AsZFSError attempts to convert an error to a ZFSError
func AsZFSError(err error, target **ZFSError) bool {
	if err == nil {
		return false
	}

	if zfsErr, ok := err.(*ZFSError); ok {
		*target = zfsErr
		return true
	}

	// Try to unwrap and check again
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		return AsZFSError(unwrapper.Unwrap(), target)
	}

	return false
}

// AsS3Error attempts to convert an error to an S3Error
func AsS3Error(err error, target **S3Error) bool {
	if err == nil {
		return false
	}

	if s3Err, ok := err.(*S3Error); ok {
		*target = s3Err
		return true
	}

	// Try to unwrap and check again
	if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
		return AsS3Error(unwrapper.Unwrap(), target)
	}

	return false
}
