package backup

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kristianvalind/z3/internal/compress"
	"github.com/kristianvalind/z3/internal/config"
	"github.com/kristianvalind/z3/internal/s3"
	"github.com/kristianvalind/z3/internal/zfs"
	"github.com/kristianvalind/z3/pkg/snapshot"
)

// snapshotListManager implements SnapshotManager for SnapshotList
type snapshotListManager struct {
	snapshots snapshot.SnapshotList
}

func (slm *snapshotListManager) List(ctx context.Context) (snapshot.SnapshotList, error) {
	return slm.snapshots, nil
}

func (slm *snapshotListManager) Get(ctx context.Context, name string) (*snapshot.Snapshot, error) {
	for _, snap := range slm.snapshots {
		if snap.Name == name {
			return snap, nil
		}
	}
	return nil, nil
}

func (slm *snapshotListManager) GetLatest(ctx context.Context) (*snapshot.Snapshot, error) {
	if len(slm.snapshots) == 0 {
		return nil, nil
	}
	return slm.snapshots[len(slm.snapshots)-1], nil
}

func (slm *snapshotListManager) Exists(ctx context.Context, name string) (bool, error) {
	snap, _ := slm.Get(ctx, name)
	return snap != nil, nil
}

func (slm *snapshotListManager) GetPrefix() string {
	return ""
}

func (slm *snapshotListManager) GetFilesystem() string {
	return ""
}

// Manager orchestrates backup operations combining ZFS, compression, and S3
type Manager struct {
	config          *config.Config
	zfsManager      *zfs.Manager
	s3Client        *s3.Client
	compressPipeline *compress.Pipeline
}

// BackupOptions contains options for backup operations
type BackupOptions struct {
	DryRun          bool
	TargetSnapshot  string
	ForceFullBackup bool
	StorageClass    string
	Metadata        map[string]string
}

// BackupResult contains the result of a backup operation
type BackupResult struct {
	SnapshotsUploaded []SnapshotUploadResult
	TotalSize         int64
	CompressedSize    int64
	Duration          time.Duration
	BackupType        string // "full" or "incremental"
}

// SnapshotUploadResult contains the result of uploading a single snapshot
type SnapshotUploadResult struct {
	SnapshotName   string
	S3Key          string
	Size           int64
	CompressedSize int64
	IsFullBackup   bool
	ParentName     string
	Duration       time.Duration
	ETag           string
}

// RestoreOptions contains options for restore operations
type RestoreOptions struct {
	TargetDataset   string
	SnapshotName    string
	DryRun          bool
	Force           bool
}

// RestoreResult contains the result of a restore operation
type RestoreResult struct {
	SnapshotName     string
	RestoredDataset  string
	Size             int64
	Duration         time.Duration
	SnapshotsRestored int
}

// NewManager creates a new backup manager
func NewManager(ctx context.Context, cfg *config.Config) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration cannot be nil")
	}

	// Create ZFS manager
	zfsManager := zfs.NewManager(cfg)

	// Create S3 client
	s3Client, err := s3.NewClient(ctx, cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}

	// Create compression pipeline
	compressorTypes := compress.ParseCompressorTypes(cfg.Compressor)
	compressPipeline := compress.NewDefaultPipeline(compressorTypes, cfg.GPGRecipient)

	return &Manager{
		config:          cfg,
		zfsManager:      zfsManager,
		s3Client:        s3Client,
		compressPipeline: compressPipeline,
	}, nil
}

// Backup performs a backup operation (full or incremental)
func (m *Manager) Backup(ctx context.Context, opts *BackupOptions) (*BackupResult, error) {
	startTime := time.Now()

	if opts == nil {
		opts = &BackupOptions{}
	}

	// Set dry run mode
	m.zfsManager.SetDryRun(opts.DryRun)

	// Get local snapshots
	localSnapshots, err := m.zfsManager.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local snapshots: %w", err)
	}

	if len(localSnapshots) == 0 {
		return nil, fmt.Errorf("no local snapshots found for filesystem %s", m.zfsManager.GetFilesystem())
	}

	// Determine target snapshot
	var targetSnapshot *snapshot.Snapshot
	if opts.TargetSnapshot != "" {
		targetSnapshot = localSnapshots.FindByName(opts.TargetSnapshot)
		if targetSnapshot == nil {
			return nil, fmt.Errorf("target snapshot %s not found", opts.TargetSnapshot)
		}
	} else {
		targetSnapshot = localSnapshots.GetLatest()
	}

	// Get remote snapshots
	remoteSnapshots, err := m.s3Client.ListSnapshots(ctx, m.zfsManager.GetFilesystem())
	if err != nil {
		return nil, fmt.Errorf("failed to list remote snapshots: %w", err)
	}

	// Validate remote snapshots health
	for _, remoteSnap := range remoteSnapshots {
		manager := &snapshotListManager{snapshots: remoteSnapshots}
		if !remoteSnap.IsHealthy(manager) {
			return nil, fmt.Errorf("remote snapshot %s is unhealthy", remoteSnap.Name)
		}
	}

	// Determine snapshots to upload
	var snapshotsToUpload snapshot.SnapshotList
	var backupType string

	if opts.ForceFullBackup || len(remoteSnapshots) == 0 {
		// Full backup
		snapshotsToUpload = snapshot.SnapshotList{targetSnapshot}
		backupType = "full"
	} else {
		// Get snapshots needed for incremental backup
		snapshotsToUpload, err = m.zfsManager.GetSnapshotsToSend(ctx, remoteSnapshots, targetSnapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to determine snapshots to send: %w", err)
		}
		backupType = "incremental"
	}

	if len(snapshotsToUpload) == 0 {
		return &BackupResult{
			Duration:   time.Since(startTime),
			BackupType: backupType,
		}, nil
	}

	// Upload snapshots
	var uploadResults []SnapshotUploadResult
	var totalSize, totalCompressedSize int64

	for i, snapToUpload := range snapshotsToUpload {
		uploadResult, err := m.uploadSnapshot(ctx, snapToUpload, snapshotsToUpload, i, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to upload snapshot %s: %w", snapToUpload.Name, err)
		}

		uploadResults = append(uploadResults, *uploadResult)
		totalSize += uploadResult.Size
		totalCompressedSize += uploadResult.CompressedSize
	}

	return &BackupResult{
		SnapshotsUploaded: uploadResults,
		TotalSize:         totalSize,
		CompressedSize:    totalCompressedSize,
		Duration:          time.Since(startTime),
		BackupType:        backupType,
	}, nil
}

// uploadSnapshot uploads a single snapshot to S3
func (m *Manager) uploadSnapshot(ctx context.Context, snap *snapshot.Snapshot, allSnapshots snapshot.SnapshotList, index int, opts *BackupOptions) (*SnapshotUploadResult, error) {
	uploadStart := time.Now()

	// Generate S3 key
	s3Key := m.generateS3Key(snap.Name)

	// Determine if this is a full backup
	isFullBackup := index == 0 && (len(allSnapshots) == 1 || snap.IsFullBackup)
	var parentName string
	if !isFullBackup && index > 0 {
		parentName = allSnapshots[index-1].Name
	}

	// Get estimated size
	var estimatedSize int64
	var err error
	if isFullBackup {
		estimatedSize, err = m.zfsManager.GetSendSize(ctx, snap)
	} else {
		parentSnap := allSnapshots[index-1]
		estimatedSize, err = m.zfsManager.GetIncrementalSendSize(ctx, parentSnap, snap)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get estimated size: %w", err)
	}

	if opts.DryRun {
		return &SnapshotUploadResult{
			SnapshotName:   snap.Name,
			S3Key:          s3Key,
			Size:           estimatedSize,
			CompressedSize: estimatedSize, // Can't estimate compression in dry run
			IsFullBackup:   isFullBackup,
			ParentName:     parentName,
			Duration:       time.Since(uploadStart),
		}, nil
	}

	// Create multipart uploader
	uploadOpts := &s3.MultipartUploadOptions{
		PartSize:    optimizePartSize(estimatedSize),
		Concurrency: m.config.Concurrency,
		ContentType: "application/octet-stream",
		Metadata:    m.buildSnapshotMetadata(snap, isFullBackup, parentName, opts.Metadata),
	}

	if opts.StorageClass != "" {
		uploadOpts.StorageClass = opts.StorageClass
	} else if m.config.S3StorageClass != "" {
		uploadOpts.StorageClass = m.config.S3StorageClass
	}

	uploader, err := m.s3Client.NewMultipartUploader(ctx, s3Key, uploadOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart uploader: %w", err)
	}

	// Create compression pipeline
	reader, writer := io.Pipe()
	compressedWriter, err := m.compressPipeline.Compress(ctx, writer)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, fmt.Errorf("failed to create compression pipeline: %w", err)
	}

	// Start ZFS send in a goroutine
	var zfsSendErr error
	go func() {
		defer writer.Close()
		defer compressedWriter.Close()

		if isFullBackup {
			zfsSendErr = m.zfsManager.Send(ctx, snap, compressedWriter)
		} else {
			parentSnap := allSnapshots[index-1]
			zfsSendErr = m.zfsManager.SendIncremental(ctx, parentSnap, snap, compressedWriter)
		}
	}()

	// Upload the compressed stream
	uploadResult, err := uploader.UploadFromReader(ctx, reader, estimatedSize)
	if err != nil {
		reader.Close()
		return nil, fmt.Errorf("failed to upload to S3: %w", err)
	}

	// Check for ZFS send errors
	if zfsSendErr != nil {
		// Try to abort the upload
		uploader.Abort(ctx)
		return nil, fmt.Errorf("ZFS send failed: %w", zfsSendErr)
	}

	// For now, we'll estimate compressed size based on the actual uploaded data
	// TODO: Track actual part sizes during upload for more accurate measurement
	var compressedSize int64 = estimatedSize // Placeholder until we implement size tracking
	
	// In the future, we can track this by modifying the multipart uploader
	// to keep track of actual uploaded bytes per part

	return &SnapshotUploadResult{
		SnapshotName:   snap.Name,
		S3Key:          s3Key,
		Size:           estimatedSize,
		CompressedSize: compressedSize,
		IsFullBackup:   isFullBackup,
		ParentName:     parentName,
		Duration:       time.Since(uploadStart),
		ETag:           uploadResult.ETag,
	}, nil
}

// Restore restores a snapshot from S3
func (m *Manager) Restore(ctx context.Context, opts *RestoreOptions) (*RestoreResult, error) {
	startTime := time.Now()

	if opts == nil {
		return nil, fmt.Errorf("restore options cannot be nil")
	}

	if opts.SnapshotName == "" {
		return nil, fmt.Errorf("snapshot name is required")
	}

	m.zfsManager.SetDryRun(opts.DryRun)

	// Get remote snapshots to find the one to restore
	remoteSnapshots, err := m.s3Client.ListSnapshots(ctx, m.zfsManager.GetFilesystem())
	if err != nil {
		return nil, fmt.Errorf("failed to list remote snapshots: %w", err)
	}

	targetSnapshot := remoteSnapshots.FindByName(opts.SnapshotName)
	if targetSnapshot == nil {
		return nil, fmt.Errorf("snapshot %s not found in remote storage", opts.SnapshotName)
	}

	// Build restoration chain
	restorationChain := m.buildRestorationChain(targetSnapshot, remoteSnapshots)

	var totalSize int64
	snapshotsRestored := 0

	// Restore snapshots in order
	for _, snapToRestore := range restorationChain {
		err := m.restoreSnapshot(ctx, snapToRestore, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to restore snapshot %s: %w", snapToRestore.Name, err)
		}
		totalSize += snapToRestore.Size
		snapshotsRestored++
	}

	targetDataset := opts.TargetDataset
	if targetDataset == "" {
		targetDataset = m.zfsManager.GetFilesystem()
	}

	return &RestoreResult{
		SnapshotName:      opts.SnapshotName,
		RestoredDataset:   targetDataset,
		Size:              totalSize,
		Duration:          time.Since(startTime),
		SnapshotsRestored: snapshotsRestored,
	}, nil
}

// restoreSnapshot restores a single snapshot from S3
func (m *Manager) restoreSnapshot(ctx context.Context, snap *snapshot.Snapshot, opts *RestoreOptions) error {
	s3Key := m.generateS3Key(snap.Name)

	if opts.DryRun {
		return nil // Just simulate
	}

	// Create decompression pipeline
	reader, writer := io.Pipe()
	decompressedReader, err := m.compressPipeline.Decompress(ctx, reader)
	if err != nil {
		reader.Close()
		writer.Close()
		return fmt.Errorf("failed to create decompression pipeline: %w", err)
	}

	// Start S3 download in a goroutine
	var s3DownloadErr error
	go func() {
		defer writer.Close()
		s3DownloadErr = m.s3Client.GetObject(ctx, s3Key, writer)
	}()

	// Receive the decompressed stream
	receiveOpts := snapshot.ReceiveOptions{
		Dataset: opts.TargetDataset,
		DryRun:  opts.DryRun,
		Force:   opts.Force,
	}

	err = m.zfsManager.Receive(ctx, decompressedReader, receiveOpts)
	if err != nil {
		reader.Close()
		decompressedReader.Close()
		return fmt.Errorf("ZFS receive failed: %w", err)
	}

	// Check for S3 download errors
	if s3DownloadErr != nil {
		return fmt.Errorf("S3 download failed: %w", s3DownloadErr)
	}

	return nil
}

// Helper methods

func (m *Manager) generateS3Key(snapshotName string) string {
	if m.config.S3Prefix != "" {
		return fmt.Sprintf("%s/%s", m.config.S3Prefix, snapshotName)
	}
	return snapshotName
}

func (m *Manager) buildSnapshotMetadata(snap *snapshot.Snapshot, isFullBackup bool, parentName string, additionalMetadata map[string]string) map[string]string {
	metadata := make(map[string]string)

	// Core metadata
	metadata["snapshot_name"] = snap.Name
	metadata["is_full"] = fmt.Sprintf("%t", isFullBackup)
	metadata["backup_time"] = time.Now().UTC().Format(time.RFC3339)
	metadata["filesystem"] = m.zfsManager.GetFilesystem()

	if parentName != "" {
		metadata["parent"] = parentName
	}

	// Compression metadata
	for k, v := range m.compressPipeline.GetMetadata() {
		metadata[k] = v
	}

	// Additional metadata
	for k, v := range additionalMetadata {
		metadata[k] = v
	}

	// Z3 version info
	metadata["z3_version"] = "go-port"

	return metadata
}

func (m *Manager) buildRestorationChain(targetSnapshot *snapshot.Snapshot, allSnapshots snapshot.SnapshotList) snapshot.SnapshotList {
	var chain snapshot.SnapshotList
	current := targetSnapshot

	// Build chain backwards from target to full backup
	for current != nil {
		chain = append(snapshot.SnapshotList{current}, chain...) // Prepend
		
		if current.IsFullBackup {
			break
		}
		
		// Find parent
		current = allSnapshots.FindByName(current.ParentName)
	}

	// Sort by creation time to ensure proper order
	sort.Slice(chain, func(i, j int) bool {
		return chain[i].CreatedAt.Before(chain[j].CreatedAt)
	})

	return chain
}

func optimizePartSize(estimatedSize int64) int64 {
	// Use S3 multipart upload optimization logic
	const minPartSize = 5 * 1024 * 1024   // 5MB
	const maxPartSize = 100 * 1024 * 1024 // 100MB
	const maxParts = 10000

	if estimatedSize <= 0 {
		return minPartSize
	}

	optimalSize := estimatedSize / maxParts
	if optimalSize < minPartSize {
		optimalSize = minPartSize
	}
	if optimalSize > maxPartSize {
		optimalSize = maxPartSize
	}

	// Round up to nearest MB
	return ((optimalSize-1)/1024/1024 + 1) * 1024 * 1024
}

// GetStatus returns the current status of the backup manager
func (m *Manager) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"filesystem":    m.zfsManager.GetFilesystem(),
		"bucket":        m.s3Client.GetBucketName(),
		"prefix":        m.config.S3Prefix,
		"compressors":   m.compressPipeline.GetMetadata(),
		"dry_run":       m.zfsManager.GetDryRun(),
	}
}