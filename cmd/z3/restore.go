package main

import (
	"context"
	"fmt"
	"time"

	"github.com/kristianvalind/z3/internal/backup"
	"github.com/spf13/cobra"
)

var (
	// Restore-specific flags
	targetDataset string
	forceRestore  bool
)

// restoreCmd represents the restore command
var restoreCmd = &cobra.Command{
	Use:   "restore SNAPSHOT",
	Short: "Restore a snapshot from S3 to ZFS",
	Long: `The restore command downloads and restores a snapshot from S3 back to ZFS.

It will automatically restore any parent snapshots needed to create the
target snapshot, ensuring the dependency chain is complete.

Examples:
  # Restore a specific snapshot
  z3 restore tank/data@backup-20240101

  # Restore to a different dataset
  z3 restore tank/data@backup-20240101 --target-dataset tank/restore

  # Force restore (will rollback the dataset)
  z3 restore tank/data@backup-20240101 --force

  # Dry run to see what would be restored
  z3 restore tank/data@backup-20240101 --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: runRestore,
}

func init() {
	restoreCmd.Flags().StringVar(&targetDataset, "target-dataset", "", "target ZFS dataset (default: original dataset)")
	restoreCmd.Flags().BoolVar(&forceRestore, "force", false, "force restore with zfs recv -F (rollback dataset)")
}

func runRestore(cmd *cobra.Command, args []string) error {
	snapshotName := args[0]

	// Validate arguments
	if err := validateCommonArgs(); err != nil {
		return err
	}

	if snapshotName == "" {
		return fmt.Errorf("snapshot name is required")
	}

	ctx := context.Background()

	// Create backup manager
	printVerbose("Creating backup manager...")
	manager, err := createBackupManager(ctx)
	if err != nil {
		return err
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
