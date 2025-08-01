package snapshot

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshot_GetShortName(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		expected string
	}{
		{
			name:     "full snapshot name",
			snapshot: "tank/data@snapshot-2024-01-01",
			expected: "snapshot-2024-01-01",
		},
		{
			name:     "simple snapshot name",
			snapshot: "tank@snap1",
			expected: "snap1",
		},
		{
			name:     "no @ symbol",
			snapshot: "tank",
			expected: "tank",
		},
		{
			name:     "empty string",
			snapshot: "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &Snapshot{Name: tt.snapshot}
			result := snap.GetShortName()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSnapshot_GetFilesystem(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		expected string
	}{
		{
			name:     "full snapshot name",
			snapshot: "tank/data@snapshot-2024-01-01",
			expected: "tank/data",
		},
		{
			name:     "simple snapshot name",
			snapshot: "tank@snap1",
			expected: "tank",
		},
		{
			name:     "no @ symbol",
			snapshot: "tank",
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
			snap := &Snapshot{Name: tt.snapshot}
			result := snap.GetFilesystem()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSnapshot_MatchesPrefix(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		prefix   string
		expected bool
	}{
		{
			name:     "matches prefix",
			snapshot: "tank/data@zfs-auto-snap:daily-2024-01-01",
			prefix:   "zfs-auto-snap:daily",
			expected: true,
		},
		{
			name:     "doesn't match prefix",
			snapshot: "tank/data@zfs-auto-snap:hourly-2024-01-01",
			prefix:   "zfs-auto-snap:daily",
			expected: false,
		},
		{
			name:     "empty prefix matches all",
			snapshot: "tank/data@anything",
			prefix:   "",
			expected: true,
		},
		{
			name:     "prefix longer than snapshot",
			snapshot: "tank/data@short",
			prefix:   "very-long-prefix",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &Snapshot{Name: tt.snapshot}
			result := snap.MatchesPrefix(tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSnapshot_String(t *testing.T) {
	tests := []struct {
		name     string
		snapshot *Snapshot
		expected string
	}{
		{
			name: "full backup",
			snapshot: &Snapshot{
				Name:         "tank/data@snap1",
				IsFullBackup: true,
			},
			expected: "Snapshot{tank/data@snap1 [full]}",
		},
		{
			name: "incremental backup",
			snapshot: &Snapshot{
				Name:         "tank/data@snap2",
				IsFullBackup: false,
				ParentName:   "tank/data@snap1",
			},
			expected: "Snapshot{tank/data@snap2 [tank/data@snap1]}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.snapshot.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// MockSnapshotManager for testing
type MockSnapshotManager struct {
	snapshots map[string]*Snapshot
}

func NewMockSnapshotManager(snapshots []*Snapshot) *MockSnapshotManager {
	manager := &MockSnapshotManager{
		snapshots: make(map[string]*Snapshot),
	}
	for _, snap := range snapshots {
		manager.snapshots[snap.Name] = snap
	}
	return manager
}

func (m *MockSnapshotManager) List(ctx context.Context) (SnapshotList, error) {
	var list SnapshotList
	for _, snap := range m.snapshots {
		list = append(list, snap)
	}
	sort.Sort(list)
	return list, nil
}

func (m *MockSnapshotManager) Get(ctx context.Context, name string) (*Snapshot, error) {
	if snap, exists := m.snapshots[name]; exists {
		return snap, nil
	}
	return nil, NewSnapshotErrorWithSnapshot(ErrorTypeNotFound, "snapshot not found", name)
}

func (m *MockSnapshotManager) GetLatest(ctx context.Context) (*Snapshot, error) {
	list, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	return list.GetLatest(), nil
}

func (m *MockSnapshotManager) Exists(ctx context.Context, name string) (bool, error) {
	_, exists := m.snapshots[name]
	return exists, nil
}

func (m *MockSnapshotManager) GetPrefix() string {
	return "test-prefix"
}

func (m *MockSnapshotManager) GetFilesystem() string {
	return "tank/test"
}

func TestSnapshot_IsHealthy(t *testing.T) {
	// Create test snapshots
	snap1 := &Snapshot{
		Name:         "tank/data@snap1",
		IsFullBackup: true,
	}

	snap2 := &Snapshot{
		Name:         "tank/data@snap2",
		IsFullBackup: false,
		ParentName:   "tank/data@snap1",
	}

	snap3 := &Snapshot{
		Name:         "tank/data@snap3",
		IsFullBackup: false,
		ParentName:   "tank/data@snap2",
	}

	// Snapshot with missing parent
	snapOrphan := &Snapshot{
		Name:         "tank/data@orphan",
		IsFullBackup: false,
		ParentName:   "tank/data@missing",
	}

	// Snapshot that creates a cycle
	snapCycle1 := &Snapshot{
		Name:         "tank/data@cycle1",
		IsFullBackup: false,
		ParentName:   "tank/data@cycle2",
	}

	snapCycle2 := &Snapshot{
		Name:         "tank/data@cycle2",
		IsFullBackup: false,
		ParentName:   "tank/data@cycle1",
	}

	tests := []struct {
		name      string
		snapshots []*Snapshot
		testSnap  string
		expected  bool
		reason    string
	}{
		{
			name:      "full backup is healthy",
			snapshots: []*Snapshot{snap1},
			testSnap:  "tank/data@snap1",
			expected:  true,
			reason:    "",
		},
		{
			name:      "incremental with valid parent is healthy",
			snapshots: []*Snapshot{snap1, snap2},
			testSnap:  "tank/data@snap2",
			expected:  true,
			reason:    "",
		},
		{
			name:      "chain of incrementals is healthy",
			snapshots: []*Snapshot{snap1, snap2, snap3},
			testSnap:  "tank/data@snap3",
			expected:  true,
			reason:    "",
		},
		{
			name:      "snapshot with missing parent is unhealthy",
			snapshots: []*Snapshot{snapOrphan},
			testSnap:  "tank/data@orphan",
			expected:  false,
			reason:    string(HealthStatusMissingParent),
		},
		{
			name:      "cycle detection",
			snapshots: []*Snapshot{snapCycle1, snapCycle2},
			testSnap:  "tank/data@cycle1",
			expected:  false,
			reason:    string(HealthStatusCycle),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewMockSnapshotManager(tt.snapshots)

			snap, err := manager.Get(context.Background(), tt.testSnap)
			require.NoError(t, err)
			require.NotNil(t, snap)

			result := snap.IsHealthy(manager)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.reason, snap.GetHealthReason())
		})
	}
}

func TestSnapshotList_Operations(t *testing.T) {
	snap1 := &Snapshot{Name: "tank/data@snap1", IsFullBackup: true}
	snap2 := &Snapshot{Name: "tank/data@snap2", IsFullBackup: false, ParentName: "tank/data@snap1"}
	snap3 := &Snapshot{Name: "tank/data@snap3", IsFullBackup: false, ParentName: "tank/data@snap2"}

	list := SnapshotList{snap3, snap1, snap2} // Intentionally unsorted

	t.Run("sorting", func(t *testing.T) {
		sort.Sort(list)
		assert.Equal(t, "tank/data@snap1", list[0].Name)
		assert.Equal(t, "tank/data@snap2", list[1].Name)
		assert.Equal(t, "tank/data@snap3", list[2].Name)
	})

	t.Run("find by name", func(t *testing.T) {
		found := list.FindByName("tank/data@snap2")
		require.NotNil(t, found)
		assert.Equal(t, "tank/data@snap2", found.Name)

		notFound := list.FindByName("tank/data@nonexistent")
		assert.Nil(t, notFound)
	})

	t.Run("get latest", func(t *testing.T) {
		latest := list.GetLatest()
		require.NotNil(t, latest)
		assert.Equal(t, "tank/data@snap3", latest.Name)

		emptyList := SnapshotList{}
		assert.Nil(t, emptyList.GetLatest())
	})

	t.Run("filter by prefix", func(t *testing.T) {
		// Add snapshots with different prefixes
		snapDaily := &Snapshot{Name: "tank/data@zfs-auto-snap:daily-2024-01-01"}
		snapHourly := &Snapshot{Name: "tank/data@zfs-auto-snap:hourly-2024-01-01"}

		mixedList := SnapshotList{snap1, snapDaily, snapHourly}

		dailyList := mixedList.FilterByPrefix("zfs-auto-snap:daily")
		assert.Len(t, dailyList, 1)
		assert.Equal(t, "tank/data@zfs-auto-snap:daily-2024-01-01", dailyList[0].Name)
	})
}

func TestSnapshot_HealthCaching(t *testing.T) {
	snap := &Snapshot{
		Name:         "tank/data@snap1",
		IsFullBackup: true,
	}

	manager := NewMockSnapshotManager([]*Snapshot{snap})

	// First call should compute health
	result1 := snap.IsHealthy(manager)
	assert.True(t, result1)
	assert.NotNil(t, snap.isHealthy)

	// Second call should use cached result
	result2 := snap.IsHealthy(manager)
	assert.True(t, result2)
	assert.Equal(t, result1, result2)

	// Clear cache and verify it's recomputed
	snap.ClearHealthCache()
	assert.Nil(t, snap.isHealthy)

	result3 := snap.IsHealthy(manager)
	assert.True(t, result3)
	assert.NotNil(t, snap.isHealthy)
}

func TestFindLastIndex(t *testing.T) {
	tests := []struct {
		name     string
		str      string
		char     rune
		expected int
	}{
		{
			name:     "found at end",
			str:      "tank/data@snap",
			char:     '@',
			expected: 9,
		},
		{
			name:     "found in middle",
			str:      "tank/data@snap@test",
			char:     '@',
			expected: 14,
		},
		{
			name:     "not found",
			str:      "tank/data",
			char:     '@',
			expected: -1,
		},
		{
			name:     "empty string",
			str:      "",
			char:     '@',
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findLastIndex(tt.str, tt.char)
			assert.Equal(t, tt.expected, result)
		})
	}
}
