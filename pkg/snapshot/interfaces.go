package snapshot

import (
	"context"
	"io"
	"time"
)

// SnapshotManager defines the interface for managing snapshots.
//
// This interface is implemented by both ZFS and S3 snapshot managers,
// allowing for consistent handling of snapshot operations regardless
// of the underlying storage system.
type SnapshotManager interface {
	// List returns all snapshots managed by this manager
	List(ctx context.Context) (SnapshotList, error)

	// Get retrieves a specific snapshot by name
	Get(ctx context.Context, name string) (*Snapshot, error)

	// GetLatest returns the most recent snapshot
	GetLatest(ctx context.Context) (*Snapshot, error)

	// Exists checks if a snapshot exists
	Exists(ctx context.Context, name string) (bool, error)

	// GetPrefix returns the snapshot prefix for this manager
	GetPrefix() string

	// GetFilesystem returns the filesystem name for this manager
	GetFilesystem() string
}

// ZFSManager extends SnapshotManager with ZFS-specific operations
type ZFSManager interface {
	SnapshotManager

	// Send streams a ZFS snapshot using 'zfs send'
	Send(ctx context.Context, snapshot *Snapshot, writer io.Writer) error

	// SendIncremental streams an incremental ZFS snapshot
	SendIncremental(ctx context.Context, fromSnapshot, toSnapshot *Snapshot, writer io.Writer) error

	// Receive receives a ZFS stream using 'zfs recv'
	Receive(ctx context.Context, reader io.Reader, options ReceiveOptions) error

	// GetSendSize estimates the size of a zfs send operation
	GetSendSize(ctx context.Context, snapshot *Snapshot) (int64, error)

	// GetIncrementalSendSize estimates the size of an incremental send
	GetIncrementalSendSize(ctx context.Context, fromSnapshot, toSnapshot *Snapshot) (int64, error)
}

// S3Manager extends SnapshotManager with S3-specific operations
type S3Manager interface {
	SnapshotManager

	// Upload uploads a snapshot to S3
	Upload(ctx context.Context, key string, reader io.Reader, metadata map[string]string) error

	// Download downloads a snapshot from S3
	Download(ctx context.Context, key string, writer io.Writer) error

	// Delete removes a snapshot from S3
	Delete(ctx context.Context, key string) error

	// GetMetadata retrieves metadata for an S3 object
	GetMetadata(ctx context.Context, key string) (map[string]string, error)

	// SetMetadata updates metadata for an S3 object
	SetMetadata(ctx context.Context, key string, metadata map[string]string) error

	// GetBucket returns the S3 bucket name
	GetBucket() string

	// GetKeyPrefix returns the S3 key prefix
	GetKeyPrefix() string
}

// BackupManager orchestrates backup operations between ZFS and S3
type BackupManager interface {
	// BackupFull performs a full backup of the latest snapshot
	BackupFull(ctx context.Context, options BackupOptions) (*BackupResult, error)

	// BackupIncremental performs incremental backups of missing snapshots
	BackupIncremental(ctx context.Context, options BackupOptions) (*BackupResult, error)

	// Restore restores a snapshot from S3 to ZFS
	Restore(ctx context.Context, snapshotName string, options RestoreOptions) error

	// Status returns the current backup status
	Status(ctx context.Context) (*BackupStatus, error)

	// Validate checks the integrity of backup chains
	Validate(ctx context.Context) (*ValidationResult, error)
}

// ReceiveOptions configures ZFS receive operations
type ReceiveOptions struct {
	// Force enables forced receive (zfs recv -F)
	Force bool

	// DryRun performs a dry run without making changes
	DryRun bool

	// Dataset specifies the target dataset
	Dataset string
}

// BackupOptions configures backup operations
type BackupOptions struct {
	// SnapshotName specifies a particular snapshot to backup (optional)
	SnapshotName string

	// DryRun performs a dry run without making changes
	DryRun bool

	// Compressor specifies the compression method
	Compressor string

	// GPGRecipient specifies the GPG recipient for encryption
	GPGRecipient string

	// Concurrency specifies the number of concurrent upload workers
	Concurrency int

	// Force forces backup even if snapshot exists
	Force bool
}

// RestoreOptions configures restore operations
type RestoreOptions struct {
	// Force enables forced restore (zfs recv -F)
	Force bool

	// DryRun performs a dry run without making changes
	DryRun bool

	// Dataset specifies the target dataset (optional, uses original if empty)
	Dataset string
}

// BackupResult contains the results of a backup operation
type BackupResult struct {
	// SnapshotsBackedUp contains information about backed up snapshots
	SnapshotsBackedUp []SnapshotBackupInfo

	// TotalSize is the total uncompressed size backed up
	TotalSize int64

	// CompressedSize is the total compressed size uploaded
	CompressedSize int64

	// Duration is how long the backup took
	Duration time.Duration

	// Success indicates if the backup completed successfully
	Success bool

	// Error contains any error that occurred
	Error error
}

// SnapshotBackupInfo contains information about a single snapshot backup
type SnapshotBackupInfo struct {
	// SnapshotName is the name of the snapshot
	SnapshotName string

	// Size is the uncompressed size
	Size int64

	// CompressedSize is the compressed size uploaded
	CompressedSize int64

	// IsIncremental indicates if this was an incremental backup
	IsIncremental bool

	// ParentName is the parent snapshot name for incremental backups
	ParentName string
}

// BackupStatus represents the current status of backups
type BackupStatus struct {
	// LocalSnapshots are the snapshots available locally
	LocalSnapshots SnapshotList

	// RemoteSnapshots are the snapshots available in S3
	RemoteSnapshots SnapshotList

	// SnapshotPairs pairs local and remote snapshots
	SnapshotPairs []SnapshotPair

	// HealthySanpshots are snapshots that pass health checks
	HealthySnapshots SnapshotList

	// UnhealthySnapshots are snapshots that fail health checks
	UnhealthySnapshots SnapshotList

	// MissingFromRemote are local snapshots not backed up
	MissingFromRemote SnapshotList

	// MissingFromLocal are remote snapshots not available locally
	MissingFromLocal SnapshotList
}

// SnapshotPair pairs a local and remote snapshot
type SnapshotPair struct {
	// Local is the local ZFS snapshot (may be nil)
	Local *Snapshot

	// Remote is the remote S3 snapshot (may be nil)
	Remote *Snapshot

	// Status describes the relationship between local and remote
	Status PairStatus
}

// PairStatus describes the status of a snapshot pair
type PairStatus string

const (
	// PairStatusSynced indicates local and remote are in sync
	PairStatusSynced PairStatus = "synced"

	// PairStatusLocalOnly indicates snapshot exists only locally
	PairStatusLocalOnly PairStatus = "local_only"

	// PairStatusRemoteOnly indicates snapshot exists only remotely
	PairStatusRemoteOnly PairStatus = "remote_only"

	// PairStatusDifferent indicates local and remote differ
	PairStatusDifferent PairStatus = "different"
)

// ValidationResult contains the results of backup validation
type ValidationResult struct {
	// IsValid indicates if all backups are valid
	IsValid bool

	// Issues contains any validation issues found
	Issues []ValidationIssue

	// TotalSnapshots is the total number of snapshots checked
	TotalSnapshots int

	// ValidSnapshots is the number of valid snapshots
	ValidSnapshots int
}

// ValidationIssue represents a validation problem
type ValidationIssue struct {
	// SnapshotName is the name of the problematic snapshot
	SnapshotName string

	// IssueType describes the type of issue
	IssueType ValidationIssueType

	// Description provides details about the issue
	Description string

	// Severity indicates how serious the issue is
	Severity ValidationSeverity
}

// ValidationIssueType represents different types of validation issues
type ValidationIssueType string

const (
	// ValidationIssueTypeCycle indicates a cycle in the parent chain
	ValidationIssueTypeCycle ValidationIssueType = "cycle"

	// ValidationIssueTypeMissingParent indicates a missing parent snapshot
	ValidationIssueTypeMissingParent ValidationIssueType = "missing_parent"

	// ValidationIssueTypeCorrupted indicates corrupted data
	ValidationIssueTypeCorrupted ValidationIssueType = "corrupted"

	// ValidationIssueTypeInconsistent indicates inconsistent metadata
	ValidationIssueTypeInconsistent ValidationIssueType = "inconsistent"
)

// ValidationSeverity indicates the severity of a validation issue
type ValidationSeverity string

const (
	// ValidationSeverityError indicates a critical issue
	ValidationSeverityError ValidationSeverity = "error"

	// ValidationSeverityWarning indicates a non-critical issue
	ValidationSeverityWarning ValidationSeverity = "warning"

	// ValidationSeverityInfo indicates informational issue
	ValidationSeverityInfo ValidationSeverity = "info"
)