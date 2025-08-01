package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	// MinPartSize is the minimum size for a multipart upload part (5MB)
	MinPartSize int64 = 5 * 1024 * 1024

	// MaxPartSize is the maximum size for a multipart upload part (100MB)
	MaxPartSize int64 = 100 * 1024 * 1024

	// MaxParts is the maximum number of parts allowed in a multipart upload
	MaxParts = 10000

	// DefaultPartSize is the default part size (50MB)
	DefaultPartSize int64 = 50 * 1024 * 1024

	// DefaultConcurrency is the default number of concurrent upload workers
	DefaultConcurrency = 4
)

// MultipartUploader handles multipart uploads to S3
type MultipartUploader struct {
	client       *Client
	uploadID     string
	key          string
	partSize     int64
	concurrency  int
	parts        []types.CompletedPart
	partsMutex   sync.RWMutex
	errors       []error
	errorsMutex  sync.RWMutex
}

// MultipartUploadOptions contains options for multipart uploads
type MultipartUploadOptions struct {
	PartSize             int64
	Concurrency          int
	ContentType          string
	StorageClass         string
	ServerSideEncryption string
	Metadata             map[string]string
	ACL                  string
}

// PartUploadResult contains the result of uploading a single part
type PartUploadResult struct {
	PartNumber int32
	ETag       string
	Size       int64
	MD5Hash    string
	Error      error
}

// MultipartUploadResult contains the final result of a multipart upload
type MultipartUploadResult struct {
	Key       string
	ETag      string
	Location  string
	VersionId string
	Parts     []types.CompletedPart
}

// NewMultipartUploader creates a new multipart uploader
func (c *Client) NewMultipartUploader(ctx context.Context, key string, opts *MultipartUploadOptions) (*MultipartUploader, error) {
	if opts == nil {
		opts = &MultipartUploadOptions{
			PartSize:    DefaultPartSize,
			Concurrency: DefaultConcurrency,
		}
	}

	// Validate part size
	if opts.PartSize < MinPartSize {
		opts.PartSize = MinPartSize
	}
	if opts.PartSize > MaxPartSize {
		opts.PartSize = MaxPartSize
	}

	// Validate concurrency
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultConcurrency
	}

	// Create multipart upload input
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
	}

	// Apply options
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if opts.StorageClass != "" {
		input.StorageClass = types.StorageClass(opts.StorageClass)
	}
	if opts.ServerSideEncryption != "" {
		input.ServerSideEncryption = types.ServerSideEncryption(opts.ServerSideEncryption)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}
	if opts.ACL != "" {
		input.ACL = types.ObjectCannedACL(opts.ACL)
	}

	// Initiate multipart upload
	output, err := c.s3Client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate multipart upload: %w", err)
	}

	return &MultipartUploader{
		client:      c,
		uploadID:    aws.ToString(output.UploadId),
		key:         key,
		partSize:    opts.PartSize,
		concurrency: opts.Concurrency,
		parts:       make([]types.CompletedPart, 0),
	}, nil
}

// UploadFromReader uploads data from a reader using multipart upload
func (mu *MultipartUploader) UploadFromReader(ctx context.Context, reader io.Reader, estimatedSize int64) (*MultipartUploadResult, error) {
	// Optimize part size based on estimated size if provided
	if estimatedSize > 0 {
		mu.optimizePartSize(estimatedSize)
	}

	// Create channels for coordinating uploads
	partChan := make(chan *partData, mu.concurrency*2)
	resultChan := make(chan *PartUploadResult, mu.concurrency*2)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < mu.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.uploadWorker(ctx, partChan, resultChan)
		}()
	}

	// Start result collector
	var collectorWG sync.WaitGroup
	collectorWG.Add(1)
	go func() {
		defer collectorWG.Done()
		mu.resultCollector(resultChan)
	}()

	// Read and queue parts
	partNumber := int32(1)
	buffer := make([]byte, mu.partSize)

	for {
		n, err := io.ReadFull(reader, buffer)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			close(partChan)
			return nil, fmt.Errorf("failed to read data: %w", err)
		}

		if n == 0 {
			break
		}

		// Create part data (copy buffer to avoid data races)
		partBuffer := make([]byte, n)
		copy(partBuffer, buffer[:n])

		part := &partData{
			PartNumber: partNumber,
			Data:       partBuffer,
		}

		select {
		case partChan <- part:
			partNumber++
		case <-ctx.Done():
			close(partChan)
			return nil, ctx.Err()
		}

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
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
	mu.errorsMutex.RLock()
	if len(mu.errors) > 0 {
		firstError := mu.errors[0]
		mu.errorsMutex.RUnlock()
		
		// Abort the upload on error
		mu.Abort(ctx)
		return nil, fmt.Errorf("upload failed: %w", firstError)
	}
	mu.errorsMutex.RUnlock()

	// Complete the multipart upload
	return mu.Complete(ctx)
}

// uploadWorker processes part uploads
func (mu *MultipartUploader) uploadWorker(ctx context.Context, partChan <-chan *partData, resultChan chan<- *PartUploadResult) {
	for part := range partChan {
		result := mu.uploadPart(ctx, part)
		select {
		case resultChan <- result:
		case <-ctx.Done():
			return
		}
	}
}

// resultCollector collects upload results
func (mu *MultipartUploader) resultCollector(resultChan <-chan *PartUploadResult) {
	for result := range resultChan {
		if result.Error != nil {
			mu.errorsMutex.Lock()
			mu.errors = append(mu.errors, result.Error)
			mu.errorsMutex.Unlock()
		} else {
			mu.partsMutex.Lock()
			mu.parts = append(mu.parts, types.CompletedPart{
				ETag:       aws.String(result.ETag),
				PartNumber: aws.Int32(result.PartNumber),
			})
			mu.partsMutex.Unlock()
		}
	}
}

// uploadPart uploads a single part
func (mu *MultipartUploader) uploadPart(ctx context.Context, part *partData) *PartUploadResult {
	// Calculate MD5 hash
	hash := md5.Sum(part.Data)
	md5Hash := hex.EncodeToString(hash[:])

	// Upload the part with retries
	maxRetries := 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff
			backoff := time.Duration(attempt*attempt) * time.Second
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return &PartUploadResult{
					PartNumber: part.PartNumber,
					Error:      ctx.Err(),
				}
			}
		}

		output, err := mu.client.s3Client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(mu.client.bucketName),
			Key:        aws.String(mu.key),
			PartNumber: aws.Int32(part.PartNumber),
			UploadId:   aws.String(mu.uploadID),
			Body:       &partReader{data: part.Data},
		})

		if err != nil {
			lastErr = err
			continue
		}

		return &PartUploadResult{
			PartNumber: part.PartNumber,
			ETag:       aws.ToString(output.ETag),
			Size:       int64(len(part.Data)),
			MD5Hash:    md5Hash,
		}
	}

	return &PartUploadResult{
		PartNumber: part.PartNumber,
		Error:      fmt.Errorf("failed to upload part %d after %d attempts: %w", part.PartNumber, maxRetries, lastErr),
	}
}

// Complete completes the multipart upload
func (mu *MultipartUploader) Complete(ctx context.Context) (*MultipartUploadResult, error) {
	if len(mu.parts) == 0 {
		return nil, fmt.Errorf("no parts to complete")
	}

	// Sort parts by part number
	sort.Slice(mu.parts, func(i, j int) bool {
		return aws.ToInt32(mu.parts[i].PartNumber) < aws.ToInt32(mu.parts[j].PartNumber)
	})

	output, err := mu.client.s3Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(mu.client.bucketName),
		Key:      aws.String(mu.key),
		UploadId: aws.String(mu.uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: mu.parts,
		},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return &MultipartUploadResult{
		Key:       mu.key,
		ETag:      aws.ToString(output.ETag),
		Location:  aws.ToString(output.Location),
		VersionId: aws.ToString(output.VersionId),
		Parts:     mu.parts,
	}, nil
}

// Abort aborts the multipart upload
func (mu *MultipartUploader) Abort(ctx context.Context) error {
	_, err := mu.client.s3Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(mu.client.bucketName),
		Key:      aws.String(mu.key),
		UploadId: aws.String(mu.uploadID),
	})

	if err != nil {
		return fmt.Errorf("failed to abort multipart upload: %w", err)
	}

	return nil
}

// optimizePartSize optimizes the part size based on estimated total size
func (mu *MultipartUploader) optimizePartSize(estimatedSize int64) {
	if estimatedSize <= 0 {
		return
	}

	// Calculate optimal part size to stay under MaxParts limit
	optimalPartSize := estimatedSize / MaxParts
	if optimalPartSize < MinPartSize {
		optimalPartSize = MinPartSize
	}
	if optimalPartSize > MaxPartSize {
		optimalPartSize = MaxPartSize
	}

	// Round up to nearest MB for cleaner sizes
	optimalPartSize = ((optimalPartSize-1)/1024/1024 + 1) * 1024 * 1024

	mu.partSize = optimalPartSize
}

// partData represents a single part to be uploaded
type partData struct {
	PartNumber int32
	Data       []byte
}

// partReader implements io.ReadSeeker for part data
type partReader struct {
	data   []byte
	offset int64
}

func (pr *partReader) Read(p []byte) (n int, err error) {
	if pr.offset >= int64(len(pr.data)) {
		return 0, io.EOF
	}

	n = copy(p, pr.data[pr.offset:])
	pr.offset += int64(n)
	return n, nil
}

func (pr *partReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		pr.offset = offset
	case io.SeekCurrent:
		pr.offset += offset
	case io.SeekEnd:
		pr.offset = int64(len(pr.data)) + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if pr.offset < 0 {
		pr.offset = 0
	}
	if pr.offset > int64(len(pr.data)) {
		pr.offset = int64(len(pr.data))
	}

	return pr.offset, nil
}

// ComputeMultipartETag computes the ETag for a multipart upload
func ComputeMultipartETag(partETags []string) string {
	if len(partETags) == 1 {
		return partETags[0]
	}

	hash := md5.New()
	for _, etag := range partETags {
		// Remove quotes from ETag if present
		cleanETag := etag
		if len(cleanETag) > 2 && cleanETag[0] == '"' && cleanETag[len(cleanETag)-1] == '"' {
			cleanETag = cleanETag[1 : len(cleanETag)-1]
		}

		// Decode hex and hash
		if decoded, err := hex.DecodeString(cleanETag); err == nil {
			hash.Write(decoded)
		}
	}

	return fmt.Sprintf("\"%x-%d\"", hash.Sum(nil), len(partETags))
}