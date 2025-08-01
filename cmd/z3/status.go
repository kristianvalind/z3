package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/kristianvalind/z3/internal/s3"
	"github.com/kristianvalind/z3/internal/zfs"
	"github.com/kristianvalind/z3/pkg/snapshot"
	"github.com/spf13/cobra"
)

var (
	// Status-specific flags
	showAll    bool
	showLocal  bool
	showRemote bool
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show backup status and snapshot listing",
	Long: `The status command displays the current backup status, showing which
snapshots exist locally and remotely, and their health status.

Examples:
  # Show all snapshots
  z3 status

  # Show only local snapshots
  z3 status --local

  # Show only remote snapshots  
  z3 status --remote

  # Show all snapshots including healthy ones
  z3 status --all`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&showAll, "all", false, "show all snapshots including healthy ones")
	statusCmd.Flags().BoolVar(&showLocal, "local", false, "show only local snapshots")
	statusCmd.Flags().BoolVar(&showRemote, "remote", false, "show only remote snapshots")

	// Make local and remote mutually exclusive
	statusCmd.MarkFlagsMutuallyExclusive("local", "remote")
}

func runStatus(cmd *cobra.Command, args []string) error {
	// Validate arguments
	if err := validateCommonArgs(); err != nil {
		return err
	}

	ctx := context.Background()

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Create managers
	printVerbose("Creating ZFS manager...")
	zfsManager := zfs.NewManager(cfg)

	printVerbose("Creating S3 client...")
	s3Client, err := s3.NewClient(ctx, cfg, nil)
	if err != nil {
		return fmt.Errorf("failed to create S3 client: %w", err)
	}

	// Get local snapshots
	var localSnapshots snapshot.SnapshotList
	if !showRemote {
		printVerbose("Listing local snapshots...")
		localSnapshots, err = zfsManager.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list local snapshots: %w", err)
		}
	}

	// Get remote snapshots
	var remoteSnapshots snapshot.SnapshotList
	if !showLocal {
		printVerbose("Listing remote snapshots...")
		remoteSnapshots, err = s3Client.ListSnapshots(ctx, cfg.Filesystem)
		if err != nil {
			return fmt.Errorf("failed to list remote snapshots: %w", err)
		}
	}

	// Print header
	fmt.Printf("Backup status for %s@%s* on %s", cfg.Filesystem, cfg.SnapshotPrefix, cfg.Bucket)
	if cfg.S3Prefix != "" {
		fmt.Printf("/%s", cfg.S3Prefix)
	}
	fmt.Println()
	fmt.Println()

	// Determine what to show
	if showLocal {
		printSnapshotList("Local Snapshots", localSnapshots, nil)
	} else if showRemote {
		printSnapshotList("Remote Snapshots", remoteSnapshots, remoteSnapshots)
	} else {
		// Show combined view
		printCombinedStatus(localSnapshots, remoteSnapshots)
	}

	return nil
}

// printSnapshotList prints a list of snapshots
func printSnapshotList(title string, snapshots snapshot.SnapshotList, healthManager snapshot.SnapshotList) {
	fmt.Printf("%s (%d total):\n", title, len(snapshots))

	if len(snapshots) == 0 {
		fmt.Println("  (none)")
		return
	}

	// Table headers
	fmt.Printf("%-40s %-12s %-10s %-15s %s\n", "NAME", "TYPE", "HEALTH", "SIZE", "CREATED")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	for _, snap := range snapshots {
		shortName := snap.GetShortName()
		if len(shortName) > 38 {
			shortName = shortName[:35] + "..."
		}

		snapType := "incremental"
		if snap.IsFullBackup {
			snapType = "full"
		}

		health := "ok"
		if healthManager != nil {
			manager := &snapshotListManager{snapshots: healthManager}
			if !snap.IsHealthy(manager) {
				// Get health reason (this would need to be implemented in the snapshot package)
				health = "broken"
			}
		}

		size := formatSize(snap.Size)
		created := snap.CreatedAt.Format("2006-01-02 15:04")

		fmt.Printf("%-40s %-12s %-10s %-15s %s\n", shortName, snapType, health, size, created)
	}
	fmt.Println()
}

// printCombinedStatus prints a combined view of local and remote snapshots
func printCombinedStatus(localSnapshots, remoteSnapshots snapshot.SnapshotList) {
	// Create a map for easier lookup
	localMap := make(map[string]*snapshot.Snapshot)
	for _, snap := range localSnapshots {
		localMap[snap.Name] = snap
	}

	remoteMap := make(map[string]*snapshot.Snapshot)
	for _, snap := range remoteSnapshots {
		remoteMap[snap.Name] = snap
	}

	// Get all unique snapshot names
	allNames := make(map[string]bool)
	for name := range localMap {
		allNames[name] = true
	}
	for name := range remoteMap {
		allNames[name] = true
	}

	// Convert to sorted slice
	var names []string
	for name := range allNames {
		names = append(names, name)
	}

	// Print table headers
	fmt.Printf("%-35s %-15s %-12s %-10s %-12s %s\n", "NAME", "PARENT", "TYPE", "HEALTH", "LOCAL", "SIZE")
	fmt.Printf("%s\n", strings.Repeat("-", 95))

	for _, name := range names {
		localSnap := localMap[name]
		remoteSnap := remoteMap[name]

		// Use remote snapshot for most info, fall back to local
		snap := remoteSnap
		if snap == nil {
			snap = localSnap
		}

		shortName := snap.GetShortName()
		if len(shortName) > 33 {
			shortName = shortName[:30] + "..."
		}

		// Parent name
		parentName := ""
		if !snap.IsFullBackup && snap.ParentName != "" {
			parentParts := strings.Split(snap.ParentName, "@")
			if len(parentParts) > 1 {
				parentName = parentParts[1]
				if len(parentName) > 13 {
					parentName = parentName[:10] + "..."
				}
			}
		}

		// Type
		snapType := "incremental"
		if snap.IsFullBackup {
			snapType = "full"
		}

		// Health (only for remote snapshots)
		health := "-"
		if remoteSnap != nil {
			health = "ok"
			if len(remoteSnapshots) > 0 {
				manager := &snapshotListManager{snapshots: remoteSnapshots}
				if !remoteSnap.IsHealthy(manager) {
					health = "broken"
				}
			}
		}

		// Local state
		localState := "missing"
		if localSnap != nil {
			localState = "ok"
		}

		// Size
		size := ""
		if remoteSnap != nil && remoteSnap.Size > 0 {
			size = formatSize(remoteSnap.Size)
		}

		fmt.Printf("%-35s %-15s %-12s %-10s %-12s %s\n",
			shortName, parentName, snapType, health, localState, size)
	}

	fmt.Println()

	// Print summary
	localCount := len(localSnapshots)
	remoteCount := len(remoteSnapshots)

	// Count missing snapshots
	missingFromRemote := 0
	for name := range localMap {
		if remoteMap[name] == nil {
			missingFromRemote++
		}
	}

	missingFromLocal := 0
	for name := range remoteMap {
		if localMap[name] == nil {
			missingFromLocal++
		}
	}

	fmt.Printf("Summary:\n")
	fmt.Printf("  Local snapshots: %d\n", localCount)
	fmt.Printf("  Remote snapshots: %d\n", remoteCount)
	if missingFromRemote > 0 {
		fmt.Printf("  Missing from remote: %d\n", missingFromRemote)
	}
	if missingFromLocal > 0 {
		fmt.Printf("  Missing from local: %d\n", missingFromLocal)
	}
}

// snapshotListManager implements SnapshotManager for SnapshotList (for health checking)
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
