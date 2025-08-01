// Package config provides configuration management for Z3.
//
// It supports multiple configuration sources with precedence:
// 1. Command line flags
// 2. Environment variables
// 3. Configuration files
// 4. Default values
//
// The package maintains compatibility with the original Python Z3
// configuration format while adding Go-specific improvements.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all configuration values for Z3
type Config struct {
	// S3 Configuration
	Bucket         string `mapstructure:"BUCKET"`
	S3KeyID        string `mapstructure:"S3_KEY_ID"`
	S3Secret       string `mapstructure:"S3_SECRET"`
	S3Prefix       string `mapstructure:"S3_PREFIX"`
	Host           string `mapstructure:"HOST"`
	AWSRegion      string `mapstructure:"AWS_REGION"`
	S3StorageClass string `mapstructure:"S3_STORAGE_CLASS"`

	// ZFS Configuration
	Filesystem     string `mapstructure:"FILESYSTEM"`
	SnapshotPrefix string `mapstructure:"SNAPSHOT_PREFIX"`

	// Upload Configuration
	Concurrency int    `mapstructure:"CONCURRENCY"`
	MaxRetries  int    `mapstructure:"MAX_RETRIES"`
	ChunkSize   string `mapstructure:"CHUNK_SIZE"`

	// Compression Configuration
	Compressor   string `mapstructure:"COMPRESSOR"`
	GPGRecipient string `mapstructure:"GPG_RECIPIENT"`

	// Per-filesystem configurations
	FilesystemConfigs map[string]*FilesystemConfig `mapstructure:"-"`
}

// FilesystemConfig holds per-filesystem configuration overrides
type FilesystemConfig struct {
	SnapshotPrefix string `mapstructure:"SNAPSHOT_PREFIX"`
	Compressor     string `mapstructure:"COMPRESSOR"`
}

// defaults contains the default configuration values
var defaults = map[string]interface{}{
	"S3_PREFIX":        "z3-backup/",
	"S3_STORAGE_CLASS": "STANDARD_IA",
	"SNAPSHOT_PREFIX":  "zfs-auto-snap:daily",
	"CONCURRENCY":      64,
	"MAX_RETRIES":      3,
	"CHUNK_SIZE":       "5M",
	"COMPRESSOR":       "pigz1",
	"GPG_RECIPIENT":    "z3_backup",
}

// configPaths defines the search paths for configuration files
var configPaths = []string{
	"/etc/z3_backup/",
	"$HOME/.z3/",
	".",
}

// configNames defines the configuration file names to search for
var configNames = []string{
	"z3.conf",
	"z3",
}

// Load loads configuration from multiple sources with proper precedence
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	for key, value := range defaults {
		v.SetDefault(key, value)
	}

	// Configure Viper
	v.AutomaticEnv()

	// Try to find and read configuration file
	configFile := findConfigFile()
	if configFile != "" {
		// Read the INI file using our custom parser to maintain Python compatibility
		if err := readINIConfig(v, configFile); err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", configFile, err)
		}
	}

	// Ensure environment variables override file values
	// Check for all possible config keys in environment
	configKeys := []string{
		"BUCKET", "S3_KEY_ID", "S3_SECRET", "S3_PREFIX", "HOST", "S3_STORAGE_CLASS",
		"FILESYSTEM", "SNAPSHOT_PREFIX", "CONCURRENCY", "MAX_RETRIES", "CHUNK_SIZE",
		"COMPRESSOR", "GPG_RECIPIENT",
	}

	for _, key := range configKeys {
		if envVal := os.Getenv(key); envVal != "" {
			v.Set(key, envVal)
		}
	}

	// Unmarshal into config struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Load per-filesystem configurations
	if err := loadFilesystemConfigs(v, &config); err != nil {
		return nil, fmt.Errorf("failed to load filesystem configs: %w", err)
	}

	// Validate required fields
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// loadFilesystemConfigs loads per-filesystem configuration sections
func loadFilesystemConfigs(v *viper.Viper, config *Config) error {
	config.FilesystemConfigs = make(map[string]*FilesystemConfig)

	// Get all keys from viper
	allKeys := v.AllKeys()

	// Group keys by filesystem sections (fs:filesystem_name)
	fsConfigs := make(map[string]map[string]interface{})

	for _, key := range allKeys {
		if strings.HasPrefix(key, "fs:") {
			// Extract filesystem name and config key
			parts := strings.SplitN(key, ".", 2)
			if len(parts) != 2 {
				continue
			}

			fsSection := parts[0] // This includes "fs:filesystem_name"
			configKey := strings.ToUpper(parts[1])

			if fsConfigs[fsSection] == nil {
				fsConfigs[fsSection] = make(map[string]interface{})
			}

			fsConfigs[fsSection][configKey] = v.Get(key)
		}
	}

	// Convert to FilesystemConfig structs
	for fsSection, fsConfig := range fsConfigs {
		fc := &FilesystemConfig{}

		if val, exists := fsConfig["SNAPSHOT_PREFIX"]; exists {
			if str, ok := val.(string); ok {
				fc.SnapshotPrefix = str
			}
		}

		if val, exists := fsConfig["COMPRESSOR"]; exists {
			if str, ok := val.(string); ok {
				fc.Compressor = str
			}
		}

		config.FilesystemConfigs[fsSection] = fc
	}

	return nil
}

// validateConfig validates required configuration fields
func validateConfig(config *Config) error {
	if config.Bucket == "" {
		return fmt.Errorf("BUCKET is required")
	}

	if config.Filesystem == "" {
		return fmt.Errorf("FILESYSTEM is required")
	}

	// Validate chunk size format
	if err := validateChunkSize(config.ChunkSize); err != nil {
		return fmt.Errorf("invalid CHUNK_SIZE: %w", err)
	}

	return nil
}

// validateChunkSize validates the chunk size format (e.g., "5M", "100K", "1G")
func validateChunkSize(size string) error {
	if size == "" {
		return fmt.Errorf("chunk size cannot be empty")
	}

	size = strings.ToUpper(strings.TrimSpace(size))
	if len(size) < 2 {
		return fmt.Errorf("invalid format")
	}

	// Check suffix
	suffix := size[len(size)-1:]
	if suffix != "K" && suffix != "M" && suffix != "G" && suffix != "T" {
		// Try to parse as plain number
		if _, err := strconv.ParseInt(size, 10, 64); err != nil {
			return fmt.Errorf("invalid format, expected number with K/M/G/T suffix or plain number")
		}
		return nil
	}

	// Parse number part
	numberPart := size[:len(size)-1]
	if _, err := strconv.ParseInt(numberPart, 10, 64); err != nil {
		return fmt.Errorf("invalid number part: %w", err)
	}

	return nil
}

// GetFilesystemConfig returns configuration for a specific filesystem
func (c *Config) GetFilesystemConfig(filesystem string) *FilesystemConfig {
	key := fmt.Sprintf("fs:%s", filesystem)
	if fc, exists := c.FilesystemConfigs[key]; exists {
		return fc
	}
	return nil
}

// GetSnapshotPrefix returns the snapshot prefix for a filesystem, with fallback to global config
func (c *Config) GetSnapshotPrefix(filesystem string) string {
	if fc := c.GetFilesystemConfig(filesystem); fc != nil && fc.SnapshotPrefix != "" {
		return fc.SnapshotPrefix
	}
	return c.SnapshotPrefix
}

// GetCompressor returns the compressor for a filesystem, with fallback to global config
func (c *Config) GetCompressor(filesystem string) string {
	if fc := c.GetFilesystemConfig(filesystem); fc != nil && fc.Compressor != "" {
		return fc.Compressor
	}
	return c.Compressor
}

// ParseChunkSize converts a chunk size string to bytes
func (c *Config) ParseChunkSize() (int64, error) {
	return ParseSize(c.ChunkSize)
}

// ParseSize converts a size string (e.g., "5M", "100K") to bytes
func ParseSize(size string) (int64, error) {
	if size == "" {
		return 0, fmt.Errorf("size cannot be empty")
	}

	size = strings.ToUpper(strings.TrimSpace(size))

	// Try to parse as plain number first
	if val, err := strconv.ParseInt(size, 10, 64); err == nil {
		return val, nil
	}

	if len(size) < 2 {
		return 0, fmt.Errorf("invalid size format")
	}

	// Parse with suffix
	suffix := size[len(size)-1:]
	numberPart := size[:len(size)-1]

	val, err := strconv.ParseInt(numberPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number part: %w", err)
	}

	switch suffix {
	case "K":
		return val * 1024, nil
	case "M":
		return val * 1024 * 1024, nil
	case "G":
		return val * 1024 * 1024 * 1024, nil
	case "T":
		return val * 1024 * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("invalid suffix %s, expected K/M/G/T", suffix)
	}
}

// String returns a string representation of the configuration (without secrets)
func (c *Config) String() string {
	return fmt.Sprintf("Config{Bucket: %s, Filesystem: %s, SnapshotPrefix: %s, Concurrency: %d}",
		c.Bucket, c.Filesystem, c.SnapshotPrefix, c.Concurrency)
}

// findConfigFile searches for configuration files in the standard paths
func findConfigFile() string {
	// Try all combinations of paths and names
	for _, path := range configPaths {
		expandedPath := os.ExpandEnv(path)
		for _, name := range configNames {
			fullPath := filepath.Join(expandedPath, name)
			if _, err := os.Stat(fullPath); err == nil {
				return fullPath
			}
		}
	}

	// Also try the z3 directory for compatibility
	defaultConfigPath := filepath.Join("z3", "z3.conf")
	if _, err := os.Stat(defaultConfigPath); err == nil {
		return defaultConfigPath
	}

	return ""
}

// readINIConfig reads an INI configuration file in Python ConfigParser format
func readINIConfig(v *viper.Viper, filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	currentSection := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		// Check for section headers [section]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.Trim(line, "[]")
			continue
		}

		// Parse key=value pairs
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])

				// For main section, set directly
				if currentSection == "main" || currentSection == "" {
					v.Set(key, value)
				} else {
					// For other sections, prefix with section name
					sectionKey := fmt.Sprintf("%s.%s", currentSection, key)
					v.Set(sectionKey, value)
				}
			}
		}
	}

	return scanner.Err()
}
