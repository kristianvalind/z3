package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kristianvalind/z3/internal/backup"
	"github.com/kristianvalind/z3/pkg/snapshot"
	"github.com/spf13/cobra"
)

var (
	// Restore-specific flags
	targetDataset       string
	forceRestore        bool
	restoreAllSnapshots bool
	latestSnapshot      bool
	untilSnapshot       string
)

// restoreCmd represents the restore command
var restoreCmd = &cobra.Command{
	Use:   "restore [SNAPSHOT]",
	Short: "Restore a snapshot from S3 to ZFS",
	Long: `The restore command downloads and restores a snapshot from S3 back to ZFS.

It will automatically restore any parent snapshots needed to create the
target snapshot, ensuring the dependency chain is complete.

Examples:
  # Restore a specific snapshot
  z3 restore tank/data@backup-20240101

  # Restore to a different dataset
  z3 restore tank/data@backup-20240101 --target-dataset tank/restore

  # Restore ALL snapshots from S3
  z3 restore --all-snapshots

  # Restore only the latest snapshot
  z3 restore --latest

  # Restore all snapshots up to a specific one
  z3 restore --until tank/data@backup-20240105

  # Force restore (will rollback the dataset)
  z3 restore tank/data@backup-20240101 --force

  # Dry run to see what would be restored
  z3 restore tank/data@backup-20240101 --dry-run`,
	Args: func(cmd *cobra.Command, args []string) error {
		if restoreAllSnapshots || latestSnapshot || untilSnapshot != "" {
			if len(args) != 0 {
				return fmt.Errorf("snapshot name cannot be specified with --all-snapshots, --latest, or --until")
			}
		} else if len(args) == 0 {
			// No snapshot name provided, and no special flags used
			return fmt.Errorf("snapshot name is required unless using --all-snapshots, --latest, or --until")
		} else if len(args) > 1 {
			return fmt.Errorf("too many arguments, please specify only one snapshot name")
		}
		return nil
	},
	RunE: runRestore,
}

func init() {
	restoreCmd.Flags().StringVar(&targetDataset, "target-dataset", "", "target ZFS dataset (default: original dataset)")
	restoreCmd.Flags().BoolVar(&forceRestore, "force", false, "force restore with zfs recv -F (rollback dataset)")
	restoreCmd.Flags().BoolVar(&restoreAllSnapshots, "all-snapshots", false, "restore all snapshots from S3")
	restoreCmd.Flags().BoolVar(&latestSnapshot, "latest", false, "restore only the latest snapshot")
	restoreCmd.Flags().StringVar(&untilSnapshot, "until", "", "restore all snapshots up to and including this one")

	// Make these flags mutually exclusive
	restoreCmd.MarkFlagsMutuallyExclusive("all-snapshots", "latest", "until")
}

func runRestore(cmd *cobra.Command, args []string) error {
	// Validate arguments
	if err := validateCommonArgs(); err != nil {
		return err
	}

	ctx := context.Background()

	// Create backup manager
	printVerbose("Creating backup manager...")
	manager, err := createBackupManager(ctx)
	if err != nil {
		return err
	}

	// Handle recursive restore modes
	if restoreAllSnapshots || latestSnapshot || untilSnapshot != "" {
		return runRestoreAllSnapshots(ctx, manager)
	}

	// Single snapshot restore
	snapshotName := args[0]
	if snapshotName == "" {
		return fmt.Errorf("snapshot name is required")
	}

	// Prepare restore options
	opts := &backup.RestoreOptions{
		TargetDataset: targetDataset,
		SnapshotName:  snapshotName,
		DryRun:        dryRun,
		Force:         forceRestore,
	}

	// Print status
	if dryRun {
		fmt.Println("=== DRY RUN MODE - No changes will be made ===")
	}

	fmt.Printf("Starting restore of snapshot: %s\n", snapshotName)
	if targetDataset != "" {
		fmt.Printf("Target dataset: %s\n", targetDataset)
	}
	if forceRestore {
		fmt.Println("Force mode: Will rollback target dataset if needed")
	}

	status := manager.GetStatus()
	fmt.Printf("S3 Bucket: %s\n", status["bucket"])
	if prefix, ok := status["prefix"].(string); ok && prefix != "" {
		fmt.Printf("S3 Prefix: %s\n", prefix)
	}
	fmt.Println()

	// Perform restore
	printVerbose("Starting restore operation...")
	startTime := time.Now()

	result, err := manager.Restore(ctx, opts)
	if err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	duration := time.Since(startTime)

	// Print results
	fmt.Printf("Successfully restored snapshot!\n\n")
	fmt.Printf("Restore summary:\n")
	fmt.Printf("  Snapshot: %s\n", result.SnapshotName)
	fmt.Printf("  Target dataset: %s\n", result.RestoredDataset)
	fmt.Printf("  Snapshots restored: %d\n", result.SnapshotsRestored)
	fmt.Printf("  Total size: %s\n", formatSize(result.Size))
	fmt.Printf("  Duration: %v\n", duration.Round(time.Second))

	if result.SnapshotsRestored > 1 {
		fmt.Printf("\nNote: %d dependent snapshots were restored to build the complete chain.\n", result.SnapshotsRestored)
	}

	return nil
}

// runRestoreAllSnapshots handles restoring multiple snapshots
func runRestoreAllSnapshots(ctx context.Context, manager *backup.Manager) error {
	// Get all remote snapshots
	remoteSnaps, err := manager.ListRemoteSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("failed to list remote snapshots: %w", err)
	}

	if len(remoteSnaps) == 0 {
		fmt.Println("No snapshots found in remote storage")
		return nil
	}

	// Get local snapshots to check what already exists
	localSnaps, err := manager.ListLocalSnapshots(ctx, true) // ignore prefix to see all
	if err != nil {
		return fmt.Errorf("failed to list local snapshots: %w", err)
	}

	// Create a map for quick lookup
	localMap := make(map[string]bool)
	for _, snap := range localSnaps {
		localMap[snap.Name] = true
	}

	// Filter snapshots based on mode
	var toRestore []*snapshot.Snapshot

	if latestSnapshot {
		// Only restore the latest snapshot
		latest := remoteSnaps[len(remoteSnaps)-1]
		if !localMap[latest.Name] {
			toRestore = append(toRestore, latest)
		}
	} else if untilSnapshot != "" {
		// Restore up to and including the specified snapshot
		found := false
		for _, snap := range remoteSnaps {
			if !localMap[snap.Name] {
				toRestore = append(toRestore, snap)
			}
			if snap.Name == untilSnapshot {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("snapshot %s not found in remote storage", untilSnapshot)
		}
	} else {
		// Restore all snapshots
		for _, snap := range remoteSnaps {
			if !localMap[snap.Name] {
				toRestore = append(toRestore, snap)
			}
		}
	}

	if len(toRestore) == 0 {
		fmt.Println("All remote snapshots already exist locally!")
		return nil
	}

	// Print what we'll restore
	if dryRun {
		fmt.Println("=== DRY RUN MODE - No changes will be made ===")
	}
	fmt.Printf("Found %d snapshots to restore:\n", len(toRestore))
	for i, snap := range toRestore {
		fmt.Printf("  [%d/%d] %s", i+1, len(toRestore), snap.Name)
		if snap.IsFullBackup {
			fmt.Printf(" (full)")
		} else {
			fmt.Printf(" (incremental)")
		}
		fmt.Println()
	}
	fmt.Println()

	// Restore each snapshot
	totalStartTime := time.Now()
	var totalSize int64
	var successCount, errorCount int

	for i, snap := range toRestore {
		fmt.Printf("\n=== Restoring snapshot %d/%d: %s ===\n", i+1, len(toRestore), snap.Name)

		// Create options for this specific snapshot
		// IMPORTANT: We must set the correct snapshot name for each iteration
		// Apply force only to the first snapshot in the chain
		applyForce := forceRestore && i == 0

		snapOpts := &backup.RestoreOptions{
			TargetDataset: targetDataset,
			SnapshotName:  snap.Name, // Use the current snapshot's name, not the original
			DryRun:        dryRun,
			Force:         applyForce,
		}

		// Perform restore
		result, err := manager.Restore(ctx, snapOpts)
		if err != nil {
			errorCount++
			fmt.Printf("ERROR: Failed to restore %s: %v\n", snap.Name, err)
			// For restore, we should stop on error as subsequent snapshots may depend on this one
			return fmt.Errorf("restore failed at snapshot %s: %w", snap.Name, err)
		}

		successCount++
		totalSize += result.Size

		fmt.Printf("✓ Successfully restored %s\n", snap.Name)
		fmt.Printf("  Size: %s\n", formatSize(result.Size))
		fmt.Printf("  Snapshots in chain: %d\n", result.SnapshotsRestored)
	}

	// Print final summary
	totalDuration := time.Since(totalStartTime)
	fmt.Printf("\n=== Restore Summary ===\n")
	fmt.Printf("Total snapshots processed: %d\n", len(toRestore))
	fmt.Printf("  Successful: %d\n", successCount)
	fmt.Printf("  Failed: %d\n", errorCount)
	fmt.Printf("Total size: %s\n", formatSize(totalSize))
	fmt.Printf("Total duration: %v\n", totalDuration.Round(time.Second))

	if errorCount > 0 {
		return fmt.Errorf("%d snapshots failed to restore", errorCount)
	}

	return nil
}
