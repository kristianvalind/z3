package zfs

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kristianvalind/z3/internal/config"
	"github.com/kristianvalind/z3/pkg/snapshot"
)

// Manager implements the ZFSManager interface for ZFS operations
type Manager struct {
	// Config holds the Z3 configuration
	config *config.Config

	// Executor handles ZFS command execution
	executor *CommandExecutor

	// Parser handles parsing of ZFS command output
	parser *SnapshotParser

	// FilesystemName is the ZFS filesystem to operate on
	filesystemName string

	// SnapshotPrefix filters snapshots by prefix
	snapshotPrefix string

	// DryRun indicates whether to perform actual operations
	dryRun bool
}

// NewManager creates a new ZFS manager
func NewManager(cfg *config.Config) *Manager {
	filesystem := cfg.Filesystem
	prefix := cfg.GetSnapshotPrefix(filesystem)

	executor := NewCommandExecutor()
	executor.DryRun = false // Will be controlled per-operation

	parser := NewSnapshotParser(filesystem, prefix)

	return &Manager{
		config:         cfg,
		executor:       executor,
		parser:         parser,
		filesystemName: filesystem,
		snapshotPrefix: prefix,
		dryRun:         false,
	}
}

// List returns all snapshots for the configured filesystem
func (m *Manager) List(ctx context.Context) (snapshot.SnapshotList, error) {
	// Execute zfs list command
	listOpts := ListOptions{
		Type:       "snapshot",
		Properties: []string{"name", "used", "refer", "mountpoint", "written"},
		Recursive:  true,
		Parseable:  true,
		Dataset:    m.filesystemName,
		Timeout:    30 * time.Second,
	}

	result, err := m.executor.List(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	// Parse the output
	snapshots, err := m.parser.ParseSnapshotList(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to parse snapshot list: %w", err)
	}

	// Convert to SnapshotList and sort
	snapshotList := snapshot.SnapshotList(snapshots)
	sort.Sort(snapshotList)

	return snapshotList, nil
}

// Get retrieves a specific snapshot by name
func (m *Manager) Get(ctx context.Context, name string) (*snapshot.Snapshot, error) {
	// Validate the snapshot name
	if err := ValidateSnapshotName(name); err != nil {
		return nil, snapshot.NewSnapshotErrorWithSnapshot(
			snapshot.ErrorTypeInvalid, "invalid snapshot name", name)
	}

	// Get all snapshots and find the one we want
	snapshots, err := m.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	// Find the snapshot
	for _, snap := range snapshots {
		if snap.Name == name {
			return snap, nil
		}
	}

	return nil, snapshot.NewSnapshotErrorWithSnapshot(
		snapshot.ErrorTypeNotFound, "snapshot not found", name)
}

// GetLatest returns the most recent snapshot
func (m *Manager) GetLatest(ctx context.Context) (*snapshot.Snapshot, error) {
	snapshots, err := m.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	if len(snapshots) == 0 {
		return nil, snapshot.NewSnapshotError(
			snapshot.ErrorTypeNotFound,
			fmt.Sprintf("no snapshots found for filesystem %s with prefix %s",
				m.filesystemName, m.snapshotPrefix))
	}

	// Get the last snapshot (they're sorted by name)
	return snapshots[len(snapshots)-1], nil
}

// Exists checks if a snapshot exists
func (m *Manager) Exists(ctx context.Context, name string) (bool, error) {
	_, err := m.Get(ctx, name)
	if err != nil {
		if snapshot.IsNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetPrefix returns the snapshot prefix for this manager
func (m *Manager) GetPrefix() string {
	return m.snapshotPrefix
}

// GetFilesystem returns the filesystem name for this manager
func (m *Manager) GetFilesystem() string {
	return m.filesystemName
}

// Send streams a ZFS snapshot using 'zfs send'
func (m *Manager) Send(ctx context.Context, snap *snapshot.Snapshot, writer io.Writer) error {
	if snap == nil {
		return snapshot.NewSnapshotError(snapshot.ErrorTypeInvalid, "snapshot cannot be nil")
	}

	sendOpts := SendOptions{
		Snapshot:          snap.Name,
		Output:            writer,
		Verbose:           true,
		ParseableOutput:   false,
		IncludeProperties: true,
		Timeout:           30 * time.Minute, // Large snapshots can take time
	}

	result, err := m.executor.Send(ctx, sendOpts)
	if err != nil {
		return fmt.Errorf("zfs send failed for snapshot %s: %w", snap.Name, err)
	}

	// Log the operation if verbose
	if result.Stderr != "" {
		// ZFS send writes progress to stderr, which is normal
		// We could log this for debugging
	}

	return nil
}

// SendIncremental streams an incremental ZFS snapshot
func (m *Manager) SendIncremental(ctx context.Context, fromSnapshot, toSnapshot *snapshot.Snapshot, writer io.Writer) error {
	if fromSnapshot == nil || toSnapshot == nil {
		return snapshot.NewSnapshotError(snapshot.ErrorTypeInvalid, "snapshots cannot be nil")
	}

	sendOpts := SendOptions{
		Snapshot:          toSnapshot.Name,
		FromSnapshot:      fromSnapshot.Name,
		Incremental:       true,
		Output:            writer,
		Verbose:           true,
		ParseableOutput:   false,
		IncludeProperties: true,
		Timeout:           30 * time.Minute,
	}

	result, err := m.executor.Send(ctx, sendOpts)
	if err != nil {
		return fmt.Errorf("zfs incremental send failed from %s to %s: %w",
			fromSnapshot.Name, toSnapshot.Name, err)
	}

	// Log the operation if verbose
	if result.Stderr != "" {
		// ZFS send writes progress to stderr, which is normal
	}

	return nil
}

// Receive receives a ZFS stream using 'zfs recv'
func (m *Manager) Receive(ctx context.Context, reader io.Reader, options snapshot.ReceiveOptions) error {
	recvOpts := ReceiveOptions{
		Dataset:          options.Dataset,
		Input:            reader,
		DryRun:           options.DryRun,
		Verbose:          true,
		Force:            options.Force,
		DiscardFirstName: false, // Don't use -d when restoring to same filesystem
		Timeout:          30 * time.Minute,
	}

	// If no dataset specified, use the configured filesystem
	if recvOpts.Dataset == "" {
		recvOpts.Dataset = m.filesystemName
	}

	// If we're restoring to a different dataset, use -d to discard the first name
	if options.Dataset != "" && options.Dataset != m.filesystemName {
		recvOpts.DiscardFirstName = true
	}

	result, err := m.executor.Receive(ctx, recvOpts)
	if err != nil {
		return fmt.Errorf("zfs receive failed for dataset %s: %w", recvOpts.Dataset, err)
	}

	// Log the operation if verbose
	if result.Stderr != "" {
		// ZFS recv writes progress to stderr, which is normal
	}

	return nil
}

// GetSendSize estimates the size of a zfs send operation
func (m *Manager) GetSendSize(ctx context.Context, snap *snapshot.Snapshot) (int64, error) {
	if snap == nil {
		return 0, snapshot.NewSnapshotError(snapshot.ErrorTypeInvalid, "snapshot cannot be nil")
	}

	return m.executor.GetSendSize(ctx, snap.Name, "")
}

// GetIncrementalSendSize estimates the size of an incremental send
func (m *Manager) GetIncrementalSendSize(ctx context.Context, fromSnapshot, toSnapshot *snapshot.Snapshot) (int64, error) {
	if fromSnapshot == nil || toSnapshot == nil {
		return 0, snapshot.NewSnapshotError(snapshot.ErrorTypeInvalid, "snapshots cannot be nil")
	}

	return m.executor.GetSendSize(ctx, toSnapshot.Name, fromSnapshot.Name)
}

// SetDryRun configures whether operations should be performed or just simulated
func (m *Manager) SetDryRun(dryRun bool) {
	m.dryRun = dryRun
	m.executor.DryRun = dryRun
}

// GetDryRun returns whether dry run mode is enabled
func (m *Manager) GetDryRun() bool {
	return m.dryRun
}

// ValidateSnapshot validates that a snapshot meets requirements
func (m *Manager) ValidateSnapshot(ctx context.Context, snap *snapshot.Snapshot) error {
	if snap == nil {
		return snapshot.NewSnapshotError(snapshot.ErrorTypeInvalid, "snapshot cannot be nil")
	}

	// Validate the name format
	if err := ValidateSnapshotName(snap.Name); err != nil {
		return snapshot.NewSnapshotErrorWithCause(
			snapshot.ErrorTypeInvalid, "invalid snapshot name", err)
	}

	// Check that the snapshot exists
	exists, err := m.Exists(ctx, snap.Name)
	if err != nil {
		return fmt.Errorf("failed to check snapshot existence: %w", err)
	}

	if !exists {
		return snapshot.NewSnapshotErrorWithSnapshot(
			snapshot.ErrorTypeNotFound, "snapshot does not exist", snap.Name)
	}

	// Check filesystem matches
	filesystem := ExtractFilesystemFromSnapshot(snap.Name)
	if filesystem != m.filesystemName {
		return snapshot.NewSnapshotErrorWithSnapshot(
			snapshot.ErrorTypeInvalid,
			fmt.Sprintf("snapshot filesystem %s does not match manager filesystem %s",
				filesystem, m.filesystemName), snap.Name)
	}

	return nil
}

// GetSnapshotsToSend determines which snapshots need to be sent for backup
func (m *Manager) GetSnapshotsToSend(ctx context.Context, remoteSnapshots snapshot.SnapshotList, targetSnapshot *snapshot.Snapshot) (snapshot.SnapshotList, error) {
	// Get local snapshots
	localSnapshots, err := m.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list local snapshots: %w", err)
	}

	// If no target specified, use latest
	if targetSnapshot == nil {
		targetSnapshot = localSnapshots.GetLatest()
		if targetSnapshot == nil {
			return nil, snapshot.NewSnapshotError(
				snapshot.ErrorTypeNotFound, "no local snapshots found")
		}
	}

	// Find the latest common snapshot between local and remote
	var commonSnapshot *snapshot.Snapshot
	for i := len(localSnapshots) - 1; i >= 0; i-- {
		localSnap := localSnapshots[i]
		if remoteSnapshots.FindByName(localSnap.Name) != nil {
			commonSnapshot = localSnap
			break
		}
	}

	// Determine snapshots to send
	var toSend snapshot.SnapshotList

	if commonSnapshot == nil {
		// No common snapshot, need to send full backup
		toSend = append(toSend, targetSnapshot)
	} else {
		// Send incrementals from common snapshot to target
		commonIndex := -1
		targetIndex := -1

		for i, snap := range localSnapshots {
			if snap.Name == commonSnapshot.Name {
				commonIndex = i
			}
			if snap.Name == targetSnapshot.Name {
				targetIndex = i
			}
		}

		if commonIndex == -1 || targetIndex == -1 {
			return nil, fmt.Errorf("failed to find snapshot indices")
		}

		// Add all snapshots from common+1 to target
		for i := commonIndex + 1; i <= targetIndex; i++ {
			toSend = append(toSend, localSnapshots[i])
		}
	}

	return toSend, nil
}
