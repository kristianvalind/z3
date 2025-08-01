package s3

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	z3config "github.com/kristianvalind/z3/internal/config"
	"github.com/kristianvalind/z3/pkg/snapshot"
)

// Client wraps AWS S3 client with Z3-specific functionality
type Client struct {
	// S3 is the underlying AWS S3 client
	s3Client *s3.Client

	// Config holds the Z3 configuration
	config *z3config.Config

	// BucketName is the S3 bucket to operate on
	bucketName string

	// Region is the AWS region
	region string

	// Endpoint allows custom S3 endpoints (e.g., MinIO)
	endpoint string
}

// ClientOptions contains options for creating an S3 client
type ClientOptions struct {
	// Region specifies the AWS region (optional, will use config or default)
	Region string

	// Endpoint specifies custom S3 endpoint (optional)
	Endpoint string

	// Credentials specifies custom credentials (optional)
	Credentials aws.CredentialsProvider
}

// NewClient creates a new S3 client from Z3 configuration
func NewClient(ctx context.Context, cfg *z3config.Config, opts *ClientOptions) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration cannot be nil")
	}

	if cfg.Bucket == "" {
		return nil, fmt.Errorf("bucket name is required in configuration")
	}

	// Prepare AWS config options
	var configOpts []func(*config.LoadOptions) error

	// Set region
	region := "us-east-1" // Default region
	if opts != nil && opts.Region != "" {
		region = opts.Region
	} else if cfg.AWSRegion != "" {
		region = cfg.AWSRegion
	}
	configOpts = append(configOpts, config.WithRegion(region))

	// Set credentials if provided in Z3 config
	if cfg.S3KeyID != "" && cfg.S3Secret != "" {
		creds := credentials.NewStaticCredentialsProvider(cfg.S3KeyID, cfg.S3Secret, "")
		configOpts = append(configOpts, config.WithCredentialsProvider(creds))
	} else if opts != nil && opts.Credentials != nil {
		configOpts = append(configOpts, config.WithCredentialsProvider(opts.Credentials))
	}

	// Load AWS config
	awsConfig, err := config.LoadDefaultConfig(ctx, configOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client options
	var s3Opts []func(*s3.Options)

	// Set custom endpoint if specified
	endpoint := ""
	if opts != nil && opts.Endpoint != "" {
		endpoint = opts.Endpoint
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // Required for custom endpoints like MinIO
		})
	} else if cfg.Host != "" {
		endpoint = cfg.Host
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}

	// Create S3 client
	s3Client := s3.NewFromConfig(awsConfig, s3Opts...)

	return &Client{
		s3Client:   s3Client,
		config:     cfg,
		bucketName: cfg.Bucket,
		region:     region,
		endpoint:   endpoint,
	}, nil
}

// GetBucketName returns the configured bucket name
func (c *Client) GetBucketName() string {
	return c.bucketName
}

// GetRegion returns the configured region
func (c *Client) GetRegion() string {
	return c.region
}

// GetEndpoint returns the configured endpoint
func (c *Client) GetEndpoint() string {
	return c.endpoint
}

// ListObjects lists objects in the bucket with the given prefix
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	paginator := s3.NewListObjectsV2Paginator(c.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucketName),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		output, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range output.Contents {
			objects = append(objects, ObjectInfo{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
				ETag:         aws.ToString(obj.ETag),
				StorageClass: string(obj.StorageClass),
			})
		}
	}

	return objects, nil
}

// HeadObject retrieves metadata for an object without downloading it
func (c *Client) HeadObject(ctx context.Context, key string) (*ObjectInfo, error) {
	output, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to head object %s: %w", key, err)
	}

	return &ObjectInfo{
		Key:          key,
		Size:         aws.ToInt64(output.ContentLength),
		LastModified: aws.ToTime(output.LastModified),
		ETag:         aws.ToString(output.ETag),
		ContentType:  aws.ToString(output.ContentType),
		Metadata:     output.Metadata,
	}, nil
}

// GetObject downloads an object and writes it to the provided writer
func (c *Client) GetObject(ctx context.Context, key string, writer io.Writer) error {
	output, err := c.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to get object %s: %w", key, err)
	}
	defer output.Body.Close()

	_, err = io.Copy(writer, output.Body)
	if err != nil {
		return fmt.Errorf("failed to copy object data: %w", err)
	}

	return nil
}

// PutObject uploads data from a reader to S3
func (c *Client) PutObject(ctx context.Context, key string, reader io.Reader, opts *PutObjectOptions) (*PutObjectResult, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
		Body:   reader,
	}

	// Apply options if provided
	if opts != nil {
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
	}

	output, err := c.s3Client.PutObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to put object %s: %w", key, err)
	}

	return &PutObjectResult{
		ETag:      aws.ToString(output.ETag),
		VersionId: aws.ToString(output.VersionId),
	}, nil
}

// DeleteObject deletes an object from S3
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object %s: %w", key, err)
	}

	return nil
}

// ObjectExists checks if an object exists in S3
func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// ListSnapshots lists all snapshots (objects with snapshot naming pattern) in the bucket
func (c *Client) ListSnapshots(ctx context.Context, filesystem string) (snapshot.SnapshotList, error) {
	// Use filesystem name as prefix to filter snapshots
	prefix := filesystem + "@"
	if filesystem == "" {
		prefix = "" // List all if no filesystem specified
	}

	objects, err := c.ListObjects(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshot objects: %w", err)
	}

	var snapshots snapshot.SnapshotList
	for _, obj := range objects {
		// Parse object key to extract snapshot information
		if strings.Contains(obj.Key, "@") {
			snap := &snapshot.Snapshot{
				Name:           obj.Key,
				Size:           obj.Size,
				CompressedSize: obj.Size, // S3 objects are already compressed if uploaded compressed
				CreatedAt:      obj.LastModified,
				Metadata: map[string]string{
					"etag":          obj.ETag,
					"storage_class": obj.StorageClass,
				},
			}

			// Extract filesystem from key
			parts := strings.Split(obj.Key, "@")
			if len(parts) == 2 {
				snap.Metadata["filesystem"] = parts[0]
				snap.Metadata["snapshot_name"] = parts[1]
			}

			snapshots = append(snapshots, snap)
		}
	}

	return snapshots, nil
}

// ObjectInfo contains information about an S3 object
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
	ETag         string
	ContentType  string
	StorageClass string
	Metadata     map[string]string
}

// PutObjectOptions contains options for uploading objects
type PutObjectOptions struct {
	ContentType          string
	StorageClass         string
	ServerSideEncryption string
	Metadata             map[string]string
	ACL                  string
}

// PutObjectResult contains the result of a put operation
type PutObjectResult struct {
	ETag      string
	VersionId string
}
