package s3

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Downloader handles downloading objects from S3 with support for concurrent range requests
type Downloader struct {
	client      *Client
	concurrency int
	partSize    int64
}

// DownloadOptions contains options for downloading objects
type DownloadOptions struct {
	Concurrency int   // Number of concurrent download workers
	PartSize    int64 // Size of each download part for concurrent downloads
}

// DownloadResult contains the result of a download operation
type DownloadResult struct {
	BytesDownloaded int64
	ContentType     string
	LastModified    string
	ETag            string
}

// NewDownloader creates a new downloader
func (c *Client) NewDownloader(opts *DownloadOptions) *Downloader {
	if opts == nil {
		opts = &DownloadOptions{
			Concurrency: DefaultConcurrency,
			PartSize:    DefaultPartSize,
		}
	}

	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}
	if opts.PartSize <= 0 {
		opts.PartSize = DefaultPartSize
	}

	return &Downloader{
		client:      c,
		concurrency: opts.Concurrency,
		partSize:    opts.PartSize,
	}
}

// Download downloads an object from S3 to the provided writer
func (d *Downloader) Download(ctx context.Context, key string, writer io.Writer) (*DownloadResult, error) {
	// First, get object metadata to determine size and decide on download strategy
	objectInfo, err := d.client.HeadObject(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get object metadata: %w", err)
	}

	// For small objects or single-threaded downloads, use simple download
	if objectInfo.Size <= d.partSize || d.concurrency == 1 {
		return d.downloadSingle(ctx, key, writer, objectInfo)
	}

	// For large objects, use concurrent download
	return d.downloadConcurrent(ctx, key, writer, objectInfo)
}

// downloadSingle downloads an object in a single request
func (d *Downloader) downloadSingle(ctx context.Context, key string, writer io.Writer, objectInfo *ObjectInfo) (*DownloadResult, error) {
	output, err := d.client.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.client.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}
	defer output.Body.Close()

	bytesWritten, err := io.Copy(writer, output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to copy object data: %w", err)
	}

	return &DownloadResult{
		BytesDownloaded: bytesWritten,
		ContentType:     aws.ToString(output.ContentType),
		LastModified:    aws.ToTime(output.LastModified).Format("2006-01-02T15:04:05Z"),
		ETag:            aws.ToString(output.ETag),
	}, nil
}

// downloadConcurrent downloads an object using concurrent range requests
func (d *Downloader) downloadConcurrent(ctx context.Context, key string, writer io.Writer, objectInfo *ObjectInfo) (*DownloadResult, error) {
	// Calculate the number of parts needed
	totalSize := objectInfo.Size
	numParts := (totalSize + d.partSize - 1) / d.partSize

	// Create a synchronized writer to handle concurrent writes
	syncWriter := newSynchronizedWriter(writer, int(numParts))

	// Create channels for coordinating downloads
	partChan := make(chan *downloadPart, d.concurrency*2)
	resultChan := make(chan *downloadResult, d.concurrency*2)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < d.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.downloadWorker(ctx, key, partChan, resultChan)
		}()
	}

	// Start result collector
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)
	var totalBytesDownloaded int64
	var downloadErrors []error
	var errorsMutex sync.Mutex

	go func() {
		defer collectorWG.Done()
		for result := range resultChan {
			if result.Error != nil {
				errorsMutex.Lock()
				downloadErrors = append(downloadErrors, result.Error)
				errorsMutex.Unlock()
			} else {
				// Write the part data to the synchronized writer
				err := syncWriter.WritePart(result.PartNumber, result.Data)
				if err != nil {
					errorsMutex.Lock()
					downloadErrors = append(downloadErrors, fmt.Errorf("failed to write part %d: %w", result.PartNumber, err))
					errorsMutex.Unlock()
				} else {
					totalBytesDownloaded += int64(len(result.Data))
				}
			}
		}
	}()

	// Queue download parts
	for i := int64(0); i < numParts; i++ {
		start := i * d.partSize
		end := start + d.partSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}

		part := &downloadPart{
			PartNumber: int(i),
			Start:      start,
			End:        end,
		}

		select {
		case partChan <- part:
		case <-ctx.Done():
			close(partChan)
			return nil, ctx.Err()
		}
	}

	// Close part channel to signal workers to stop
	close(partChan)

	// Wait for all workers to complete
	wg.Wait()

	// Close result channel and wait for collector
	close(resultChan)
	collectorWG.Wait()

	// Check for errors
	errorsMutex.Lock()
	if len(downloadErrors) > 0 {
		firstError := downloadErrors[0]
		errorsMutex.Unlock()
		return nil, fmt.Errorf("download failed: %w", firstError)
	}
	errorsMutex.Unlock()

	// Wait for all parts to be written
	err := syncWriter.WaitForCompletion(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to complete download: %w", err)
	}

	return &DownloadResult{
		BytesDownloaded: totalBytesDownloaded,
		ContentType:     objectInfo.ContentType,
		LastModified:    objectInfo.LastModified.Format("2006-01-02T15:04:05Z"),
		ETag:            objectInfo.ETag,
	}, nil
}

// downloadWorker processes download parts
func (d *Downloader) downloadWorker(ctx context.Context, key string, partChan <-chan *downloadPart, resultChan chan<- *downloadResult) {
	for part := range partChan {
		result := d.downloadPart(ctx, key, part)
		select {
		case resultChan <- result:
		case <-ctx.Done():
			return
		}
	}
}

// downloadPart downloads a single part using range request
func (d *Downloader) downloadPart(ctx context.Context, key string, part *downloadPart) *downloadResult {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", part.Start, part.End)

	output, err := d.client.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(d.client.bucketName),
		Key:    aws.String(key),
		Range:  aws.String(rangeHeader),
	})
	if err != nil {
		return &downloadResult{
			PartNumber: part.PartNumber,
			Error:      fmt.Errorf("failed to download part %d: %w", part.PartNumber, err),
		}
	}
	defer output.Body.Close()

	// Read all data from the response body
	data, err := io.ReadAll(output.Body)
	if err != nil {
		return &downloadResult{
			PartNumber: part.PartNumber,
			Error:      fmt.Errorf("failed to read part %d data: %w", part.PartNumber, err),
		}
	}

	return &downloadResult{
		PartNumber: part.PartNumber,
		Data:       data,
	}
}

// downloadPart represents a single part to be downloaded
type downloadPart struct {
	PartNumber int
	Start      int64
	End        int64
}

// downloadResult contains the result of downloading a single part
type downloadResult struct {
	PartNumber int
	Data       []byte
	Error      error
}

// synchronizedWriter ensures parts are written in order
type synchronizedWriter struct {
	writer     io.Writer
	parts      map[int][]byte
	nextPart   int
	totalParts int
	mutex      sync.Mutex
	cond       *sync.Cond
	completed  bool
}

// newSynchronizedWriter creates a new synchronized writer
func newSynchronizedWriter(writer io.Writer, totalParts int) *synchronizedWriter {
	sw := &synchronizedWriter{
		writer:     writer,
		parts:      make(map[int][]byte),
		nextPart:   0,
		totalParts: totalParts,
	}
	sw.cond = sync.NewCond(&sw.mutex)
	return sw
}

// WritePart writes a part to the buffer and triggers writing if it's the next expected part
func (sw *synchronizedWriter) WritePart(partNumber int, data []byte) error {
	sw.mutex.Lock()
	defer sw.mutex.Unlock()

	// Store the part data
	sw.parts[partNumber] = data

	// Write consecutive parts starting from nextPart
	for {
		if partData, exists := sw.parts[sw.nextPart]; exists {
			_, err := sw.writer.Write(partData)
			if err != nil {
				return err
			}

			delete(sw.parts, sw.nextPart)
			sw.nextPart++

			// Check if we've written all parts
			if sw.nextPart >= sw.totalParts {
				sw.completed = true
				sw.cond.Broadcast()
				break
			}
		} else {
			break
		}
	}

	return nil
}

// WaitForCompletion waits for all parts to be written
func (sw *synchronizedWriter) WaitForCompletion(ctx context.Context) error {
	sw.mutex.Lock()
	defer sw.mutex.Unlock()

	// Wait for completion or context cancellation
	done := make(chan struct{})
	go func() {
		sw.mutex.Lock()
		defer sw.mutex.Unlock()
		for !sw.completed {
			sw.cond.Wait()
		}
		close(done)
	}()

	// Temporarily unlock to allow the goroutine to acquire the lock
	sw.mutex.Unlock()
	
	select {
	case <-done:
		sw.mutex.Lock() // Re-acquire lock for defer unlock
		return nil
	case <-ctx.Done():
		sw.mutex.Lock() // Re-acquire lock for defer unlock
		return ctx.Err()
	}
}