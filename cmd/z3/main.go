package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/kristianvalind/z3/internal/backup"
	"github.com/kristianvalind/z3/internal/config"
)

var (
	// Global flags
	configFile     string
	filesystem     string
	s3Prefix       string
	snapshotPrefix string
	bucket         string
	dryRun         bool
	verbose        bool
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "z3",
	Short: "Z3 - ZFS to S3 backup tool",
	Long: `Z3 is a tool for backing up ZFS snapshots to Amazon S3.

It supports full and incremental backups with compression and encryption,
and can restore snapshots from S3 back to ZFS datasets.

This is the Go port of the original Python Z3 tool, providing improved
performance and easier deployment.`,
	SilenceUsage: true,
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file (default is $HOME/.z3.conf)")
	rootCmd.PersistentFlags().StringVar(&filesystem, "filesystem", "", "ZFS filesystem to backup")
	rootCmd.PersistentFlags().StringVar(&s3Prefix, "s3-prefix", "z3-backup/", "S3 key prefix")
	rootCmd.PersistentFlags().StringVar(&snapshotPrefix, "snapshot-prefix", "", "snapshot prefix filter")
	rootCmd.PersistentFlags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "perform a dry run without making changes")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	// Add subcommands
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(restoreCmd)
	rootCmd.AddCommand(statusCmd)
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if configFile != "" {
		// Use config file from the flag
		os.Setenv("Z3_CONFIG_FILE", configFile)
	}
}

// loadConfig loads the configuration with command line overrides
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Override with command line flags
	if filesystem != "" {
		cfg.Filesystem = filesystem
	}
	if s3Prefix != "" {
		cfg.S3Prefix = s3Prefix
	}
	if snapshotPrefix != "" {
		cfg.SnapshotPrefix = snapshotPrefix
	}
	if bucket != "" {
		cfg.Bucket = bucket
	}

	return cfg, nil
}

// createBackupManager creates a backup manager with the current configuration
func createBackupManager(ctx context.Context) (*backup.Manager, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}

	manager, err := backup.NewManager(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup manager: %w", err)
	}

	return manager, nil
}

// printError prints an error message to stderr
func printError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

// printVerbose prints a message if verbose mode is enabled
func printVerbose(format string, args ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", args...)
	}
}

// formatSize formats a size in bytes to a human-readable string
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// formatDuration formats a duration to a human-readable string
func formatDuration(d int64) string {
	if d < 60 {
		return fmt.Sprintf("%ds", d)
	} else if d < 3600 {
		return fmt.Sprintf("%dm%ds", d/60, d%60)
	}
	return fmt.Sprintf("%dh%dm%ds", d/3600, (d%3600)/60, d%60)
}

// validateCommonArgs validates common arguments
func validateCommonArgs() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if cfg.Filesystem == "" {
		return fmt.Errorf("filesystem is required (use --filesystem or set in config file)")
	}

	if cfg.Bucket == "" {
		return fmt.Errorf("S3 bucket is required (use --bucket or set in config file)")
	}

	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		printError(err)
		os.Exit(1)
	}
}