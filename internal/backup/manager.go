package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
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
	config           *config.Config
	zfsManager       *zfs.Manager
	s3Client         *s3.Client
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
	TargetDataset string
	SnapshotName  string
	DryRun        bool
	Force         bool
}

// RestoreResult contains the result of a restore operation
type RestoreResult struct {
	SnapshotName      string
	RestoredDataset   string
	Size              int64
	Duration          time.Duration
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
	compressPipeline := compress.NewDefaultPipeline(compressorTypes, cfg.GetGPGRecipients())

	return &Manager{
		config:           cfg,
		zfsManager:       zfsManager,
		s3Client:         s3Client,
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
	remoteSnapshots, err := m.ListRemoteSnapshots(ctx)
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

	// Find the base snapshot for incremental backup
	var baseSnapshot *snapshot.Snapshot
	if !opts.ForceFullBackup && len(remoteSnapshots) > 0 {
		// Find the latest common snapshot between local and remote
		localSnapshots, err := m.zfsManager.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list local snapshots: %w", err)
		}

		for i := len(localSnapshots) - 1; i >= 0; i-- {
			localSnap := localSnapshots[i]
			if remoteSnapshots.FindByName(localSnap.Name) != nil {
				baseSnapshot = localSnap
				break
			}
		}
	}

	if opts.ForceFullBackup || len(remoteSnapshots) == 0 || baseSnapshot == nil {
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
		uploadResult, err := m.uploadSnapshot(ctx, snapToUpload, snapshotsToUpload, i, opts, baseSnapshot)
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
func (m *Manager) uploadSnapshot(ctx context.Context, snap *snapshot.Snapshot, allSnapshots snapshot.SnapshotList, index int, opts *BackupOptions, baseSnapshot *snapshot.Snapshot) (*SnapshotUploadResult, error) {
	uploadStart := time.Now()

	// Generate S3 key
	s3Key := m.generateS3Key(snap.Name)

	// Determine if this is a full backup
	// This should be a full backup only if:
	// 1. We're forcing full backup AND it's the first snapshot, OR
	// 2. There's no base snapshot (no common snapshot with remote)
	isFullBackup := (opts.ForceFullBackup && index == 0) || baseSnapshot == nil
	var parentName string
	if !isFullBackup {
		if index > 0 {
			// Use previous snapshot in the upload list as parent
			parentName = allSnapshots[index-1].Name
		} else if baseSnapshot != nil {
			// Use the base snapshot (latest common with remote) as parent
			parentName = baseSnapshot.Name
		}
	}

	// Get estimated size
	var estimatedSize int64
	var err error
	if isFullBackup {
		estimatedSize, err = m.zfsManager.GetSendSize(ctx, snap)
	} else {
		var parentSnap *snapshot.Snapshot
		if index > 0 {
			parentSnap = allSnapshots[index-1]
		} else if baseSnapshot != nil {
			parentSnap = baseSnapshot
		} else {
			return nil, fmt.Errorf("no parent snapshot available for incremental backup")
		}
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
			var parentSnap *snapshot.Snapshot
			if index > 0 {
				parentSnap = allSnapshots[index-1]
			} else if baseSnapshot != nil {
				parentSnap = baseSnapshot
			} else {
				zfsSendErr = fmt.Errorf("no parent snapshot available for incremental backup")
				return
			}
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
	remoteSnapshots, err := m.ListRemoteSnapshots(ctx)
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
		// For each snapshot in the chain, check if it needs to be restored
		// We need to check locally each time as the list changes after each restore
		localSnapshots, err := m.zfsManager.List(ctx)
		if err != nil {
			// If we can't list, assume we need to restore
			localSnapshots = snapshot.SnapshotList{}
		}

		// Check if snapshot already exists locally
		// Also check for the nested structure that might have been created
		exists := false
		if localSnapshots.FindByName(snapToRestore.Name) != nil {
			exists = true
		}

		// Also check if it exists in a nested structure (e.g., zroot/home/kristian/home/kristian@snapshot)
		for _, localSnap := range localSnapshots {
			if strings.HasSuffix(localSnap.Name, "@"+strings.Split(snapToRestore.Name, "@")[1]) {
				fmt.Printf("Snapshot %s already exists (found as %s), skipping\n", snapToRestore.Name, localSnap.Name)
				exists = true
				break
			}
		}

		if exists {
			continue
		}

		err = m.restoreSnapshot(ctx, snapToRestore, opts)
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

	// Determine the target dataset
	targetDataset := opts.TargetDataset
	if targetDataset == "" {
		targetDataset = m.zfsManager.GetFilesystem()
	}

	// Check if the target filesystem exists
	datasetExists := false
	checkCmd := exec.CommandContext(ctx, "zfs", "list", "-H", targetDataset)
	checkErr := checkCmd.Run()

	if checkErr == nil {
		// Dataset exists
		datasetExists = true
		fmt.Printf("Target dataset %s already exists\n", targetDataset)
	} else {
		// Dataset doesn't exist
		fmt.Printf("Target dataset %s does not exist\n", targetDataset)

		// For full backups, ZFS will create the dataset from the stream
		// For incremental backups, we need the dataset to exist
		if !snap.IsFullBackup {
			fmt.Printf("Creating target dataset for incremental restore...\n")

			// Need to create parent datasets if they don't exist
			createCmd := exec.CommandContext(ctx, "zfs", "create", "-p", targetDataset)
			var stderr bytes.Buffer
			createCmd.Stderr = &stderr

			if err := createCmd.Run(); err != nil {
				return fmt.Errorf("failed to create target dataset %s: %w (stderr: %s)", targetDataset, err, stderr.String())
			}
			fmt.Printf("Successfully created target dataset %s\n", targetDataset)
			datasetExists = true
		}
	}

	// Check if we need to handle existing filesystem
	// For incremental snapshots, we should never use -F as it would destroy the parent snapshots
	forceRestore := false

	// For existing filesystems with snapshots, we need special handling
	needsSnapshotExtraction := false
	if datasetExists && snap.IsFullBackup {
		if targetDataset == m.zfsManager.GetFilesystem() && !opts.Force {
			// When restoring a full backup to the SAME filesystem without force,
			// we need to extract the snapshot from the stream
			needsSnapshotExtraction = true
			fmt.Printf("Dataset %s exists and matches source, will extract snapshot from full backup stream\n", targetDataset)
		} else if !opts.Force {
			// For different target datasets, we need -F to overwrite
			forceRestore = true
			fmt.Printf("Target dataset exists, will use -F flag for full backup restore\n")
		}
	}

	if snap.IsFullBackup && opts.Force && !needsSnapshotExtraction {
		// Only use force for full backups when explicitly requested
		// This will rollback the filesystem to receive the full backup
		forceRestore = true
	}

	// Get object metadata to determine compression type
	objInfo, err := m.s3Client.HeadObject(ctx, s3Key)
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Check if we need GPG decryption
	hasGPG := false
	if objInfo.Metadata != nil {
		compressorType := objInfo.Metadata["compressor"]
		if compressorType == "" {
			compressorType = objInfo.Metadata["compressors"]
		}
		if strings.Contains(compressorType, "gpg") {
			hasGPG = true
		}
	}

	// If GPG is involved, use script command to provide TTY
	if hasGPG {
		fmt.Printf("Using script command for GPG TTY access\n")
		return m.restoreSnapshotWithScript(ctx, snap, opts, targetDataset, s3Key, forceRestore, needsSnapshotExtraction)
	}

	// Otherwise, use the streaming approach
	// Create appropriate decompression pipeline based on metadata
	var decompressPipeline *compress.Pipeline
	if objInfo.Metadata != nil {
		// Check both "compressor" and "compressors" for compatibility
		compressorType := objInfo.Metadata["compressor"]
		if compressorType == "" {
			compressorType = objInfo.Metadata["compressors"]
		}

		if compressorType != "" {
			// Create a pipeline specific to this snapshot's compression
			compressorTypes := compress.ParseCompressorTypes(compressorType)
			gpgRecipients := m.config.GetGPGRecipients()

			// Check if snapshot has specific GPG recipient(s) in metadata
			if recipients, ok := objInfo.Metadata["gpg_recipients"]; ok && recipients != "" {
				gpgRecipients = recipients
			} else if recipient, ok := objInfo.Metadata["gpg_recipient"]; ok && recipient != "" {
				gpgRecipients = recipient
			}

			decompressPipeline = compress.NewDefaultPipeline(compressorTypes, gpgRecipients)
		} else {
			// No compression
			decompressPipeline = compress.NewDefaultPipeline([]compress.CompressorType{}, "")
		}
	} else {
		// No metadata, try default pipeline
		decompressPipeline = m.compressPipeline
	}

	// Create decompression pipeline
	reader, writer := io.Pipe()
	decompressedReader, err := decompressPipeline.Decompress(ctx, reader)
	if err != nil {
		reader.Close()
		writer.Close()
		return fmt.Errorf("failed to create decompression pipeline: %w", err)
	}

	// Start S3 download in a goroutine
	var s3DownloadErr error
	downloadDone := make(chan struct{})
	go func() {
		defer writer.Close()
		defer close(downloadDone)

		fmt.Printf("Starting S3 download for key: %s\n", s3Key)
		s3DownloadErr = m.s3Client.GetObject(ctx, s3Key, writer)
		if s3DownloadErr != nil {
			fmt.Printf("S3 download error: %v\n", s3DownloadErr)
		} else {
			fmt.Printf("S3 download completed successfully\n")
		}
	}()

	// Debug: Log what we're about to restore
	fmt.Printf("Restoring snapshot: %s\n", snap.Name)
	fmt.Printf("Target dataset: %s\n", targetDataset)
	fmt.Printf("Is full backup: %v\n", snap.IsFullBackup)
	fmt.Printf("Using force: %v\n", forceRestore)
	fmt.Printf("Needs snapshot extraction: %v\n", needsSnapshotExtraction)

	// Add a timeout to detect hanging decompression
	recvCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if needsSnapshotExtraction {
		// Extract snapshot from full backup stream to existing filesystem
		err = m.extractAndRestoreSnapshot(recvCtx, decompressedReader, snap, targetDataset)
	} else {
		// Normal restore
		receiveOpts := snapshot.ReceiveOptions{
			Dataset: targetDataset,
			DryRun:  opts.DryRun,
			Force:   forceRestore,
		}
		err = m.zfsManager.Receive(recvCtx, decompressedReader, receiveOpts)
	}
	if err != nil {
		reader.Close()
		decompressedReader.Close()

		// Wait for download to complete to get any S3 errors
		select {
		case <-downloadDone:
			if s3DownloadErr != nil {
				return fmt.Errorf("S3 download failed: %w, ZFS receive also failed: %w", s3DownloadErr, err)
			}
		case <-time.After(5 * time.Second):
			// Don't wait too long
		}

		return fmt.Errorf("ZFS receive failed: %w", err)
	}

	// Wait for download to complete
	<-downloadDone

	// Check for S3 download errors
	if s3DownloadErr != nil {
		return fmt.Errorf("S3 download failed: %w", s3DownloadErr)
	}

	return nil
}

// Helper methods

func (m *Manager) generateS3Key(snapshotName string) string {
	if m.config.S3Prefix != "" {
		// Ensure we don't have double slashes
		prefix := strings.TrimSuffix(m.config.S3Prefix, "/")
		if prefix != "" {
			return fmt.Sprintf("%s/%s", prefix, snapshotName)
		}
	}
	return snapshotName
}

func (m *Manager) buildSnapshotMetadata(snap *snapshot.Snapshot, isFullBackup bool, parentName string, additionalMetadata map[string]string) map[string]string {
	metadata := make(map[string]string)

	// Core metadata
	metadata["snapshot_name"] = snap.Name
	// Use "isfull" for compatibility with Python Z3
	metadata["isfull"] = fmt.Sprintf("%t", isFullBackup)
	metadata["backup_time"] = time.Now().UTC().Format(time.RFC3339)
	metadata["filesystem"] = m.zfsManager.GetFilesystem()

	if parentName != "" {
		metadata["parent"] = parentName
	}

	// Compression metadata - write both for compatibility
	compMeta := m.compressPipeline.GetMetadata()
	if compType, ok := compMeta["compressor"]; ok {
		metadata["compressor"] = compType  // Python Z3 expects this
		metadata["compressors"] = compType // Our format
	}
	// Also include other compression metadata
	for k, v := range compMeta {
		if k != "compressor" { // Don't duplicate
			metadata[k] = v
		}
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
		"filesystem":  m.zfsManager.GetFilesystem(),
		"bucket":      m.s3Client.GetBucketName(),
		"prefix":      m.config.S3Prefix,
		"compressors": m.compressPipeline.GetMetadata(),
		"dry_run":     m.zfsManager.GetDryRun(),
	}
}

// ListLocalSnapshots returns all local snapshots for the configured filesystem
func (m *Manager) ListLocalSnapshots(ctx context.Context, ignorePrefix bool) (snapshot.SnapshotList, error) {
	// Get all snapshots from ZFS
	allSnapshots, err := m.zfsManager.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list ZFS snapshots: %w", err)
	}

	// If ignorePrefix is true, return all snapshots
	if ignorePrefix {
		return allSnapshots, nil
	}

	// Otherwise, filter by prefix
	prefix := m.config.GetSnapshotPrefix(m.zfsManager.GetFilesystem())
	var filtered snapshot.SnapshotList
	for _, snap := range allSnapshots {
		// Check if snapshot name contains the prefix
		// The snapshot name format is typically "daily-2024-01-01", "weekly-2024-01-01", etc.
		if strings.Contains(snap.Name, prefix) || prefix == "" {
			filtered = append(filtered, snap)
		}
	}

	return filtered, nil
}

// ListRemoteSnapshots returns all snapshots stored in S3
func (m *Manager) ListRemoteSnapshots(ctx context.Context) (snapshot.SnapshotList, error) {
	// List all objects in S3 with our prefix
	prefix := m.config.S3Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}
	prefix = prefix + m.zfsManager.GetFilesystem() + "@"

	objects, err := m.s3Client.ListObjects(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to list S3 objects: %w", err)
	}

	var snapshots snapshot.SnapshotList
	for _, obj := range objects {
		// Extract snapshot name from S3 key
		// Format: prefix/filesystem@snapshot-name
		if idx := strings.LastIndex(obj.Key, "@"); idx >= 0 {
			fullName := obj.Key
			// Remove the prefix if present
			if m.config.S3Prefix != "" {
				fullName = strings.TrimPrefix(fullName, m.config.S3Prefix)
				fullName = strings.TrimPrefix(fullName, "/")
			}

			// Get object metadata to determine if it's a full backup and parent info
			objInfo, err := m.s3Client.HeadObject(ctx, obj.Key)
			if err != nil {
				// If we can't get metadata, create snapshot with basic info
				snap := &snapshot.Snapshot{
					Name:           fullName,
					Size:           obj.Size,
					CompressedSize: obj.Size,
					CreatedAt:      obj.LastModified,
				}
				snapshots = append(snapshots, snap)
				continue
			}

			// Create snapshot with metadata
			snap := &snapshot.Snapshot{
				Name:           fullName,
				Size:           obj.Size,
				CompressedSize: obj.Size,
				CreatedAt:      obj.LastModified,
			}

			// Parse metadata
			if objInfo.Metadata != nil {
				// Check both "isfull" and "is_full" for backwards compatibility
				if isFullStr, ok := objInfo.Metadata["isfull"]; ok {
					snap.IsFullBackup = isFullStr == "true"
				} else if isFullStr, ok := objInfo.Metadata["is_full"]; ok {
					snap.IsFullBackup = isFullStr == "true"
				}
				if parent, ok := objInfo.Metadata["parent"]; ok && parent != "" {
					snap.ParentName = parent
				}
			}

			snapshots = append(snapshots, snap)
		}
	}

	return snapshots, nil
}

// extractAndRestoreSnapshot extracts a snapshot from a full backup stream and restores it to an existing filesystem
func (m *Manager) extractAndRestoreSnapshot(ctx context.Context, reader io.Reader, snap *snapshot.Snapshot, targetDataset string) error {
	// For now, we don't support extracting snapshots from full backups to existing datasets
	// This is a complex operation that requires careful handling of the dataset structure
	// and potential conflicts with existing data
	return fmt.Errorf("restoring full backups to existing datasets is not currently supported. Please use --target-dataset to restore to a new dataset")
}

// cleanupTempDataset removes a temporary dataset and all its snapshots
func (m *Manager) cleanupTempDataset(ctx context.Context, dataset string) error {
	// Use -r to recursively destroy the dataset and all snapshots
	cmd := exec.CommandContext(ctx, "zfs", "destroy", "-r", dataset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to destroy temporary dataset: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// restoreSnapshotWithScript handles restore when GPG is involved by using script command for TTY
func (m *Manager) restoreSnapshotWithScript(ctx context.Context, snap *snapshot.Snapshot, opts *RestoreOptions, targetDataset, s3Key string, forceRestore, needsSnapshotExtraction bool) error {
	// For GPG, we'll actually go back to the temp file approach since script is causing issues
	// But we'll run the GPG command with explicit environment to ensure it uses the agent

	// Create a temporary file for the encrypted data
	tempFile, err := os.CreateTemp("", "z3-restore-*.gpg")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Download the encrypted file from S3
	fmt.Printf("Downloading encrypted snapshot to temporary file...\n")
	err = m.s3Client.GetObject(ctx, s3Key, tempFile)
	if err != nil {
		return fmt.Errorf("failed to download from S3: %w", err)
	}
	tempFile.Close()

	// Get object metadata to determine compression pipeline
	objInfo, err := m.s3Client.HeadObject(ctx, s3Key)
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Build decompression command
	compressorType := ""
	if objInfo.Metadata != nil {
		compressorType = objInfo.Metadata["compressor"]
		if compressorType == "" {
			compressorType = objInfo.Metadata["compressors"]
		}
	}

	// Parse the compressor chain
	compressors := strings.Split(compressorType, ",")

	// Debug: print the compression chain
	fmt.Printf("Compression chain: %v\n", compressors)

	// Build the full command pipeline
	var pipelineCmd string
	var decryptedFile *os.File
	zfsRecvFlags := ""
	if forceRestore {
		zfsRecvFlags = "-F "
	}

	// Handle different compression scenarios
	if len(compressors) == 1 && compressors[0] == "gpg" {
		// Only GPG: Let's test different approaches

		// First, let's verify the file exists and has content
		fileInfo, err := os.Stat(tempFile.Name())
		if err != nil {
			return fmt.Errorf("temp file error: %w", err)
		}
		fmt.Printf("Temp file size: %d bytes\n", fileInfo.Size())

		// Try approach 1: Direct GPG without script (with --quiet to suppress status messages)
		fmt.Printf("\nTrying direct GPG approach...\n")
		testCmd := exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("gpg --quiet --use-agent -d '%s' 2>/dev/null | head -c 100 | xxd", tempFile.Name()))
		testCmd.Stdin = os.Stdin
		var testOut bytes.Buffer
		testCmd.Stdout = &testOut
		testCmd.Stderr = os.Stderr

		if err := testCmd.Run(); err != nil {
			fmt.Printf("Direct GPG test failed: %v\n", err)
		} else {
			fmt.Printf("First 100 bytes (hex): %s\n", testOut.String())
		}

		// Script corrupts binary data, so let's decrypt to a temp file first, then pipe to zfs
		// Create a second temp file for decrypted data
		decryptedFile, err = os.CreateTemp("", "z3-decrypted-*.zfs")
		if err != nil {
			return fmt.Errorf("failed to create decrypted temp file: %w", err)
		}
		defer os.Remove(decryptedFile.Name())
		decryptedFile.Close()

		// Decrypt with script to allow TTY access
		decryptCmd := fmt.Sprintf("script -q /dev/null gpg --quiet --use-agent -d -o '%s' '%s'",
			decryptedFile.Name(), tempFile.Name())

		fmt.Printf("Decrypting with GPG...\n")
		gpgCmd := exec.CommandContext(ctx, "sh", "-c", decryptCmd)
		gpgCmd.Stdin = os.Stdin
		gpgCmd.Stdout = os.Stdout
		gpgCmd.Stderr = os.Stderr
		gpgCmd.Env = os.Environ()

		if err := gpgCmd.Run(); err != nil {
			return fmt.Errorf("GPG decryption failed: %w", err)
		}

		// For snapshot extraction, we'll handle this differently below
		if !needsSnapshotExtraction {
			// Now pipe the decrypted file to zfs recv
			pipelineCmd = fmt.Sprintf("cat '%s' | zfs recv %s'%s'",
				decryptedFile.Name(), zfsRecvFlags, targetDataset)
		}
	} else if len(compressors) == 0 || (len(compressors) == 1 && compressors[0] == "") {
		// No compression
		pipelineCmd = fmt.Sprintf("cat '%s' | zfs recv %s'%s'",
			tempFile.Name(), zfsRecvFlags, targetDataset)
	} else {
		// For any other combination, build the pipeline dynamically
		// Start with cat to read the file
		pipeline := fmt.Sprintf("cat '%s'", tempFile.Name())

		// Apply decompression in reverse order
		for i := len(compressors) - 1; i >= 0; i-- {
			comp := strings.TrimSpace(compressors[i])
			if comp == "" {
				continue
			}

			switch comp {
			case "gpg":
				// Use script for GPG to get TTY
				pipeline = fmt.Sprintf("script -q /dev/null sh -c '%s' | gpg --quiet --use-agent -d", pipeline)
			case "pigz1", "pigz4":
				pipeline = fmt.Sprintf("%s | pigz -d", pipeline)
			default:
				return fmt.Errorf("unknown compressor: %s", comp)
			}
		}

		// Finally pipe to zfs recv
		pipelineCmd = fmt.Sprintf("%s | zfs recv %s'%s'", pipeline, zfsRecvFlags, targetDataset)
	}

	fmt.Printf("Running restore pipeline...\n")
	fmt.Printf("Command: sh -c \"%s\"\n", pipelineCmd)

	// Only run the regular restore if we're not doing snapshot extraction
	if !needsSnapshotExtraction && pipelineCmd != "" {
		// Run the command through sh to handle the pipeline
		cmd := exec.CommandContext(ctx, "sh", "-c", pipelineCmd)

		// Set up environment
		cmd.Env = os.Environ()

		// Connect stdin/stdout/stderr to allow GPG interaction
		cmd.Stdin = os.Stdin

		// Capture output for debugging
		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

		// Run the command
		if err := cmd.Run(); err != nil {
			fmt.Printf("stdout: %s\n", stdoutBuf.String())
			fmt.Printf("stderr: %s\n", stderrBuf.String())
			return fmt.Errorf("restore pipeline failed: %w", err)
		}
	}

	// If we need snapshot extraction, handle it here
	if needsSnapshotExtraction {
		// For snapshot extraction, we need to restore to a temp dataset first
		// Extract the snapshot name from the full name
		snapParts := strings.Split(snap.Name, "@")
		if len(snapParts) != 2 {
			return fmt.Errorf("invalid snapshot name format: %s", snap.Name)
		}
		snapshotName := snapParts[1]

		// Create a unique temporary dataset name
		tempDataset := fmt.Sprintf("%s_z3_restore_temp_%d", targetDataset, time.Now().Unix())

		fmt.Printf("Creating temporary dataset for extraction: %s\n", tempDataset)

		// Determine the source file for restoration
		sourceFile := tempFile.Name()
		if decryptedFile != nil {
			sourceFile = decryptedFile.Name()
		}

		// Now pipe the decrypted file to zfs recv on the temp dataset
		tempPipelineCmd := fmt.Sprintf("cat '%s' | zfs recv '%s'",
			sourceFile, tempDataset)

		fmt.Printf("Restoring to temporary dataset...\n")
		tempCmd := exec.CommandContext(ctx, "sh", "-c", tempPipelineCmd)
		tempCmd.Env = os.Environ()

		var tempStdoutBuf, tempStderrBuf bytes.Buffer
		tempCmd.Stdout = &tempStdoutBuf
		tempCmd.Stderr = &tempStderrBuf

		if err := tempCmd.Run(); err != nil {
			fmt.Printf("temp stdout: %s\n", tempStdoutBuf.String())
			fmt.Printf("temp stderr: %s\n", tempStderrBuf.String())
			return fmt.Errorf("restore to temp dataset failed: %w", err)
		}

		// Find the actual snapshot in the temp dataset
		listCmd := exec.CommandContext(ctx, "zfs", "list", "-t", "snapshot", "-H", "-o", "name", "-r", tempDataset)
		listOutput, err := listCmd.Output()
		if err != nil {
			m.cleanupTempDataset(ctx, tempDataset)
			return fmt.Errorf("failed to list snapshots in temporary dataset: %w", err)
		}

		var tempSnapshotName string
		snapshots := strings.Split(strings.TrimSpace(string(listOutput)), "\n")
		for _, snapName := range snapshots {
			if strings.HasSuffix(snapName, "@"+snapshotName) {
				tempSnapshotName = snapName
				break
			}
		}

		if tempSnapshotName == "" {
			m.cleanupTempDataset(ctx, tempDataset)
			return fmt.Errorf("could not find restored snapshot @%s in temporary dataset", snapshotName)
		}

		fmt.Printf("Found temporary snapshot: %s\n", tempSnapshotName)

		// Check if the target snapshot already exists
		targetSnapshotName := fmt.Sprintf("%s@%s", targetDataset, snapshotName)
		checkCmd := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", targetSnapshotName)
		if err := checkCmd.Run(); err == nil {
			// Snapshot already exists
			fmt.Printf("Snapshot %s already exists, skipping\n", targetSnapshotName)
			// Clean up temporary dataset
			if err := m.cleanupTempDataset(ctx, tempDataset); err != nil {
				fmt.Printf("Warning: failed to clean up temporary dataset %s: %v\n", tempDataset, err)
			}
			return nil
		}

		// Clean up temporary dataset first since we're not using it for extraction
		if err := m.cleanupTempDataset(ctx, tempDataset); err != nil {
			fmt.Printf("Warning: failed to clean up temporary dataset %s: %v\n", tempDataset, err)
		}

		// For now, we don't support extracting snapshots from full backups to existing datasets
		// This is a complex operation that requires careful handling of the dataset structure
		return fmt.Errorf("restoring full backups to existing datasets is not currently supported. Please use --target-dataset to restore to a new dataset")
	} else {
		fmt.Printf("Successfully restored snapshot %s\n", snap.Name)
	}

	return nil
}

// restoreSnapshotWithTempFile handles restore when GPG is involved by using temporary files
func (m *Manager) restoreSnapshotWithTempFile(ctx context.Context, snap *snapshot.Snapshot, opts *RestoreOptions, targetDataset, s3Key string, forceRestore, needsSnapshotExtraction bool) error {
	// Create a temporary file for the encrypted data
	tempFile, err := os.CreateTemp("", "z3-restore-*.gpg")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Download the encrypted file from S3
	fmt.Printf("Downloading encrypted snapshot to temporary file: %s\n", tempFile.Name())
	err = m.s3Client.GetObject(ctx, s3Key, tempFile)
	if err != nil {
		return fmt.Errorf("failed to download from S3: %w", err)
	}
	tempFile.Close()

	// Now decrypt using GPG in a separate command
	fmt.Printf("Decrypting snapshot with GPG...\n")

	// Get object metadata to build proper decompression pipeline
	objInfo, err := m.s3Client.HeadObject(ctx, s3Key)
	if err != nil {
		return fmt.Errorf("failed to get object metadata: %w", err)
	}

	// Build decompression command
	compressorType := ""
	if objInfo.Metadata != nil {
		compressorType = objInfo.Metadata["compressor"]
		if compressorType == "" {
			compressorType = objInfo.Metadata["compressors"]
		}
	}

	// Parse the compressor chain
	compressors := strings.Split(compressorType, ",")

	// First, let's try to decrypt to another temp file to see if GPG works at all
	decryptedFile, err := os.CreateTemp("", "z3-decrypted-*.zfs")
	if err != nil {
		return fmt.Errorf("failed to create decrypted temp file: %w", err)
	}
	defer os.Remove(decryptedFile.Name())
	defer decryptedFile.Close()

	// Try running GPG with explicit TTY allocation
	fmt.Printf("Running GPG decryption (file: %s)...\n", tempFile.Name())

	// First attempt: try with current TTY
	gpgCmd := exec.CommandContext(ctx, "gpg", "--use-agent", "-d", "-o", decryptedFile.Name(), tempFile.Name())

	// Try to connect to the current TTY
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		defer tty.Close()
		gpgCmd.Stdin = tty
		gpgCmd.Stdout = tty
		gpgCmd.Stderr = tty
		fmt.Printf("Connected GPG to /dev/tty\n")
	} else {
		fmt.Printf("Could not open /dev/tty: %v\n", err)
		// Fall back to standard streams
		gpgCmd.Stdin = os.Stdin
		gpgCmd.Stdout = os.Stdout
		gpgCmd.Stderr = os.Stderr
	}

	// Set environment
	gpgCmd.Env = os.Environ()

	// Run GPG
	if err := gpgCmd.Run(); err != nil {
		// If that fails, try running through a PTY
		fmt.Printf("Direct GPG failed, trying with script command for PTY...\n")

		// Use script command to allocate a PTY
		scriptCmd := exec.CommandContext(ctx, "script", "-q", "/dev/null", "gpg", "--use-agent", "-d", "-o", decryptedFile.Name(), tempFile.Name())
		scriptCmd.Stdin = os.Stdin
		scriptCmd.Stdout = os.Stdout
		scriptCmd.Stderr = os.Stderr
		scriptCmd.Env = os.Environ()

		if err := scriptCmd.Run(); err != nil {
			return fmt.Errorf("GPG decryption failed: %w", err)
		}
	}

	// Now we have the decrypted file, process it through remaining pipeline
	fmt.Printf("GPG decryption successful, processing through ZFS...\n")

	// Check if we need additional decompression
	needsPigz := false
	for _, comp := range compressors {
		if strings.HasPrefix(comp, "pigz") {
			needsPigz = true
			break
		}
	}

	// Build the final command
	var finalCmd *exec.Cmd
	if needsPigz {
		// pigz -d decryptedFile | zfs recv
		finalCmd = exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("pigz -d < '%s' | zfs recv %s '%s'",
			decryptedFile.Name(),
			func() string {
				if forceRestore {
					return "-F"
				}
				return ""
			}(),
			targetDataset))
	} else {
		// cat decryptedFile | zfs recv
		finalCmd = exec.CommandContext(ctx, "sh", "-c", fmt.Sprintf("cat '%s' | zfs recv %s '%s'",
			decryptedFile.Name(),
			func() string {
				if forceRestore {
					return "-F"
				}
				return ""
			}(),
			targetDataset))
	}

	var stderr bytes.Buffer
	finalCmd.Stderr = &stderr

	if err := finalCmd.Run(); err != nil {
		return fmt.Errorf("ZFS receive failed: %w (stderr: %s)", err, stderr.String())
	}

	// If we need snapshot extraction, handle it here
	if needsSnapshotExtraction {
		// This is more complex and would need similar handling as in extractAndRestoreSnapshot
		return fmt.Errorf("snapshot extraction not yet implemented for temp file approach")
	}

	fmt.Printf("Successfully restored snapshot %s\n", snap.Name)
	return nil
}
