package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/kristianvalind/z3/internal/backup"
)

var (
	// Backup-specific flags
	snapshotName    string
	full            bool
	incremental     bool
	compressor      string
	gpgRecipient    string
	storageClass    string
	parseable       bool
	force           bool
)

// backupCmd represents the backup command
var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Backup ZFS snapshots to S3",
	Long: `The backup command uploads ZFS snapshots to Amazon S3.

By default, it performs an incremental backup, uploading only the snapshots
that are missing from S3. You can force a full backup using --full.

Examples:
  # Backup latest snapshot incrementally
  z3 backup

  # Perform a full backup of latest snapshot
  z3 backup --full

  # Backup a specific snapshot
  z3 backup --snapshot snap1

  # Dry run to see what would be backed up
  z3 backup --dry-run

  # Use specific compression
  z3 backup --compressor pigz4`,
	RunE: runBackup,
}

func init() {
	backupCmd.Flags().StringVar(&snapshotName, "snapshot", "", "specific snapshot to backup (default: latest)")
	backupCmd.Flags().BoolVar(&full, "full", false, "perform full backup instead of incremental")
	backupCmd.Flags().BoolVar(&incremental, "incremental", true, "perform incremental backup (default)")
	backupCmd.Flags().StringVar(&compressor, "compressor", "", "compression method (pigz1, pigz4, gpg, none)")
	backupCmd.Flags().StringVar(&gpgRecipient, "gpg-recipient", "", "GPG recipient for encryption")
	backupCmd.Flags().StringVar(&storageClass, "storage-class", "", "S3 storage class (STANDARD, STANDARD_IA, GLACIER, etc.)")
	backupCmd.Flags().BoolVar(&parseable, "parseable", false, "machine-readable output")
	backupCmd.Flags().BoolVar(&force, "force", false, "force backup even if snapshot already exists")

	// Make full and incremental mutually exclusive
	backupCmd.MarkFlagsMutuallyExclusive("full", "incremental")
}

func runBackup(cmd *cobra.Command, args []string) error {
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

	// Prepare backup options
	opts := &backup.BackupOptions{
		DryRun:          dryRun,
		TargetSnapshot:  snapshotName,
		ForceFullBackup: full,
		StorageClass:    storageClass,
		Metadata:        make(map[string]string),
	}

	// Add metadata
	opts.Metadata["backup_tool"] = "z3-go"
	opts.Metadata["backup_time"] = time.Now().UTC().Format(time.RFC3339)

	// Print status if not in parseable mode
	if !parseable {
		if dryRun {
			fmt.Println("=== DRY RUN MODE - No changes will be made ===")
		}

		backupType := "incremental"
		if full {
			backupType = "full"
		}

		if snapshotName != "" {
			fmt.Printf("Starting %s backup of snapshot: %s\n", backupType, snapshotName)
		} else {
			fmt.Printf("Starting %s backup of latest snapshot\n", backupType)
		}

		status := manager.GetStatus()
		fmt.Printf("Filesystem: %s\n", status["filesystem"])
		fmt.Printf("S3 Bucket: %s\n", status["bucket"])
		if prefix, ok := status["prefix"].(string); ok && prefix != "" {
			fmt.Printf("S3 Prefix: %s\n", prefix)
		}
		fmt.Println()
	}

	// Perform backup
	printVerbose("Starting backup operation...")
	startTime := time.Now()

	result, err := manager.Backup(ctx, opts)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	duration := time.Since(startTime)

	// Print results
	if parseable {
		// Machine-readable output
		for _, uploadResult := range result.SnapshotsUploaded {
			fmt.Printf("%s\x00%d\n", uploadResult.SnapshotName, uploadResult.Size)
		}
	} else {
		// Human-readable output
		if len(result.SnapshotsUploaded) == 0 {
			fmt.Println("No snapshots needed backup - everything is up to date!")
		} else {
			fmt.Printf("Successfully backed up %d snapshot(s):\n\n", len(result.SnapshotsUploaded))
			
			for _, uploadResult := range result.SnapshotsUploaded {
				backupTypeStr := "incremental"
				if uploadResult.IsFullBackup {
					backupTypeStr = "full"
				}
				
				fmt.Printf("  %s (%s)\n", uploadResult.SnapshotName, backupTypeStr)
				fmt.Printf("    Size: %s", formatSize(uploadResult.Size))
				if uploadResult.CompressedSize > 0 && uploadResult.CompressedSize != uploadResult.Size {
					ratio := float64(uploadResult.CompressedSize) / float64(uploadResult.Size) * 100
					fmt.Printf(" → %s (%.1f%%)", formatSize(uploadResult.CompressedSize), ratio)
				}
				fmt.Println()
				
				if uploadResult.ParentName != "" {
					fmt.Printf("    Parent: %s\n", uploadResult.ParentName)
				}
				
				fmt.Printf("    Duration: %v\n", uploadResult.Duration.Round(time.Second))
				if uploadResult.ETag != "" {
					fmt.Printf("    ETag: %s\n", uploadResult.ETag)
				}
				fmt.Println()
			}

			fmt.Printf("Total backup summary:\n")
			fmt.Printf("  Total size: %s", formatSize(result.TotalSize))
			if result.CompressedSize > 0 && result.CompressedSize != result.TotalSize {
				ratio := float64(result.CompressedSize) / float64(result.TotalSize) * 100
				fmt.Printf(" → %s (%.1f%%)", formatSize(result.CompressedSize), ratio)
			}
			fmt.Println()
			fmt.Printf("  Duration: %v\n", duration.Round(time.Second))
			fmt.Printf("  Backup type: %s\n", result.BackupType)
		}
	}

	return nil
}