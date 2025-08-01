package zfs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kristianvalind/z3/pkg/snapshot"
)

// SnapshotParser handles parsing of ZFS command output into snapshot structures
type SnapshotParser struct {
	// FilesystemName is the filesystem to filter by
	FilesystemName string

	// SnapshotPrefix is the prefix to filter snapshots by
	SnapshotPrefix string
}

// NewSnapshotParser creates a new snapshot parser
func NewSnapshotParser(filesystem, prefix string) *SnapshotParser {
	return &SnapshotParser{
		FilesystemName: filesystem,
		SnapshotPrefix: prefix,
	}
}

// ParseSnapshotList parses the output of 'zfs list -t snapshot' into snapshots
func (sp *SnapshotParser) ParseSnapshotList(output string) ([]*snapshot.Snapshot, error) {
	lines := strings.Split(output, "\n")
	var snapshots []*snapshot.Snapshot

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip header lines
		if strings.HasPrefix(line, "NAME") {
			continue
		}

		snap, err := sp.parseSnapshotLine(line)
		if err != nil {
			// Log the error but continue processing other lines
			continue
		}

		if snap != nil {
			snapshots = append(snapshots, snap)
		}
	}

	// Sort snapshots by name to ensure consistent ordering
	return sp.sortAndLinkSnapshots(snapshots), nil
}

// parseSnapshotLine parses a single line of ZFS list output
func (sp *SnapshotParser) parseSnapshotLine(line string) (*snapshot.Snapshot, error) {
	// Expected format: NAME USED REFER MOUNTPOINT WRITTEN
	// Example: tank/data@snap1  1.23M  10.5G  -  1.23M
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil, fmt.Errorf("insufficient fields in line: %s", line)
	}

	name := fields[0]
	used := fields[1]
	refer := fields[2]
	// mountpoint := fields[3] // Usually "-" for snapshots
	written := fields[4]

	// Parse the snapshot name
	if !strings.Contains(name, "@") {
		return nil, fmt.Errorf("invalid snapshot name: %s", name)
	}

	parts := strings.Split(name, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid snapshot name format: %s", name)
	}

	filesystem := parts[0]
	snapshotName := parts[1]

	// Filter by filesystem if specified
	if sp.FilesystemName != "" && filesystem != sp.FilesystemName {
		return nil, nil // Skip this snapshot
	}

	// Filter by snapshot prefix if specified
	if sp.SnapshotPrefix != "" && !strings.HasPrefix(snapshotName, sp.SnapshotPrefix) {
		return nil, nil // Skip this snapshot
	}

	// Parse sizes
	usedBytes, err := parseZFSSize(used)
	if err != nil {
		usedBytes = 0 // Default to 0 if can't parse
	}

	referBytes, err := parseZFSSize(refer)
	if err != nil {
		referBytes = 0 // Default to 0 if can't parse
	}

	writtenBytes, err := parseZFSSize(written)
	if err != nil {
		writtenBytes = 0 // Default to 0 if can't parse
	}
	_ = writtenBytes // Use the variable to avoid "declared and not used" error

	// Create the snapshot
	snap := &snapshot.Snapshot{
		Name:           name,
		IsFullBackup:   false, // Will be determined by linking logic
		Size:           referBytes,
		CompressedSize: usedBytes,
		CreatedAt:      time.Now(), // ZFS doesn't provide creation time in basic list
		Metadata: map[string]string{
			"used":    used,
			"refer":   refer,
			"written": written,
		},
	}

	return snap, nil
}

// sortAndLinkSnapshots sorts snapshots and determines parent-child relationships
func (sp *SnapshotParser) sortAndLinkSnapshots(snapshots []*snapshot.Snapshot) []*snapshot.Snapshot {
	if len(snapshots) == 0 {
		return snapshots
	}

	// Group snapshots by filesystem
	filesystemSnapshots := make(map[string][]*snapshot.Snapshot)
	for _, snap := range snapshots {
		filesystem := snap.GetFilesystem()
		filesystemSnapshots[filesystem] = append(filesystemSnapshots[filesystem], snap)
	}

	var result []*snapshot.Snapshot

	// Process each filesystem separately
	for filesystem, fsSnapshots := range filesystemSnapshots {
		// Sort snapshots within the filesystem
		sortedSnapshots := sp.sortSnapshotsByName(fsSnapshots)

		// Link parent-child relationships
		linkedSnapshots := sp.linkSnapshotChain(filesystem, sortedSnapshots)

		result = append(result, linkedSnapshots...)
	}

	return result
}

// sortSnapshotsByName sorts snapshots by their full name
func (sp *SnapshotParser) sortSnapshotsByName(snapshots []*snapshot.Snapshot) []*snapshot.Snapshot {
	// Simple bubble sort by name (could be optimized with sort.Slice)
	n := len(snapshots)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if snapshots[j].Name > snapshots[j+1].Name {
				snapshots[j], snapshots[j+1] = snapshots[j+1], snapshots[j]
			}
		}
	}
	return snapshots
}

// linkSnapshotChain determines parent-child relationships in a snapshot chain
func (sp *SnapshotParser) linkSnapshotChain(filesystem string, snapshots []*snapshot.Snapshot) []*snapshot.Snapshot {
	if len(snapshots) == 0 {
		return snapshots
	}

	// The first snapshot in chronological order is considered a full backup
	snapshots[0].IsFullBackup = true
	snapshots[0].ParentName = ""

	// Link subsequent snapshots
	for i := 1; i < len(snapshots); i++ {
		snapshots[i].IsFullBackup = false
		snapshots[i].ParentName = snapshots[i-1].Name
	}

	return snapshots
}

// ParseSendOutput parses the output of 'zfs send -n -v -P' to extract information
func (sp *SnapshotParser) ParseSendOutput(output string) (*SendInfo, error) {
	info := &SendInfo{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Look for size information
		if strings.Contains(line, "size") || strings.Contains(line, "estimated") {
			if size := extractSizeFromLine(line); size > 0 {
				info.EstimatedSize = size
			}
		}

		// Look for incremental information
		if strings.Contains(line, "incremental") {
			info.IsIncremental = true
		}

		// Look for full stream information
		if strings.Contains(line, "full") {
			info.IsIncremental = false
		}
	}

	return info, nil
}

// SendInfo contains information about a ZFS send operation
type SendInfo struct {
	EstimatedSize int64
	IsIncremental bool
}

// parseZFSSize parses ZFS size strings (e.g., "1.23M", "10.5G", "512K")
func parseZFSSize(sizeStr string) (int64, error) {
	if sizeStr == "" || sizeStr == "-" {
		return 0, nil
	}

	sizeStr = strings.TrimSpace(sizeStr)
	
	// Try to parse as plain number first
	if val, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
		return val, nil
	}

	if len(sizeStr) < 2 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	// Get the unit (last character)
	unit := strings.ToUpper(sizeStr[len(sizeStr)-1:])
	numberPart := sizeStr[:len(sizeStr)-1]

	// Parse the numeric part
	value, err := strconv.ParseFloat(numberPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number part: %s", numberPart)
	}

	// Convert based on unit
	switch unit {
	case "K":
		return int64(value * 1024), nil
	case "M":
		return int64(value * 1024 * 1024), nil
	case "G":
		return int64(value * 1024 * 1024 * 1024), nil
	case "T":
		return int64(value * 1024 * 1024 * 1024 * 1024), nil
	case "P":
		return int64(value * 1024 * 1024 * 1024 * 1024 * 1024), nil
	default:
		return 0, fmt.Errorf("invalid unit: %s", unit)
	}
}

// extractSizeFromLine extracts a size value from a line of text
func extractSizeFromLine(line string) int64 {
	fields := strings.Fields(line)
	for _, field := range fields {
		if size, err := parseZFSSize(field); err == nil && size > 0 {
			return size
		}
	}
	return 0
}

// ParsePropertyOutput parses ZFS property output
func (sp *SnapshotParser) ParsePropertyOutput(output string) (map[string]string, error) {
	properties := make(map[string]string)
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip header lines
		if strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "PROPERTY") {
			continue
		}

		// Parse property lines: NAME PROPERTY VALUE SOURCE
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			property := fields[1]
			value := fields[2]
			properties[property] = value
		}
	}

	return properties, nil
}

// ValidateSnapshotName validates that a snapshot name follows ZFS conventions
func ValidateSnapshotName(name string) error {
	if name == "" {
		return fmt.Errorf("snapshot name cannot be empty")
	}

	if !strings.Contains(name, "@") {
		return fmt.Errorf("snapshot name must contain '@' separator")
	}

	parts := strings.Split(name, "@")
	if len(parts) != 2 {
		return fmt.Errorf("snapshot name must have exactly one '@' separator")
	}

	filesystem := parts[0]
	snapshot := parts[1]

	if filesystem == "" {
		return fmt.Errorf("filesystem name cannot be empty")
	}

	if snapshot == "" {
		return fmt.Errorf("snapshot name cannot be empty")
	}

	// Check for invalid characters
	invalidChars := []string{" ", "\t", "\n", "\r"}
	for _, char := range invalidChars {
		if strings.Contains(name, char) {
			return fmt.Errorf("snapshot name contains invalid character: %q", char)
		}
	}

	return nil
}

// ExtractFilesystemFromSnapshot extracts the filesystem name from a snapshot name
func ExtractFilesystemFromSnapshot(snapshotName string) string {
	if idx := strings.Index(snapshotName, "@"); idx != -1 {
		return snapshotName[:idx]
	}
	return ""
}

// ExtractSnapshotFromFull extracts just the snapshot part from a full snapshot name
func ExtractSnapshotFromFull(fullName string) string {
	if idx := strings.Index(fullName, "@"); idx != -1 {
		return fullName[idx+1:]
	}
	return ""
}