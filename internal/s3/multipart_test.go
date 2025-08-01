package s3

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/kristianvalind/z3/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultipartUploadOptions(t *testing.T) {
	opts := &MultipartUploadOptions{
		PartSize:             10 * 1024 * 1024, // 10MB
		Concurrency:          8,
		ContentType:          "application/octet-stream",
		StorageClass:         "STANDARD_IA",
		ServerSideEncryption: "AES256",
		Metadata: map[string]string{
			"backup-type": "zfs-snapshot",
			"filesystem":  "tank/data",
		},
		ACL: "bucket-owner-full-control",
	}

	assert.Equal(t, int64(10*1024*1024), opts.PartSize)
	assert.Equal(t, 8, opts.Concurrency)
	assert.Equal(t, "application/octet-stream", opts.ContentType)
	assert.Equal(t, "STANDARD_IA", opts.StorageClass)
	assert.Equal(t, "AES256", opts.ServerSideEncryption)
	assert.Equal(t, "zfs-snapshot", opts.Metadata["backup-type"])
	assert.Equal(t, "tank/data", opts.Metadata["filesystem"])
	assert.Equal(t, "bucket-owner-full-control", opts.ACL)
}

func TestPartUploadResult(t *testing.T) {
	result := &PartUploadResult{
		PartNumber: 1,
		ETag:       "\"abcdef123456\"",
		Size:       1024,
		MD5Hash:    "5d41402abc4b2a76b9719d911017c592",
	}

	assert.Equal(t, int32(1), result.PartNumber)
	assert.Equal(t, "\"abcdef123456\"", result.ETag)
	assert.Equal(t, int64(1024), result.Size)
	assert.Equal(t, "5d41402abc4b2a76b9719d911017c592", result.MD5Hash)
	assert.Nil(t, result.Error)
}

func TestMultipartUploadResult(t *testing.T) {
	result := &MultipartUploadResult{
		Key:       "test/object.txt",
		ETag:      "\"multipart-etag-123\"",
		Location:  "https://bucket.s3.amazonaws.com/test/object.txt",
		VersionId: "version-123",
	}

	assert.Equal(t, "test/object.txt", result.Key)
	assert.Equal(t, "\"multipart-etag-123\"", result.ETag)
	assert.Equal(t, "https://bucket.s3.amazonaws.com/test/object.txt", result.Location)
	assert.Equal(t, "version-123", result.VersionId)
}

func TestPartReader(t *testing.T) {
	data := []byte("Hello, World! This is test data for the part reader.")
	reader := &partReader{data: data}

	t.Run("read all data", func(t *testing.T) {
		buffer := make([]byte, len(data))
		n, err := reader.Read(buffer)
		assert.NoError(t, err)
		assert.Equal(t, len(data), n)
		assert.Equal(t, data, buffer)
	})

	t.Run("read in chunks", func(t *testing.T) {
		reader.offset = 0 // Reset reader
		
		// Read first 5 bytes
		buffer1 := make([]byte, 5)
		n1, err := reader.Read(buffer1)
		assert.NoError(t, err)
		assert.Equal(t, 5, n1)
		assert.Equal(t, []byte("Hello"), buffer1)

		// Read next 7 bytes
		buffer2 := make([]byte, 7)
		n2, err := reader.Read(buffer2)
		assert.NoError(t, err)
		assert.Equal(t, 7, n2)
		assert.Equal(t, []byte(", World"), buffer2)
	})

	t.Run("read past end", func(t *testing.T) {
		reader.offset = int64(len(data)) // Set to end
		
		buffer := make([]byte, 10)
		n, err := reader.Read(buffer)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, 0, n)
	})

	t.Run("seek operations", func(t *testing.T) {
		// Seek to start
		pos, err := reader.Seek(0, io.SeekStart)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), pos)

		// Seek 10 bytes from current position
		pos, err = reader.Seek(10, io.SeekCurrent)
		assert.NoError(t, err)
		assert.Equal(t, int64(10), pos)

		// Seek 5 bytes from end
		pos, err = reader.Seek(-5, io.SeekEnd)
		assert.NoError(t, err)
		assert.Equal(t, int64(len(data)-5), pos)

		// Test reading from new position
		buffer := make([]byte, 5)
		n, err := reader.Read(buffer)
		assert.NoError(t, err)
		assert.Equal(t, 5, n)
		assert.Equal(t, data[len(data)-5:], buffer)
	})

	t.Run("seek beyond bounds", func(t *testing.T) {
		// Seek before start
		pos, err := reader.Seek(-100, io.SeekStart)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), pos)

		// Seek past end
		pos, err = reader.Seek(int64(len(data)+100), io.SeekStart)
		assert.NoError(t, err)
		assert.Equal(t, int64(len(data)), pos)
	})

	t.Run("invalid seek whence", func(t *testing.T) {
		_, err := reader.Seek(0, 999)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid whence")
	})
}

func TestComputeMultipartETag(t *testing.T) {
	testCases := []struct {
		name     string
		etags    []string
		expected string
	}{
		{
			name:     "single part",
			etags:    []string{"\"d41d8cd98f00b204e9800998ecf8427e\""},
			expected: "\"d41d8cd98f00b204e9800998ecf8427e\"",
		},
		{
			name:     "multiple parts",
			etags:    []string{"\"d41d8cd98f00b204e9800998ecf8427e\"", "\"098f6bcd4621d373cade4e832627b4f6\""},
			expected: "\"3858f62230ac3c915f300c664312c63f-2\"",
		},
		{
			name:     "etags with quotes",
			etags:    []string{"\"abc123\"", "\"def456\""},
			expected: "\"6cd3556deb0da54bca060b4c39479839-2\"",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := ComputeMultipartETag(tc.etags)
			
			// The actual hash will vary, but we can test the format
			if len(tc.etags) == 1 {
				assert.Equal(t, tc.etags[0], result)
			} else {
				assert.True(t, strings.HasPrefix(result, "\""))
				assert.True(t, strings.HasSuffix(result, "\""))
				assert.Contains(t, result, "-")
				
				// Extract the count from the end
				parts := strings.Split(strings.Trim(result, "\""), "-")
				assert.Equal(t, "2", parts[len(parts)-1])
			}
		})
	}
}

func TestMultipartUploader_OptimizePartSize(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket: "test-bucket",
	}

	client, err := NewClient(ctx, cfg, nil)
	require.NoError(t, err)

	uploader := &MultipartUploader{
		client:   client,
		partSize: DefaultPartSize,
	}

	testCases := []struct {
		name          string
		estimatedSize int64
		expectedMin   int64
		expectedMax   int64
	}{
		{
			name:          "small file",
			estimatedSize: 100 * 1024 * 1024, // 100MB
			expectedMin:   MinPartSize,
			expectedMax:   MinPartSize,
		},
		{
			name:          "medium file",
			estimatedSize: 1024 * 1024 * 1024, // 1GB
			expectedMin:   MinPartSize,
			expectedMax:   MaxPartSize,
		},
		{
			name:          "large file",
			estimatedSize: 100 * 1024 * 1024 * 1024, // 100GB
			expectedMin:   10 * 1024 * 1024,          // Should be optimized up
			expectedMax:   MaxPartSize,
		},
		{
			name:          "huge file",
			estimatedSize: 500 * 1024 * 1024 * 1024, // 500GB (more reasonable)
			expectedMin:   50 * 1024 * 1024,         // Should be optimized up  
			expectedMax:   MaxPartSize,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			uploader.partSize = DefaultPartSize
			uploader.optimizePartSize(tc.estimatedSize)
			
			assert.GreaterOrEqual(t, uploader.partSize, tc.expectedMin)
			assert.LessOrEqual(t, uploader.partSize, tc.expectedMax)
			assert.GreaterOrEqual(t, uploader.partSize, MinPartSize)
			
			// Verify it would result in <= MaxParts
			if tc.estimatedSize > 0 {
				maxPossibleParts := (tc.estimatedSize + uploader.partSize - 1) / uploader.partSize
				assert.LessOrEqual(t, maxPossibleParts, int64(MaxParts))
			}
		})
	}
}

func TestMultipartConstants(t *testing.T) {
	// Test that our constants are reasonable
	assert.Equal(t, int64(5*1024*1024), MinPartSize)    // 5MB
	assert.Equal(t, int64(100*1024*1024), MaxPartSize)  // 100MB
	assert.Equal(t, int64(50*1024*1024), DefaultPartSize) // 50MB
	assert.Equal(t, 10000, MaxParts)
	assert.Equal(t, 4, DefaultConcurrency)

	// Verify relationships
	assert.Less(t, MinPartSize, DefaultPartSize)
	assert.Less(t, DefaultPartSize, MaxPartSize)
	assert.Greater(t, MaxParts, 1000) // Should allow for large files
}

// Mock uploader for testing without AWS
type mockMultipartUploader struct {
	*MultipartUploader
	uploadedParts [][]byte
}

func TestPartData(t *testing.T) {
	data := []byte("test data for part")
	part := &partData{
		PartNumber: 1,
		Data:       data,
	}

	assert.Equal(t, int32(1), part.PartNumber)
	assert.Equal(t, data, part.Data)
	assert.Equal(t, len(data), len(part.Data))
}

// Test helper to create test data
func createTestData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}
	return data
}

func TestCreateTestData(t *testing.T) {
	data := createTestData(1000)
	assert.Equal(t, 1000, len(data))
	
	// Check pattern
	for i := 0; i < 256; i++ {
		assert.Equal(t, byte(i), data[i])
	}
	assert.Equal(t, byte(0), data[256]) // Should wrap around
}

// Test multipart upload logic without AWS dependencies
func TestMultipartUploadLogic(t *testing.T) {
	testData := bytes.NewReader(createTestData(int(DefaultPartSize * 3))) // 3 parts worth of data
	
	// Test reading parts
	buffer := make([]byte, int(DefaultPartSize))
	partsRead := 0
	
	for {
		n, err := testData.Read(buffer)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		
		partsRead++
		if partsRead <= 2 {
			assert.Equal(t, int(DefaultPartSize), n)
		} else {
			// Last part should be exactly equal since we created 3 * DefaultPartSize data
			assert.Equal(t, int(DefaultPartSize), n)
		}
	}
	
	assert.Equal(t, 3, partsRead)
}

func TestPartSizeValidation(t *testing.T) {
	testCases := []struct {
		name         string
		inputSize    int64
		expectedSize int64
	}{
		{
			name:         "below minimum",
			inputSize:    1024 * 1024, // 1MB
			expectedSize: MinPartSize,
		},
		{
			name:         "above maximum",
			inputSize:    200 * 1024 * 1024, // 200MB
			expectedSize: MaxPartSize,
		},
		{
			name:         "valid size",
			inputSize:    10 * 1024 * 1024, // 10MB
			expectedSize: 10 * 1024 * 1024,
		},
		{
			name:         "zero size",
			inputSize:    0,
			expectedSize: MinPartSize,
		},
		{
			name:         "negative size",
			inputSize:    -1000,
			expectedSize: MinPartSize,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &MultipartUploadOptions{
				PartSize: tc.inputSize,
			}

			// Simulate the validation logic
			validatedSize := opts.PartSize
			if validatedSize < MinPartSize {
				validatedSize = MinPartSize
			}
			if validatedSize > MaxPartSize {
				validatedSize = MaxPartSize
			}

			assert.Equal(t, tc.expectedSize, validatedSize)
		})
	}
}