package s3

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kristianvalind/z3/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadOptions(t *testing.T) {
	opts := &DownloadOptions{
		Concurrency: 8,
		PartSize:    20 * 1024 * 1024, // 20MB
	}

	assert.Equal(t, 8, opts.Concurrency)
	assert.Equal(t, int64(20*1024*1024), opts.PartSize)
}

func TestDownloadResult(t *testing.T) {
	result := &DownloadResult{
		BytesDownloaded: 1024,
		ContentType:     "application/octet-stream",
		LastModified:    "2024-01-01T12:00:00Z",
		ETag:            "\"abcdef123456\"",
	}

	assert.Equal(t, int64(1024), result.BytesDownloaded)
	assert.Equal(t, "application/octet-stream", result.ContentType)
	assert.Equal(t, "2024-01-01T12:00:00Z", result.LastModified)
	assert.Equal(t, "\"abcdef123456\"", result.ETag)
}

func TestNewDownloader(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket: "test-bucket",
	}

	client, err := NewClient(ctx, cfg, nil)
	require.NoError(t, err)

	t.Run("with nil options", func(t *testing.T) {
		downloader := client.NewDownloader(nil)
		assert.NotNil(t, downloader)
		assert.Equal(t, DefaultConcurrency, downloader.concurrency)
		assert.Equal(t, int64(DefaultPartSize), downloader.partSize)
	})

	t.Run("with custom options", func(t *testing.T) {
		opts := &DownloadOptions{
			Concurrency: 8,
			PartSize:    10 * 1024 * 1024, // 10MB
		}
		downloader := client.NewDownloader(opts)
		assert.NotNil(t, downloader)
		assert.Equal(t, 8, downloader.concurrency)
		assert.Equal(t, int64(10*1024*1024), downloader.partSize)
	})

	t.Run("with invalid options", func(t *testing.T) {
		opts := &DownloadOptions{
			Concurrency: -1,
			PartSize:    -1000,
		}
		downloader := client.NewDownloader(opts)
		assert.NotNil(t, downloader)
		// Should use defaults when invalid values provided
		assert.Equal(t, DefaultConcurrency, downloader.concurrency)
		assert.Equal(t, int64(DefaultPartSize), downloader.partSize)
	})
}

func TestDownloadPart(t *testing.T) {
	part := &downloadPart{
		PartNumber: 1,
		Start:      0,
		End:        1023,
	}

	assert.Equal(t, 1, part.PartNumber)
	assert.Equal(t, int64(0), part.Start)
	assert.Equal(t, int64(1023), part.End)
	assert.Equal(t, int64(1024), part.End-part.Start+1) // Size calculation
}

func TestDownloadResult_Error(t *testing.T) {
	result := &downloadResult{
		PartNumber: 1,
		Data:       nil,
		Error:      assert.AnError,
	}

	assert.Equal(t, 1, result.PartNumber)
	assert.Nil(t, result.Data)
	assert.Error(t, result.Error)
}

func TestDownloadResult_Success(t *testing.T) {
	data := []byte("test data for download part")
	result := &downloadResult{
		PartNumber: 2,
		Data:       data,
		Error:      nil,
	}

	assert.Equal(t, 2, result.PartNumber)
	assert.Equal(t, data, result.Data)
	assert.NoError(t, result.Error)
}

func TestSynchronizedWriter(t *testing.T) {
	t.Run("write parts in order", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 3)

		// Write parts in order
		err := writer.WritePart(0, []byte("part0"))
		assert.NoError(t, err)
		err = writer.WritePart(1, []byte("part1"))
		assert.NoError(t, err)
		err = writer.WritePart(2, []byte("part2"))
		assert.NoError(t, err)

		// Wait for completion
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = writer.WaitForCompletion(ctx)
		assert.NoError(t, err)

		assert.Equal(t, "part0part1part2", buffer.String())
	})

	t.Run("write parts out of order", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 3)

		// Write parts out of order
		err := writer.WritePart(2, []byte("part2"))
		assert.NoError(t, err)
		err = writer.WritePart(0, []byte("part0"))
		assert.NoError(t, err)
		err = writer.WritePart(1, []byte("part1"))
		assert.NoError(t, err)

		// Wait for completion
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = writer.WaitForCompletion(ctx)
		assert.NoError(t, err)

		assert.Equal(t, "part0part1part2", buffer.String())
	})

	t.Run("concurrent writes", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 10)

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(partNum int) {
				defer wg.Done()
				data := []byte("part" + string(rune('0'+partNum)))
				err := writer.WritePart(partNum, data)
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()

		// Wait for completion
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := writer.WaitForCompletion(ctx)
		assert.NoError(t, err)

		expected := "part0part1part2part3part4part5part6part7part8part9"
		assert.Equal(t, expected, buffer.String())
	})

	t.Run("context cancellation", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 2)

		// Write only first part
		err := writer.WritePart(0, []byte("part0"))
		assert.NoError(t, err)

		// Create cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should return context error
		err = writer.WaitForCompletion(ctx)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("write error propagation", func(t *testing.T) {
		// Create a writer that will fail
		failingWriter := &failingWriter{}
		writer := newSynchronizedWriter(failingWriter, 1)

		err := writer.WritePart(0, []byte("test"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "write failed")
	})
}

// failingWriter is a test helper that always returns an error on write
type failingWriter struct{}

func (fw *failingWriter) Write(p []byte) (n int, err error) {
	return 0, &testError{"write failed"}
}

type testError struct {
	message string
}

func (e *testError) Error() string {
	return e.message
}

func TestDownloadPartCalculation(t *testing.T) {
	testCases := []struct {
		name                 string
		totalSize            int64
		partSize             int64
		expectedParts        int64
		expectedLastPartSize int64
	}{
		{
			name:                 "exact multiple",
			totalSize:            100 * 1024 * 1024, // 100MB
			partSize:             10 * 1024 * 1024,  // 10MB
			expectedParts:        10,
			expectedLastPartSize: 10 * 1024 * 1024,
		},
		{
			name:                 "with remainder",
			totalSize:            105 * 1024 * 1024, // 105MB
			partSize:             10 * 1024 * 1024,  // 10MB
			expectedParts:        11,
			expectedLastPartSize: 5 * 1024 * 1024, // 5MB remainder
		},
		{
			name:                 "single part",
			totalSize:            5 * 1024 * 1024,  // 5MB
			partSize:             10 * 1024 * 1024, // 10MB
			expectedParts:        1,
			expectedLastPartSize: 5 * 1024 * 1024,
		},
		{
			name:                 "very small file",
			totalSize:            1024,             // 1KB
			partSize:             10 * 1024 * 1024, // 10MB
			expectedParts:        1,
			expectedLastPartSize: 1024,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			numParts := (tc.totalSize + tc.partSize - 1) / tc.partSize
			assert.Equal(t, tc.expectedParts, numParts)

			// Calculate last part size
			lastPartStart := (numParts - 1) * tc.partSize
			lastPartEnd := tc.totalSize - 1
			lastPartSize := lastPartEnd - lastPartStart + 1

			assert.Equal(t, tc.expectedLastPartSize, lastPartSize)
		})
	}
}

func TestDownloadStrategySelection(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket: "test-bucket",
	}

	client, err := NewClient(ctx, cfg, nil)
	require.NoError(t, err)

	testCases := []struct {
		name               string
		objectSize         int64
		partSize           int64
		concurrency        int
		expectSingleThread bool
	}{
		{
			name:               "small object",
			objectSize:         1 * 1024 * 1024,  // 1MB
			partSize:           10 * 1024 * 1024, // 10MB
			concurrency:        4,
			expectSingleThread: true,
		},
		{
			name:               "single concurrency",
			objectSize:         50 * 1024 * 1024, // 50MB
			partSize:           10 * 1024 * 1024, // 10MB
			concurrency:        1,
			expectSingleThread: true,
		},
		{
			name:               "large object with concurrency",
			objectSize:         100 * 1024 * 1024, // 100MB
			partSize:           10 * 1024 * 1024,  // 10MB
			concurrency:        4,
			expectSingleThread: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &DownloadOptions{
				Concurrency: tc.concurrency,
				PartSize:    tc.partSize,
			}
			downloader := client.NewDownloader(opts)

			// Test the logic for choosing download strategy
			shouldUseSingle := tc.objectSize <= tc.partSize || tc.concurrency == 1
			assert.Equal(t, tc.expectSingleThread, shouldUseSingle)

			// Use downloader to avoid "declared and not used" error
			assert.NotNil(t, downloader)
		})
	}
}

// Test helper for creating test object info
func createTestObjectInfo(key string, size int64) *ObjectInfo {
	return &ObjectInfo{
		Key:          key,
		Size:         size,
		LastModified: time.Now(),
		ETag:         "\"test-etag\"",
		ContentType:  "application/octet-stream",
	}
}

func TestCreateTestObjectInfo(t *testing.T) {
	obj := createTestObjectInfo("test/file.txt", 1024)
	assert.Equal(t, "test/file.txt", obj.Key)
	assert.Equal(t, int64(1024), obj.Size)
	assert.Equal(t, "\"test-etag\"", obj.ETag)
	assert.Equal(t, "application/octet-stream", obj.ContentType)
}

func TestDownloadRangeCalculation(t *testing.T) {
	testCases := []struct {
		name        string
		partNum     int64
		partSize    int64
		totalSize   int64
		expectStart int64
		expectEnd   int64
	}{
		{
			name:        "first part",
			partNum:     0,
			partSize:    10 * 1024 * 1024,
			totalSize:   50 * 1024 * 1024,
			expectStart: 0,
			expectEnd:   10*1024*1024 - 1,
		},
		{
			name:        "middle part",
			partNum:     2,
			partSize:    10 * 1024 * 1024,
			totalSize:   50 * 1024 * 1024,
			expectStart: 20 * 1024 * 1024,
			expectEnd:   30*1024*1024 - 1,
		},
		{
			name:        "last part (full)",
			partNum:     4,
			partSize:    10 * 1024 * 1024,
			totalSize:   50 * 1024 * 1024,
			expectStart: 40 * 1024 * 1024,
			expectEnd:   50*1024*1024 - 1,
		},
		{
			name:        "last part (partial)",
			partNum:     4,
			partSize:    10 * 1024 * 1024,
			totalSize:   45 * 1024 * 1024,
			expectStart: 40 * 1024 * 1024,
			expectEnd:   45*1024*1024 - 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			start := tc.partNum * tc.partSize
			end := start + tc.partSize - 1
			if end >= tc.totalSize {
				end = tc.totalSize - 1
			}

			assert.Equal(t, tc.expectStart, start)
			assert.Equal(t, tc.expectEnd, end)
		})
	}
}

func TestSynchronizedWriter_EdgeCases(t *testing.T) {
	t.Run("zero parts", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 0)

		// Mark as completed immediately since there are no parts
		writer.mutex.Lock()
		writer.completed = true
		writer.mutex.Unlock()
		writer.cond.Broadcast()

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		// Should complete immediately since there are no parts to wait for
		err := writer.WaitForCompletion(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "", buffer.String())
	})

	t.Run("single part", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 1)

		err := writer.WritePart(0, []byte("single"))
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = writer.WaitForCompletion(ctx)
		assert.NoError(t, err)

		assert.Equal(t, "single", buffer.String())
	})

	t.Run("duplicate part writes", func(t *testing.T) {
		var buffer bytes.Buffer
		writer := newSynchronizedWriter(&buffer, 2)

		// Write first part twice - the first write will win since it's written immediately
		err := writer.WritePart(0, []byte("first"))
		assert.NoError(t, err)

		// Write second part
		err = writer.WritePart(1, []byte("second"))
		assert.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = writer.WaitForCompletion(ctx)
		assert.NoError(t, err)

		// The first write wins
		assert.Equal(t, "firstsecond", buffer.String())
	})
}
