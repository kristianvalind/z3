package s3

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kristianvalind/z3/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	ctx := context.Background()

	t.Run("with valid config", func(t *testing.T) {
		cfg := &config.Config{
			Bucket:    "test-bucket",
			S3KeyID:   "test-key",
			S3Secret:  "test-secret",
			AWSRegion: "us-west-2",
		}

		client, err := NewClient(ctx, cfg, nil)
		require.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, "test-bucket", client.GetBucketName())
		assert.Equal(t, "us-west-2", client.GetRegion())
	})

	t.Run("with custom endpoint", func(t *testing.T) {
		cfg := &config.Config{
			Bucket: "test-bucket",
			Host:   "http://localhost:9000",
		}

		client, err := NewClient(ctx, cfg, nil)
		require.NoError(t, err)
		assert.Equal(t, "http://localhost:9000", client.GetEndpoint())
	})

	t.Run("with nil config", func(t *testing.T) {
		client, err := NewClient(ctx, nil, nil)
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "configuration cannot be nil")
	})

	t.Run("with empty bucket", func(t *testing.T) {
		cfg := &config.Config{}

		client, err := NewClient(ctx, cfg, nil)
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "bucket name is required")
	})

	t.Run("with options override", func(t *testing.T) {
		cfg := &config.Config{
			Bucket:    "test-bucket",
			AWSRegion: "us-east-1",
		}

		opts := &ClientOptions{
			Region:   "eu-west-1",
			Endpoint: "https://custom-endpoint.com",
		}

		client, err := NewClient(ctx, cfg, opts)
		require.NoError(t, err)
		assert.Equal(t, "eu-west-1", client.GetRegion())
		assert.Equal(t, "https://custom-endpoint.com", client.GetEndpoint())
	})
}

func TestObjectInfo(t *testing.T) {
	now := time.Now()
	obj := ObjectInfo{
		Key:          "test/object.txt",
		Size:         1024,
		LastModified: now,
		ETag:         "\"abcdef123456\"",
		ContentType:  "text/plain",
		StorageClass: "STANDARD",
		Metadata: map[string]string{
			"custom-key": "custom-value",
		},
	}

	assert.Equal(t, "test/object.txt", obj.Key)
	assert.Equal(t, int64(1024), obj.Size)
	assert.Equal(t, now, obj.LastModified)
	assert.Equal(t, "\"abcdef123456\"", obj.ETag)
	assert.Equal(t, "text/plain", obj.ContentType)
	assert.Equal(t, "STANDARD", obj.StorageClass)
	assert.Equal(t, "custom-value", obj.Metadata["custom-key"])
}

func TestPutObjectOptions(t *testing.T) {
	opts := &PutObjectOptions{
		ContentType:          "application/octet-stream",
		StorageClass:         "STANDARD_IA",
		ServerSideEncryption: "AES256",
		Metadata: map[string]string{
			"backup-type": "zfs-snapshot",
		},
		ACL: "bucket-owner-full-control",
	}

	assert.Equal(t, "application/octet-stream", opts.ContentType)
	assert.Equal(t, "STANDARD_IA", opts.StorageClass)
	assert.Equal(t, "AES256", opts.ServerSideEncryption)
	assert.Equal(t, "zfs-snapshot", opts.Metadata["backup-type"])
	assert.Equal(t, "bucket-owner-full-control", opts.ACL)
}

func TestPutObjectResult(t *testing.T) {
	result := &PutObjectResult{
		ETag:      "\"abcdef123456\"",
		VersionId: "version-123",
	}

	assert.Equal(t, "\"abcdef123456\"", result.ETag)
	assert.Equal(t, "version-123", result.VersionId)
}

// Mock tests - these would require actual S3 service or mocking framework
// For now, we'll test the client creation and configuration logic

func TestClient_Configuration(t *testing.T) {
	cfg := &config.Config{
		Bucket:    "test-bucket",
		S3KeyID:   "test-access-key",
		S3Secret:  "test-secret-key",
		AWSRegion: "us-west-2",
		Host:      "https://s3.amazonaws.com",
	}

	ctx := context.Background()
	client, err := NewClient(ctx, cfg, nil)
	require.NoError(t, err)

	// Test basic getters
	assert.Equal(t, "test-bucket", client.GetBucketName())
	assert.Equal(t, "us-west-2", client.GetRegion())
	assert.Equal(t, "https://s3.amazonaws.com", client.GetEndpoint())

	// Verify the client was configured
	assert.NotNil(t, client.s3Client)
	assert.NotNil(t, client.config)
}

func TestClient_SnapshotKeyParsing(t *testing.T) {
	// Test parsing of snapshot-like object keys
	testCases := []struct {
		name       string
		key        string
		expectSnap bool
		filesystem string
		snapshot   string
	}{
		{
			name:       "valid snapshot key",
			key:        "tank/data@zfs-auto-snap:daily-2024-01-01",
			expectSnap: true,
			filesystem: "tank/data",
			snapshot:   "zfs-auto-snap:daily-2024-01-01",
		},
		{
			name:       "simple snapshot key",
			key:        "pool@backup",
			expectSnap: true,
			filesystem: "pool",
			snapshot:   "backup",
		},
		{
			name:       "not a snapshot key",
			key:        "regular-file.txt",
			expectSnap: false,
		},
		{
			name:       "multiple @ symbols",
			key:        "tank@data@snapshot",
			expectSnap: true,
			filesystem: "tank",
			snapshot:   "data@snapshot",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hasAt := strings.Contains(tc.key, "@")
			assert.Equal(t, tc.expectSnap, hasAt)

			if tc.expectSnap {
				parts := strings.Split(tc.key, "@")
				if len(parts) >= 2 {
					assert.Equal(t, tc.filesystem, parts[0])
					assert.Equal(t, tc.snapshot, strings.Join(parts[1:], "@"))
				}
			}
		})
	}
}

func TestClientOptions_Validation(t *testing.T) {
	testCases := []struct {
		name        string
		opts        *ClientOptions
		expectValid bool
	}{
		{
			name:        "nil options",
			opts:        nil,
			expectValid: true, // Should use defaults
		},
		{
			name: "valid options",
			opts: &ClientOptions{
				Region:   "us-east-1",
				Endpoint: "https://s3.amazonaws.com",
			},
			expectValid: true,
		},
		{
			name: "custom endpoint",
			opts: &ClientOptions{
				Endpoint: "http://localhost:9000",
			},
			expectValid: true,
		},
	}

	cfg := &config.Config{
		Bucket: "test-bucket",
	}

	ctx := context.Background()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(ctx, cfg, tc.opts)
			if tc.expectValid {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			} else {
				assert.Error(t, err)
				assert.Nil(t, client)
			}
		})
	}
}

// Test helper function for generating test configurations
func createTestConfig(bucket, keyID, secret, region, host string) *config.Config {
	return &config.Config{
		Bucket:    bucket,
		S3KeyID:   keyID,
		S3Secret:  secret,
		AWSRegion: region,
		Host:      host,
	}
}

func TestCreateTestConfig(t *testing.T) {
	cfg := createTestConfig("test-bucket", "key", "secret", "us-west-2", "https://endpoint.com")

	assert.Equal(t, "test-bucket", cfg.Bucket)
	assert.Equal(t, "key", cfg.S3KeyID)
	assert.Equal(t, "secret", cfg.S3Secret)
	assert.Equal(t, "us-west-2", cfg.AWSRegion)
	assert.Equal(t, "https://endpoint.com", cfg.Host)
}

// Integration test setup helper (would be used with real S3 or LocalStack)
func setupTestClient(t *testing.T) *Client {
	cfg := &config.Config{
		Bucket: "test-bucket-" + strings.ToLower(t.Name()),
	}

	ctx := context.Background()
	client, err := NewClient(ctx, cfg, nil)
	require.NoError(t, err)

	return client
}

func TestSetupTestClient(t *testing.T) {
	// This test verifies our test helper works
	client := setupTestClient(t)
	assert.NotNil(t, client)
	assert.Contains(t, client.GetBucketName(), "test-bucket-")
}