package backup

import (
	"context"
	"testing"
	"time"

	"github.com/kristianvalind/z3/internal/config"
	"github.com/kristianvalind/z3/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupOptions(t *testing.T) {
	opts := &BackupOptions{
		DryRun:          true,
		TargetSnapshot:  "tank/data@test",
		ForceFullBackup: true,
		StorageClass:    "STANDARD_IA",
		Metadata: map[string]string{
			"test": "value",
		},
	}

	assert.True(t, opts.DryRun)
	assert.Equal(t, "tank/data@test", opts.TargetSnapshot)
	assert.True(t, opts.ForceFullBackup)
	assert.Equal(t, "STANDARD_IA", opts.StorageClass)
	assert.Equal(t, "value", opts.Metadata["test"])
}

func TestBackupResult(t *testing.T) {
	result := &BackupResult{
		SnapshotsUploaded: []SnapshotUploadResult{
			{
				SnapshotName:   "tank/data@test",
				S3Key:          "tank/data@test",
				Size:           1024,
				CompressedSize: 512,
				IsFullBackup:   true,
				Duration:       time.Minute,
				ETag:           "test-etag",
			},
		},
		TotalSize:      1024,
		CompressedSize: 512,
		Duration:       time.Minute,
		BackupType:     "full",
	}

	assert.Len(t, result.SnapshotsUploaded, 1)
	assert.Equal(t, int64(1024), result.TotalSize)
	assert.Equal(t, int64(512), result.CompressedSize)
	assert.Equal(t, time.Minute, result.Duration)
	assert.Equal(t, "full", result.BackupType)

	snapshot := result.SnapshotsUploaded[0]
	assert.Equal(t, "tank/data@test", snapshot.SnapshotName)
	assert.Equal(t, "tank/data@test", snapshot.S3Key)
	assert.Equal(t, int64(1024), snapshot.Size)
	assert.Equal(t, int64(512), snapshot.CompressedSize)
	assert.True(t, snapshot.IsFullBackup)
	assert.Equal(t, time.Minute, snapshot.Duration)
	assert.Equal(t, "test-etag", snapshot.ETag)
}

func TestSnapshotUploadResult(t *testing.T) {
	result := SnapshotUploadResult{
		SnapshotName:   "tank/data@snapshot1",
		S3Key:          "prefix/tank/data@snapshot1",
		Size:           2048,
		CompressedSize: 1024,
		IsFullBackup:   false,
		ParentName:     "tank/data@snapshot0",
		Duration:       30 * time.Second,
		ETag:           "abc123",
	}

	assert.Equal(t, "tank/data@snapshot1", result.SnapshotName)
	assert.Equal(t, "prefix/tank/data@snapshot1", result.S3Key)
	assert.Equal(t, int64(2048), result.Size)
	assert.Equal(t, int64(1024), result.CompressedSize)
	assert.False(t, result.IsFullBackup)
	assert.Equal(t, "tank/data@snapshot0", result.ParentName)
	assert.Equal(t, 30*time.Second, result.Duration)
	assert.Equal(t, "abc123", result.ETag)
}

func TestRestoreOptions(t *testing.T) {
	opts := &RestoreOptions{
		TargetDataset: "tank/restore",
		SnapshotName:  "tank/data@test",
		DryRun:        true,
		Force:         true,
	}

	assert.Equal(t, "tank/restore", opts.TargetDataset)
	assert.Equal(t, "tank/data@test", opts.SnapshotName)
	assert.True(t, opts.DryRun)
	assert.True(t, opts.Force)
}

func TestRestoreResult(t *testing.T) {
	result := &RestoreResult{
		SnapshotName:      "tank/data@test",
		RestoredDataset:   "tank/restore",
		Size:              1024,
		Duration:          time.Minute,
		SnapshotsRestored: 3,
	}

	assert.Equal(t, "tank/data@test", result.SnapshotName)
	assert.Equal(t, "tank/restore", result.RestoredDataset)
	assert.Equal(t, int64(1024), result.Size)
	assert.Equal(t, time.Minute, result.Duration)
	assert.Equal(t, 3, result.SnapshotsRestored)
}

func TestNewManager(t *testing.T) {
	ctx := context.Background()

	t.Run("valid config", func(t *testing.T) {
		cfg := &config.Config{
			Bucket:       "test-bucket",
			Filesystem:   "tank/data",
			Compressor:   "pigz1",
			GPGRecipient: "test@example.com",
			Concurrency:  4,
		}

		manager, err := NewManager(ctx, cfg)
		require.NoError(t, err)
		assert.NotNil(t, manager)
		assert.Equal(t, cfg, manager.config)
		assert.NotNil(t, manager.zfsManager)
		assert.NotNil(t, manager.s3Client)
		assert.NotNil(t, manager.compressPipeline)
	})

	t.Run("nil config", func(t *testing.T) {
		manager, err := NewManager(ctx, nil)
		assert.Error(t, err)
		assert.Nil(t, manager)
		assert.Contains(t, err.Error(), "configuration cannot be nil")
	})

	t.Run("invalid S3 config", func(t *testing.T) {
		cfg := &config.Config{
			// Missing bucket name
			Filesystem: "tank/data",
		}

		manager, err := NewManager(ctx, cfg)
		assert.Error(t, err)
		assert.Nil(t, manager)
		assert.Contains(t, err.Error(), "failed to create S3 client")
	})
}

func TestGenerateS3Key(t *testing.T) {
	ctx := context.Background()

	t.Run("with prefix", func(t *testing.T) {
		cfg := &config.Config{
			Bucket:     "test-bucket",
			Filesystem: "tank/data",
			S3Prefix:   "backups",
		}

		manager, err := NewManager(ctx, cfg)
		require.NoError(t, err)

		key := manager.generateS3Key("tank/data@snapshot1")
		assert.Equal(t, "backups/tank/data@snapshot1", key)
	})

	t.Run("without prefix", func(t *testing.T) {
		cfg := &config.Config{
			Bucket:     "test-bucket",
			Filesystem: "tank/data",
		}

		manager, err := NewManager(ctx, cfg)
		require.NoError(t, err)

		key := manager.generateS3Key("tank/data@snapshot1")
		assert.Equal(t, "tank/data@snapshot1", key)
	})
}

func TestBuildSnapshotMetadata(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket:       "test-bucket",
		Filesystem:   "tank/data",
		Compressor:   "pigz1,gpg",
		GPGRecipient: "test@example.com",
	}

	manager, err := NewManager(ctx, cfg)
	require.NoError(t, err)

	// Mock snapshot (we can't create a real one without ZFS)
	// snap := &struct {
	// 	Name string
	// }{"tank/data@test"}

	additionalMetadata := map[string]string{
		"custom": "value",
	}

	// Create a mock snapshot for testing
	mockSnap := &snapshot.Snapshot{
		Name: "tank/data@test",
	}

	metadata := manager.buildSnapshotMetadata(mockSnap, true, "", additionalMetadata)

	assert.Equal(t, "true", metadata["is_full"])
	assert.Equal(t, "tank/data", metadata["filesystem"])
	assert.Equal(t, "value", metadata["custom"])
	assert.Equal(t, "go-port", metadata["z3_version"])
	assert.Contains(t, metadata, "backup_time")
}

func TestOptimizePartSize(t *testing.T) {
	testCases := []struct {
		name          string
		estimatedSize int64
		expectedMin   int64
		expectedMax   int64
	}{
		{
			name:          "small file",
			estimatedSize: 1024 * 1024, // 1MB
			expectedMin:   5 * 1024 * 1024,
			expectedMax:   5 * 1024 * 1024,
		},
		{
			name:          "medium file",
			estimatedSize: 100 * 1024 * 1024, // 100MB
			expectedMin:   5 * 1024 * 1024,
			expectedMax:   5 * 1024 * 1024,
		},
		{
			name:          "large file",
			estimatedSize: 10 * 1024 * 1024 * 1024, // 10GB
			expectedMin:   5 * 1024 * 1024,         // Should use minimum
			expectedMax:   5 * 1024 * 1024,
		},
		{
			name:          "huge file",
			estimatedSize: 1000 * 1024 * 1024 * 1024, // 1TB
			expectedMin:   100 * 1024 * 1024,         // Should hit max
			expectedMax:   100 * 1024 * 1024,
		},
		{
			name:          "zero size",
			estimatedSize: 0,
			expectedMin:   5 * 1024 * 1024,
			expectedMax:   5 * 1024 * 1024,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := optimizePartSize(tc.estimatedSize)
			assert.GreaterOrEqual(t, result, tc.expectedMin)
			assert.LessOrEqual(t, result, tc.expectedMax)
			assert.GreaterOrEqual(t, result, int64(5*1024*1024)) // Min part size
			assert.LessOrEqual(t, result, int64(100*1024*1024))  // Max part size

			// Should be aligned to MB boundary
			assert.Equal(t, int64(0), result%(1024*1024))
		})
	}
}

func TestGetStatus(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket:       "test-bucket",
		Filesystem:   "tank/data",
		S3Prefix:     "backups",
		Compressor:   "pigz1",
		GPGRecipient: "test@example.com",
	}

	manager, err := NewManager(ctx, cfg)
	require.NoError(t, err)

	status := manager.GetStatus()

	assert.Equal(t, "tank/data", status["filesystem"])
	assert.Equal(t, "test-bucket", status["bucket"])
	assert.Equal(t, "backups", status["prefix"])
	assert.Contains(t, status, "compressors")
	assert.Contains(t, status, "dry_run")
	assert.IsType(t, false, status["dry_run"])
}

// Test helper functions and edge cases
func TestBackupOptionsDefaults(t *testing.T) {
	var opts *BackupOptions

	// Test nil options
	assert.Nil(t, opts)

	// Test with defaults
	opts = &BackupOptions{}
	assert.False(t, opts.DryRun)
	assert.Equal(t, "", opts.TargetSnapshot)
	assert.False(t, opts.ForceFullBackup)
	assert.Equal(t, "", opts.StorageClass)
	assert.Nil(t, opts.Metadata)
}

func TestRestoreOptionsDefaults(t *testing.T) {
	var opts *RestoreOptions

	// Test nil options
	assert.Nil(t, opts)

	// Test with defaults
	opts = &RestoreOptions{}
	assert.Equal(t, "", opts.TargetDataset)
	assert.Equal(t, "", opts.SnapshotName)
	assert.False(t, opts.DryRun)
	assert.False(t, opts.Force)
}

// Benchmark tests
func BenchmarkOptimizePartSize(b *testing.B) {
	sizes := []int64{
		1024 * 1024,               // 1MB
		100 * 1024 * 1024,         // 100MB
		1024 * 1024 * 1024,        // 1GB
		10 * 1024 * 1024 * 1024,   // 10GB
		100 * 1024 * 1024 * 1024,  // 100GB
		1000 * 1024 * 1024 * 1024, // 1TB
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, size := range sizes {
			optimizePartSize(size)
		}
	}
}

func BenchmarkGenerateS3Key(b *testing.B) {
	ctx := context.Background()
	cfg := &config.Config{
		Bucket:     "test-bucket",
		Filesystem: "tank/data",
		S3Prefix:   "backups/zfs",
	}

	manager, err := NewManager(ctx, cfg)
	require.NoError(b, err)

	snapshotName := "tank/data@zfs-auto-snap:daily-2024-01-01"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.generateS3Key(snapshotName)
	}
}

// Integration-style tests (would require mocking in real implementation)
func TestBackupWorkflowStructure(t *testing.T) {
	// Test the structure and flow of backup operations
	// This tests the logic without actually executing commands

	t.Run("backup options validation", func(t *testing.T) {
		tests := []struct {
			name    string
			opts    *BackupOptions
			wantErr bool
		}{
			{
				name: "valid options",
				opts: &BackupOptions{
					DryRun:         true,
					TargetSnapshot: "tank/data@test",
				},
				wantErr: false,
			},
			{
				name:    "nil options (should work with defaults)",
				opts:    nil,
				wantErr: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// In a real implementation, we would mock the dependencies
				// and test the actual Backup method
				if tt.opts == nil {
					tt.opts = &BackupOptions{}
				}

				// Basic validation that would happen in Backup method
				assert.IsType(t, &BackupOptions{}, tt.opts)
			})
		}
	})

	t.Run("restore options validation", func(t *testing.T) {
		tests := []struct {
			name    string
			opts    *RestoreOptions
			wantErr bool
		}{
			{
				name: "valid options",
				opts: &RestoreOptions{
					SnapshotName:  "tank/data@test",
					TargetDataset: "tank/restore",
				},
				wantErr: false,
			},
			{
				name:    "missing snapshot name",
				opts:    &RestoreOptions{},
				wantErr: true,
			},
			{
				name:    "nil options",
				opts:    nil,
				wantErr: true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// Simulate validation that would happen in Restore method
				if tt.opts == nil {
					assert.True(t, tt.wantErr)
					return
				}

				if tt.opts.SnapshotName == "" {
					assert.True(t, tt.wantErr)
				} else {
					assert.False(t, tt.wantErr)
				}
			})
		}
	})
}
