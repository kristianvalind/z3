package snapshot

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnapshotError(t *testing.T) {
	tests := []struct {
		name     string
		err      *SnapshotError
		expected string
	}{
		{
			name: "basic error",
			err: &SnapshotError{
				Type:    ErrorTypeNotFound,
				Message: "snapshot not found",
			},
			expected: "snapshot not found",
		},
		{
			name: "error with snapshot name",
			err: &SnapshotError{
				Type:         ErrorTypeNotFound,
				Message:      "not found",
				SnapshotName: "tank/data@snap1",
			},
			expected: "snapshot tank/data@snap1: not found",
		},
		{
			name: "error with operation",
			err: &SnapshotError{
				Type:      ErrorTypeInvalid,
				Message:   "invalid parameters",
				Operation: "backup",
			},
			expected: "backup failed: invalid parameters",
		},
		{
			name: "error with operation and snapshot",
			err: &SnapshotError{
				Type:         ErrorTypeUnhealthy,
				Message:      "unhealthy snapshot chain",
				SnapshotName: "tank/data@snap2",
				Operation:    "backup",
			},
			expected: "backup failed for snapshot tank/data@snap2: unhealthy snapshot chain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestSnapshotError_Unwrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := &SnapshotError{
		Type:    ErrorTypeInternal,
		Message: "wrapper error",
		Cause:   cause,
	}

	assert.Equal(t, cause, err.Unwrap())
}

func TestSnapshotError_Is(t *testing.T) {
	err1 := &SnapshotError{Type: ErrorTypeNotFound, Message: "not found"}
	err2 := &SnapshotError{Type: ErrorTypeNotFound, Message: "different message"}
	err3 := &SnapshotError{Type: ErrorTypeExists, Message: "exists"}

	assert.True(t, err1.Is(err2))  // Same type
	assert.False(t, err1.Is(err3)) // Different type
	assert.False(t, err1.Is(errors.New("different error type")))
}

func TestZFSError(t *testing.T) {
	tests := []struct {
		name     string
		err      *ZFSError
		expected string
	}{
		{
			name: "basic operation error",
			err: &ZFSError{
				Operation: "send",
				Dataset:   "tank/data",
				ExitCode:  1,
				Stderr:    "permission denied",
			},
			expected: "zfs send failed for tank/data (exit code 1): permission denied",
		},
		{
			name: "error with snapshot",
			err: &ZFSError{
				Operation: "send",
				Dataset:   "tank/data",
				Snapshot:  "snap1",
				ExitCode:  2,
				Stderr:    "snapshot does not exist",
			},
			expected: "zfs send failed for tank/data@snap1 (exit code 2): snapshot does not exist",
		},
		{
			name: "error without dataset",
			err: &ZFSError{
				Operation: "list",
				ExitCode:  1,
				Stderr:    "command not found",
			},
			expected: "zfs list failed (exit code 1): command not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestS3Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *S3Error
		expected string
	}{
		{
			name: "basic s3 error",
			err: &S3Error{
				Operation:  "upload",
				Bucket:     "my-bucket",
				Key:        "backup/snap1",
				StatusCode: 403,
				ErrorCode:  "AccessDenied",
				Message:    "Access Denied",
			},
			expected: "s3 upload failed for my-bucket/backup/snap1 (status 403, code AccessDenied): Access Denied",
		},
		{
			name: "bucket-level error",
			err: &S3Error{
				Operation:  "list",
				Bucket:     "my-bucket",
				StatusCode: 404,
				ErrorCode:  "NoSuchBucket",
				Message:    "The specified bucket does not exist",
			},
			expected: "s3 list failed for bucket my-bucket (status 404, code NoSuchBucket): The specified bucket does not exist",
		},
		{
			name: "general s3 error",
			err: &S3Error{
				Operation:  "connect",
				StatusCode: 500,
				ErrorCode:  "InternalError",
				Message:    "We encountered an internal error. Please try again.",
			},
			expected: "s3 connect failed (status 500, code InternalError): We encountered an internal error. Please try again.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestCompressionError(t *testing.T) {
	err := &CompressionError{
		Compressor: "pigz",
		Operation:  "compress",
		Message:    "invalid compression level",
	}

	expected := "pigz compress failed: invalid compression level"
	assert.Equal(t, expected, err.Error())
}

func TestErrorConstructors(t *testing.T) {
	t.Run("NewSnapshotError", func(t *testing.T) {
		err := NewSnapshotError(ErrorTypeNotFound, "test message")
		assert.Equal(t, ErrorTypeNotFound, err.Type)
		assert.Equal(t, "test message", err.Message)
	})

	t.Run("NewSnapshotErrorWithSnapshot", func(t *testing.T) {
		err := NewSnapshotErrorWithSnapshot(ErrorTypeExists, "already exists", "tank/data@snap1")
		assert.Equal(t, ErrorTypeExists, err.Type)
		assert.Equal(t, "already exists", err.Message)
		assert.Equal(t, "tank/data@snap1", err.SnapshotName)
	})

	t.Run("NewSnapshotErrorWithCause", func(t *testing.T) {
		cause := errors.New("underlying error")
		err := NewSnapshotErrorWithCause(ErrorTypeInternal, "wrapper", cause)
		assert.Equal(t, ErrorTypeInternal, err.Type)
		assert.Equal(t, "wrapper", err.Message)
		assert.Equal(t, cause, err.Cause)
	})

	t.Run("WrapZFSError", func(t *testing.T) {
		cause := errors.New("command failed")
		err := WrapZFSError("send", "tank/data", cause)
		assert.Equal(t, "send", err.Operation)
		assert.Equal(t, "tank/data", err.Dataset)
		assert.Equal(t, cause, err.Cause)
	})

	t.Run("WrapS3Error", func(t *testing.T) {
		cause := errors.New("network error")
		err := WrapS3Error("upload", "my-bucket", "backup/snap1", cause)
		assert.Equal(t, "upload", err.Operation)
		assert.Equal(t, "my-bucket", err.Bucket)
		assert.Equal(t, "backup/snap1", err.Key)
		assert.Equal(t, cause, err.Cause)
	})

	t.Run("WrapCompressionError", func(t *testing.T) {
		cause := errors.New("compression failed")
		err := WrapCompressionError("pigz", "compress", cause)
		assert.Equal(t, "pigz", err.Compressor)
		assert.Equal(t, "compress", err.Operation)
		assert.Equal(t, cause, err.Cause)
	})
}

func TestErrorPredicates(t *testing.T) {
	notFoundErr := NewSnapshotError(ErrorTypeNotFound, "not found")
	existsErr := NewSnapshotError(ErrorTypeExists, "exists")
	unhealthyErr := NewSnapshotError(ErrorTypeUnhealthy, "unhealthy")
	otherErr := errors.New("other error")

	t.Run("IsNotFoundError", func(t *testing.T) {
		assert.True(t, IsNotFoundError(notFoundErr))
		assert.False(t, IsNotFoundError(existsErr))
		assert.False(t, IsNotFoundError(otherErr))
	})

	t.Run("IsExistsError", func(t *testing.T) {
		assert.True(t, IsExistsError(existsErr))
		assert.False(t, IsExistsError(notFoundErr))
		assert.False(t, IsExistsError(otherErr))
	})

	t.Run("IsUnhealthyError", func(t *testing.T) {
		assert.True(t, IsUnhealthyError(unhealthyErr))
		assert.False(t, IsUnhealthyError(notFoundErr))
		assert.False(t, IsUnhealthyError(otherErr))
	})
}

func TestAsSnapshotError(t *testing.T) {
	snapErr := NewSnapshotError(ErrorTypeNotFound, "not found")
	otherErr := errors.New("other error")

	t.Run("direct snapshot error", func(t *testing.T) {
		var target *SnapshotError
		result := AsSnapshotError(snapErr, &target)
		assert.True(t, result)
		assert.Equal(t, snapErr, target)
	})

	t.Run("other error type", func(t *testing.T) {
		var target *SnapshotError
		result := AsSnapshotError(otherErr, &target)
		assert.False(t, result)
		assert.Nil(t, target)
	})

	t.Run("nil error", func(t *testing.T) {
		var target *SnapshotError
		result := AsSnapshotError(nil, &target)
		assert.False(t, result)
		assert.Nil(t, target)
	})
}

func TestAsZFSError(t *testing.T) {
	zfsErr := &ZFSError{Operation: "send", Dataset: "tank"}
	otherErr := errors.New("other error")

	t.Run("direct zfs error", func(t *testing.T) {
		var target *ZFSError
		result := AsZFSError(zfsErr, &target)
		assert.True(t, result)
		assert.Equal(t, zfsErr, target)
	})

	t.Run("other error type", func(t *testing.T) {
		var target *ZFSError
		result := AsZFSError(otherErr, &target)
		assert.False(t, result)
		assert.Nil(t, target)
	})
}

func TestAsS3Error(t *testing.T) {
	s3Err := &S3Error{Operation: "upload", Bucket: "test"}
	otherErr := errors.New("other error")

	t.Run("direct s3 error", func(t *testing.T) {
		var target *S3Error
		result := AsS3Error(s3Err, &target)
		assert.True(t, result)
		assert.Equal(t, s3Err, target)
	})

	t.Run("other error type", func(t *testing.T) {
		var target *S3Error
		result := AsS3Error(otherErr, &target)
		assert.False(t, result)
		assert.Nil(t, target)
	})
}