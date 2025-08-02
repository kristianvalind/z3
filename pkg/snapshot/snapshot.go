// Package snapshot provides core types and interfaces for ZFS snapshot management.
//
// This package defines the fundamental data structures used throughout the Z3
// backup system, including snapshots, metadata, and health checking logic.
// It maintains compatibility with the original Python implementation while
// providing Go-specific improvements.
package snapshot

import (
	"context"
	"fmt"
	"time"
)

// Snapshot represents a ZFS or S3 snapshot with its metadata.
//
// Snapshots can be either full backups or incremental backups that
// depend on a parent snapshot. The health checking system validates
// that incremental snapshots have valid parent chains.
type Snapshot struct {
	// Name is the full snapshot name including filesystem (e.g., "tank/data@snap1")
	Name string `json:"name"`

	// IsFullBackup indicates whether this is a full backup or incremental
	IsFullBackup bool `json:"is_full_backup"`

	// ParentName is the name of the parent snapshot for incremental backups
	ParentName string `json:"parent_name,omitempty"`

	// Size is the uncompressed size of the snapshot in bytes
	Size int64 `json:"size"`

	// CompressedSize is the compressed/encrypted size stored in S3
	CompressedSize int64 `json:"compressed_size,omitempty"`

	// Compressor used for this snapshot (pigz1, pigz4, gpg, etc.)
	Compressor string `json:"compressor,omitempty"`

	// CreatedAt is when the snapshot was created
	CreatedAt time.Time `json:"created_at"`

	// Metadata contains additional key-value pairs
	Metadata map[string]string `json:"metadata,omitempty"`

	// Health status and reason (computed fields)
	isHealthy    *bool  // Cached health status
	healthReason string // Reason if unhealthy
}

// HealthStatus represents the health status of a snapshot
type HealthStatus string

const (
	// HealthStatusHealthy indicates the snapshot is healthy
	HealthStatusHealthy HealthStatus = "healthy"

	// HealthStatusCycle indicates a cycle was detected in the parent chain
	HealthStatusCycle HealthStatus = "cycle"

	// HealthStatusMissingParent indicates the parent snapshot is missing
	HealthStatusMissingParent HealthStatus = "missing_parent"

	// HealthStatusParentBroken indicates the parent snapshot is broken
	HealthStatusParentBroken HealthStatus = "parent_broken"
)

// String returns a string representation of the snapshot
func (s *Snapshot) String() string {
	if s.IsFullBackup {
		return fmt.Sprintf("Snapshot{%s [full]}", s.Name)
	}
	return fmt.Sprintf("Snapshot{%s [%s]}", s.Name, s.ParentName)
}

// GetShortName returns the snapshot name without the filesystem prefix
func (s *Snapshot) GetShortName() string {
	if idx := findLastIndex(s.Name, '@'); idx != -1 {
		return s.Name[idx+1:]
	}
	return s.Name
}

// GetFilesystem returns the filesystem part of the snapshot name
func (s *Snapshot) GetFilesystem() string {
	if idx := findLastIndex(s.Name, '@'); idx != -1 {
		return s.Name[:idx]
	}
	return ""
}

// GetFullName returns the full snapshot name (same as Name)
// This is added for consistency and clarity in some contexts
func (s *Snapshot) GetFullName() string {
	return s.Name
}

// IsHealthy returns whether the snapshot is healthy.
// This method caches the result to avoid recomputation.
func (s *Snapshot) IsHealthy(manager SnapshotManager) bool {
	if s.isHealthy != nil {
		return *s.isHealthy
	}

	// Create a simple getter that wraps the manager
	getter := &snapshotGetter{manager: manager}
	healthy := s.checkHealth(getter, make(map[string]bool))
	s.isHealthy = &healthy
	return healthy
}

// GetHealthReason returns the reason why a snapshot is unhealthy, if any
func (s *Snapshot) GetHealthReason() string {
	return s.healthReason
}

// snapshotGetter provides a simple interface for health checking
type snapshotGetter struct {
	manager SnapshotManager
}

func (sg *snapshotGetter) Get(name string) (*Snapshot, error) {
	return sg.manager.Get(context.Background(), name)
}

// checkHealth recursively checks the health of a snapshot and its parent chain
func (s *Snapshot) checkHealth(getter *snapshotGetter, visited map[string]bool) bool {
	// Full backups are always healthy
	if s.IsFullBackup {
		s.healthReason = ""
		return true
	}

	// Check for cycles
	if visited[s.Name] {
		s.healthReason = string(HealthStatusCycle)
		return false
	}

	// Mark this snapshot as visited
	visited[s.Name] = true

	// Check if parent exists
	if s.ParentName == "" {
		s.healthReason = string(HealthStatusMissingParent)
		return false
	}

	parent, err := getter.Get(s.ParentName)
	if err != nil || parent == nil {
		s.healthReason = string(HealthStatusMissingParent)
		return false
	}

	// Check parent health
	if !parent.checkHealth(getter, visited) {
		if parent.healthReason == string(HealthStatusCycle) {
			s.healthReason = string(HealthStatusCycle)
		} else {
			s.healthReason = string(HealthStatusParentBroken)
		}
		return false
	}

	s.healthReason = ""
	return true
}

// ClearHealthCache clears the cached health status, forcing recomputation
func (s *Snapshot) ClearHealthCache() {
	s.isHealthy = nil
	s.healthReason = ""
}

// SnapshotList represents a list of snapshots with utility methods
type SnapshotList []*Snapshot

// Len returns the number of snapshots
func (sl SnapshotList) Len() int {
	return len(sl)
}

// Less compares two snapshots by name for sorting
func (sl SnapshotList) Less(i, j int) bool {
	return sl[i].Name < sl[j].Name
}

// Swap swaps two snapshots in the list
func (sl SnapshotList) Swap(i, j int) {
	sl[i], sl[j] = sl[j], sl[i]
}

// FindByName finds a snapshot by name
func (sl SnapshotList) FindByName(name string) *Snapshot {
	for _, snap := range sl {
		if snap.Name == name {
			return snap
		}
	}
	return nil
}

// FilterHealthy returns only healthy snapshots
func (sl SnapshotList) FilterHealthy(manager SnapshotManager) SnapshotList {
	var healthy SnapshotList
	for _, snap := range sl {
		if snap.IsHealthy(manager) {
			healthy = append(healthy, snap)
		}
	}
	return healthy
}

// FilterByPrefix returns snapshots that match the given prefix
func (sl SnapshotList) FilterByPrefix(prefix string) SnapshotList {
	var filtered SnapshotList
	for _, snap := range sl {
		if snap.MatchesPrefix(prefix) {
			filtered = append(filtered, snap)
		}
	}
	return filtered
}

// GetLatest returns the latest snapshot (assumes sorted list)
func (sl SnapshotList) GetLatest() *Snapshot {
	if len(sl) == 0 {
		return nil
	}
	return sl[len(sl)-1]
}

// MatchesPrefix checks if the snapshot name matches the given prefix
func (s *Snapshot) MatchesPrefix(prefix string) bool {
	shortName := s.GetShortName()
	return len(shortName) >= len(prefix) && shortName[:len(prefix)] == prefix
}

// findLastIndex finds the last occurrence of a character in a string
func findLastIndex(s string, char rune) int {
	for i := len(s) - 1; i >= 0; i-- {
		if rune(s[i]) == char {
			return i
		}
	}
	return -1
}
