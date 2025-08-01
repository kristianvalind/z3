package zfs

import (
	"testing"

	"github.com/kristianvalind/z3/pkg/snapshot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseZFSSize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{
			name:     "kilobytes",
			input:    "512K",
			expected: 512 * 1024,
			wantErr:  false,
		},
		{
			name:     "megabytes",
			input:    "1.5M",
			expected: 1572864, // 1.5 * 1024 * 1024
			wantErr:  false,
		},
		{
			name:     "gigabytes",
			input:    "2.5G",
			expected: 2684354560, // 2.5 * 1024 * 1024 * 1024
			wantErr:  false,
		},
		{
			name:     "terabytes",
			input:    "1T",
			expected: 1024 * 1024 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "bytes",
			input:    "1024",
			expected: 1024,
			wantErr:  false,
		},
		{
			name:     "dash (no value)",
			input:    "-",
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "invalid format",
			input:    "invalid",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseZFSSize(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestSnapshotParser_ParseSnapshotList(t *testing.T) {
	parser := NewSnapshotParser("tank/data", "zfs-auto-snap:daily")

	// Mock ZFS list output
	output := `NAME                                    USED  REFER  MOUNTPOINT  WRITTEN
tank/data@zfs-auto-snap:daily-2024-01-01  1.23M  10.5G       -      1.23M
tank/data@zfs-auto-snap:daily-2024-01-02  2.45M  10.6G       -      2.45M
tank/other@snapshot                        1.0M   5.0G        -      1.0M
tank/data@other-prefix                     500K   10.5G       -      500K`

	snapshots, err := parser.ParseSnapshotList(output)
	require.NoError(t, err)

	// Should only get the snapshots that match our filesystem and prefix
	assert.Len(t, snapshots, 2)

	// Check first snapshot
	snap1 := snapshots[0]
	assert.Equal(t, "tank/data@zfs-auto-snap:daily-2024-01-01", snap1.Name)
	assert.True(t, snap1.IsFullBackup) // First snapshot should be full backup
	assert.Equal(t, "", snap1.ParentName)
	assert.Equal(t, int64(11274289152), snap1.Size) // 10.5 * 1024^3
	assert.Equal(t, int64(1289748), snap1.CompressedSize) // 1.23 * 1024^2

	// Check second snapshot
	snap2 := snapshots[1]
	assert.Equal(t, "tank/data@zfs-auto-snap:daily-2024-01-02", snap2.Name)
	assert.False(t, snap2.IsFullBackup) // Second snapshot should be incremental
	assert.Equal(t, "tank/data@zfs-auto-snap:daily-2024-01-01", snap2.ParentName)
}

func TestSnapshotParser_ParseSnapshotLine(t *testing.T) {
	parser := NewSnapshotParser("tank/data", "daily")

	tests := []struct {
		name        string
		line        string
		expectSnap  bool
		expectName  string
		expectError bool
	}{
		{
			name:       "valid snapshot line",
			line:       "tank/data@daily-2024-01-01  1.23M  10.5G  -  1.23M",
			expectSnap: true,
			expectName: "tank/data@daily-2024-01-01",
		},
		{
			name:       "different filesystem (filtered out)",
			line:       "tank/other@daily-2024-01-01  1.23M  10.5G  -  1.23M",
			expectSnap: false,
		},
		{
			name:       "different prefix (filtered out)",
			line:       "tank/data@hourly-2024-01-01  1.23M  10.5G  -  1.23M",
			expectSnap: false,
		},
		{
			name:        "insufficient fields",
			line:        "tank/data@daily-2024-01-01  1.23M",
			expectSnap:  false,
			expectError: true,
		},
		{
			name:        "invalid snapshot name",
			line:        "tank-data-daily-2024-01-01  1.23M  10.5G  -  1.23M",
			expectSnap:  false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap, err := parser.parseSnapshotLine(tt.line)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			if tt.expectSnap {
				require.NoError(t, err)
				require.NotNil(t, snap)
				assert.Equal(t, tt.expectName, snap.Name)
			} else {
				assert.Nil(t, snap)
			}
		})
	}
}

func TestValidateSnapshotName(t *testing.T) {
	tests := []struct {
		name      string
		snapName  string
		wantError bool
	}{
		{
			name:      "valid snapshot name",
			snapName:  "tank/data@snapshot-2024-01-01",
			wantError: false,
		},
		{
			name:      "simple valid name",
			snapName:  "pool@snap",
			wantError: false,
		},
		{
			name:      "empty name",
			snapName:  "",
			wantError: true,
		},
		{
			name:      "no @ separator",
			snapName:  "tank/data/snapshot",
			wantError: true,
		},
		{
			name:      "multiple @ separators",
			snapName:  "tank@data@snapshot",
			wantError: true,
		},
		{
			name:      "empty filesystem",
			snapName:  "@snapshot",
			wantError: true,
		},
		{
			name:      "empty snapshot",
			snapName:  "tank/data@",
			wantError: true,
		},
		{
			name:      "contains space",
			snapName:  "tank/data@snap shot",
			wantError: true,
		},
		{
			name:      "contains tab",
			snapName:  "tank/data@snap\tshot",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSnapshotName(tt.snapName)
			if tt.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExtractFilesystemFromSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		expected string
	}{
		{
			name:     "complex filesystem",
			snapshot: "tank/data/backups@snapshot-2024-01-01",
			expected: "tank/data/backups",
		},
		{
			name:     "simple filesystem",
			snapshot: "pool@snap",
			expected: "pool",
		},
		{
			name:     "no @ separator",
			snapshot: "tank/data",
			expected: "",
		},
		{
			name:     "empty string",
			snapshot: "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractFilesystemFromSnapshot(tt.snapshot)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractSnapshotFromFull(t *testing.T) {
	tests := []struct {
		name     string
		fullName string
		expected string
	}{
		{
			name:     "complex snapshot",
			fullName: "tank/data/backups@snapshot-2024-01-01",
			expected: "snapshot-2024-01-01",
		},
		{
			name:     "simple snapshot",
			fullName: "pool@snap",
			expected: "snap",
		},
		{
			name:     "no @ separator",
			fullName: "tank/data",
			expected: "",
		},
		{
			name:     "empty string",
			fullName: "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractSnapshotFromFull(tt.fullName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSnapshotParser_sortSnapshotsByName(t *testing.T) {
	parser := NewSnapshotParser("tank/data", "daily")

	// Create unsorted snapshots
	snapshots := []*snapshot.Snapshot{
		{Name: "tank/data@daily-2024-01-03"},
		{Name: "tank/data@daily-2024-01-01"},
		{Name: "tank/data@daily-2024-01-02"},
	}

	sorted := parser.sortSnapshotsByName(snapshots)

	// Should be sorted by name
	assert.Equal(t, "tank/data@daily-2024-01-01", sorted[0].Name)
	assert.Equal(t, "tank/data@daily-2024-01-02", sorted[1].Name)
	assert.Equal(t, "tank/data@daily-2024-01-03", sorted[2].Name)
}

func TestSnapshotParser_linkSnapshotChain(t *testing.T) {
	parser := NewSnapshotParser("tank/data", "daily")

	// Create sorted snapshots
	snapshots := []*snapshot.Snapshot{
		{Name: "tank/data@daily-2024-01-01"},
		{Name: "tank/data@daily-2024-01-02"},
		{Name: "tank/data@daily-2024-01-03"},
	}

	linked := parser.linkSnapshotChain("tank/data", snapshots)

	// First should be full backup
	assert.True(t, linked[0].IsFullBackup)
	assert.Equal(t, "", linked[0].ParentName)

	// Others should be incremental with proper parent links
	assert.False(t, linked[1].IsFullBackup)
	assert.Equal(t, "tank/data@daily-2024-01-01", linked[1].ParentName)

	assert.False(t, linked[2].IsFullBackup)
	assert.Equal(t, "tank/data@daily-2024-01-02", linked[2].ParentName)
}

func TestSnapshotParser_ParsePropertyOutput(t *testing.T) {
	parser := NewSnapshotParser("tank/data", "daily")

	output := `NAME               PROPERTY     VALUE     SOURCE
tank/data@snap1    used         1.23M     -
tank/data@snap1    refer        10.5G     -
tank/data@snap1    written      1.23M     -`

	properties, err := parser.ParsePropertyOutput(output)
	require.NoError(t, err)

	assert.Equal(t, "1.23M", properties["used"])
	assert.Equal(t, "10.5G", properties["refer"])
	assert.Equal(t, "1.23M", properties["written"])
}