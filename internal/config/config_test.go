package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{
			name:     "plain number",
			input:    "1024",
			expected: 1024,
			wantErr:  false,
		},
		{
			name:     "kilobytes",
			input:    "5K",
			expected: 5 * 1024,
			wantErr:  false,
		},
		{
			name:     "megabytes",
			input:    "100M",
			expected: 100 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "gigabytes",
			input:    "2G",
			expected: 2 * 1024 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "terabytes",
			input:    "1T",
			expected: 1024 * 1024 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "lowercase suffix",
			input:    "5m",
			expected: 5 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "with spaces",
			input:    " 10M ",
			expected: 10 * 1024 * 1024,
			wantErr:  false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid suffix",
			input:    "5X",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid number",
			input:    "abcM",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "no number",
			input:    "M",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseSize(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidateChunkSize(t *testing.T) {
	tests := []struct {
		name    string
		size    string
		wantErr bool
	}{
		{"valid megabytes", "5M", false},
		{"valid kilobytes", "100K", false},
		{"valid gigabytes", "1G", false},
		{"valid plain number", "1048576", false},
		{"empty string", "", true},
		{"invalid suffix", "5X", true},
		{"invalid number", "abcM", true},
		{"too short", "M", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChunkSize(tt.size)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	// Create a temporary directory for test config
	tempDir := t.TempDir()

	// Create .z3 directory and config file
	z3Dir := filepath.Join(tempDir, ".z3")
	err := os.MkdirAll(z3Dir, 0755)
	require.NoError(t, err)

	configFile := filepath.Join(z3Dir, "z3.conf")
	err = os.WriteFile(configFile, []byte("[main]\nBUCKET=test-bucket\nFILESYSTEM=tank"), 0644)
	require.NoError(t, err)

	// Set environment to use our temp config
	oldConfigPath := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldConfigPath)

	config, err := Load()
	require.NoError(t, err)

	// Check defaults are applied
	assert.Equal(t, "z3-backup/", config.S3Prefix)
	assert.Equal(t, "STANDARD_IA", config.S3StorageClass)
	assert.Equal(t, "zfs-auto-snap:daily", config.SnapshotPrefix)
	assert.Equal(t, 64, config.Concurrency)
	assert.Equal(t, 3, config.MaxRetries)
	assert.Equal(t, "5M", config.ChunkSize)
	assert.Equal(t, "pigz1", config.Compressor)
	assert.Equal(t, "z3_backup", config.GPGRecipient)
}

func TestConfigFromFile(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `[main]
BUCKET=my-backup-bucket
S3_KEY_ID=test-key
S3_SECRET=test-secret
FILESYSTEM=tank/data
SNAPSHOT_PREFIX=custom:daily
CONCURRENCY=32
COMPRESSOR=pigz4

[fs:tank/special]
SNAPSHOT_PREFIX=special:hourly
COMPRESSOR=gpg
`

	// Create .z3 directory and config file
	z3Dir := filepath.Join(tempDir, ".z3")
	err := os.MkdirAll(z3Dir, 0755)
	require.NoError(t, err)

	configFile := filepath.Join(z3Dir, "z3.conf")
	err = os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Set HOME to temp directory so config is found
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", oldHome)

	config, err := Load()
	require.NoError(t, err)

	// Check main config
	assert.Equal(t, "my-backup-bucket", config.Bucket)
	assert.Equal(t, "test-key", config.S3KeyID)
	assert.Equal(t, "test-secret", config.S3Secret)
	assert.Equal(t, "tank/data", config.Filesystem)
	assert.Equal(t, "custom:daily", config.SnapshotPrefix)
	assert.Equal(t, 32, config.Concurrency)
	assert.Equal(t, "pigz4", config.Compressor)

	// Check filesystem-specific config
	fsConfig := config.GetFilesystemConfig("tank/special")
	require.NotNil(t, fsConfig)
	assert.Equal(t, "special:hourly", fsConfig.SnapshotPrefix)
	assert.Equal(t, "gpg", fsConfig.Compressor)
}

func TestConfigFromEnvironment(t *testing.T) {
	tempDir := t.TempDir()

	// Create minimal config file
	configContent := `[main]
BUCKET=file-bucket
FILESYSTEM=tank
`
	// Create .z3 directory and config file
	z3Dir := filepath.Join(tempDir, ".z3")
	err := os.MkdirAll(z3Dir, 0755)
	require.NoError(t, err)

	configFile := filepath.Join(z3Dir, "z3.conf")
	err = os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Set environment variables (these should override file values)
	oldEnv := map[string]string{
		"HOME":        os.Getenv("HOME"),
		"BUCKET":      os.Getenv("BUCKET"),
		"CONCURRENCY": os.Getenv("CONCURRENCY"),
	}
	defer func() {
		for key, val := range oldEnv {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
			}
		}
	}()

	os.Setenv("HOME", tempDir)
	os.Setenv("BUCKET", "env-bucket") // Override file value
	os.Setenv("CONCURRENCY", "128")   // Override default

	config, err := Load()
	require.NoError(t, err)

	// Environment should override file and defaults
	assert.Equal(t, "env-bucket", config.Bucket)
	assert.Equal(t, 128, config.Concurrency)
	assert.Equal(t, "tank", config.Filesystem) // From file
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				Bucket:     "test-bucket",
				Filesystem: "tank",
				ChunkSize:  "5M",
			},
			wantErr: false,
		},
		{
			name: "missing bucket",
			config: Config{
				Filesystem: "tank",
				ChunkSize:  "5M",
			},
			wantErr: true,
			errMsg:  "BUCKET is required",
		},
		{
			name: "missing filesystem",
			config: Config{
				Bucket:    "test-bucket",
				ChunkSize: "5M",
			},
			wantErr: true,
			errMsg:  "FILESYSTEM is required",
		},
		{
			name: "invalid chunk size",
			config: Config{
				Bucket:     "test-bucket",
				Filesystem: "tank",
				ChunkSize:  "invalid",
			},
			wantErr: true,
			errMsg:  "invalid CHUNK_SIZE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(&tt.config)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetFilesystemConfig(t *testing.T) {
	config := &Config{
		FilesystemConfigs: map[string]*FilesystemConfig{
			"fs:tank/special": {
				SnapshotPrefix: "special:daily",
				Compressor:     "gpg",
			},
		},
	}

	// Test existing filesystem config
	fsConfig := config.GetFilesystemConfig("tank/special")
	require.NotNil(t, fsConfig)
	assert.Equal(t, "special:daily", fsConfig.SnapshotPrefix)
	assert.Equal(t, "gpg", fsConfig.Compressor)

	// Test non-existing filesystem config
	fsConfig = config.GetFilesystemConfig("tank/other")
	assert.Nil(t, fsConfig)
}

func TestGetSnapshotPrefix(t *testing.T) {
	config := &Config{
		SnapshotPrefix: "global:daily",
		FilesystemConfigs: map[string]*FilesystemConfig{
			"fs:tank/special": {
				SnapshotPrefix: "special:hourly",
			},
		},
	}

	// Test filesystem with specific config
	prefix := config.GetSnapshotPrefix("tank/special")
	assert.Equal(t, "special:hourly", prefix)

	// Test filesystem without specific config (should use global)
	prefix = config.GetSnapshotPrefix("tank/regular")
	assert.Equal(t, "global:daily", prefix)
}

func TestGetCompressor(t *testing.T) {
	config := &Config{
		Compressor: "pigz4",
		FilesystemConfigs: map[string]*FilesystemConfig{
			"fs:tank/encrypted": {
				Compressor: "gpg",
			},
		},
	}

	// Test filesystem with specific compressor
	compressor := config.GetCompressor("tank/encrypted")
	assert.Equal(t, "gpg", compressor)

	// Test filesystem without specific compressor (should use global)
	compressor = config.GetCompressor("tank/regular")
	assert.Equal(t, "pigz4", compressor)
}

func TestParseChunkSize(t *testing.T) {
	config := &Config{
		ChunkSize: "10M",
	}

	size, err := config.ParseChunkSize()
	require.NoError(t, err)
	assert.Equal(t, int64(10*1024*1024), size)
}

func TestConfigString(t *testing.T) {
	config := &Config{
		Bucket:         "test-bucket",
		Filesystem:     "tank/data",
		SnapshotPrefix: "test:daily",
		Concurrency:    32,
		S3Secret:       "secret-value", // Should not appear in string
	}

	str := config.String()
	assert.Contains(t, str, "test-bucket")
	assert.Contains(t, str, "tank/data")
	assert.Contains(t, str, "test:daily")
	assert.Contains(t, str, "32")
	assert.NotContains(t, str, "secret-value") // Secrets should not be included
}
