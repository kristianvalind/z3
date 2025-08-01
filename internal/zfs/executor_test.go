package zfs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandExecutor_DryRun(t *testing.T) {
	executor := NewCommandExecutor()
	executor.DryRun = true

	ctx := context.Background()
	args := []string{"list", "-t", "snapshot"}

	result, err := executor.Execute(ctx, args, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ExitCode)
	assert.Contains(t, result.Command, "zfs list -t snapshot")
}

func TestCommandExecutor_ExtractDatasetFromArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "send with snapshot",
			args:     []string{"send", "tank/data@snap1"},
			expected: "tank/data",
		},
		{
			name:     "list with filesystem",
			args:     []string{"list", "tank/data"},
			expected: "tank/data",
		},
		{
			name:     "recv with dataset",
			args:     []string{"recv", "tank/backup"},
			expected: "tank/backup",
		},
		{
			name:     "no dataset in args",
			args:     []string{"list", "-t", "snapshot"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractDatasetFromArgs(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCommandExecutor_ExtractSnapshotFromArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "send with snapshot",
			args:     []string{"send", "tank/data@snap1"},
			expected: "snap1",
		},
		{
			name:     "incremental send",
			args:     []string{"send", "-i", "tank/data@snap1", "tank/data@snap2"},
			expected: "snap1", // First snapshot found
		},
		{
			name:     "no snapshot in args",
			args:     []string{"list", "-t", "snapshot"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractSnapshotFromArgs(tt.args)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseSendSizeOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int64
		wantErr  bool
	}{
		{
			name:     "valid size output",
			output:   "send from @ to tank/data@snap1 estimated size is 1234567890",
			expected: 1234567890,
			wantErr:  false,
		},
		{
			name:     "multiple lines with size",
			output:   "incremental send\nsize: 987654321\nother info",
			expected: 987654321,
			wantErr:  false,
		},
		{
			name:     "no size in output",
			output:   "some other output without size",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "empty output",
			output:   "",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSendSizeOutput(tt.output)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestListOptions_BuildArgs(t *testing.T) {
	executor := NewCommandExecutor()
	executor.DryRun = true // Use dry run for testing

	ctx := context.Background()

	tests := []struct {
		name     string
		options  ListOptions
		expected []string
	}{
		{
			name: "basic snapshot list",
			options: ListOptions{
				Type:       "snapshot",
				Properties: []string{"name", "used"},
				Parseable:  true,
			},
			expected: []string{"list", "-t", "snapshot", "-o", "name,used", "-H", "-p"},
		},
		{
			name: "recursive with depth",
			options: ListOptions{
				Type:      "filesystem",
				Recursive: true,
				Depth:     2,
				Dataset:   "tank",
			},
			expected: []string{"list", "-t", "filesystem", "-r", "-d", "2", "tank"},
		},
		{
			name: "with sorting",
			options: ListOptions{
				Type: "snapshot",
				Sort: "creation",
			},
			expected: []string{"list", "-t", "snapshot", "-s", "creation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.List(ctx, tt.options)
			require.NoError(t, err)

			// Check that the command contains the expected arguments
			for _, expectedArg := range tt.expected {
				assert.Contains(t, result.Command, expectedArg)
			}
		})
	}
}

func TestSendOptions_BuildArgs(t *testing.T) {
	executor := NewCommandExecutor()
	executor.DryRun = true // Use dry run for testing

	ctx := context.Background()

	tests := []struct {
		name     string
		options  SendOptions
		expected []string
	}{
		{
			name: "basic send",
			options: SendOptions{
				Snapshot: "tank/data@snap1",
				Verbose:  true,
			},
			expected: []string{"send", "-v", "tank/data@snap1"},
		},
		{
			name: "incremental send",
			options: SendOptions{
				Snapshot:     "tank/data@snap2",
				FromSnapshot: "tank/data@snap1",
				Incremental:  true,
				DryRun:       true,
			},
			expected: []string{"send", "-n", "-i", "tank/data@snap1", "tank/data@snap2"},
		},
		{
			name: "send with properties",
			options: SendOptions{
				Snapshot:          "tank/data@snap1",
				IncludeProperties: true,
				ParseableOutput:   true,
			},
			expected: []string{"send", "-P", "-p", "tank/data@snap1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := executor.Send(ctx, tt.options)
			require.NoError(t, err)

			// Check that the command contains the expected arguments
			for _, expectedArg := range tt.expected {
				assert.Contains(t, result.Command, expectedArg)
			}
		})
	}
}

func TestReceiveOptions_BuildArgs(t *testing.T) {
	executor := NewCommandExecutor()
	executor.DryRun = true // Use dry run for testing

	ctx := context.Background()

	tests := []struct {
		name     string
		options  ReceiveOptions
		expected []string
	}{
		{
			name: "basic receive",
			options: ReceiveOptions{
				Dataset: "tank/restore",
				Verbose: true,
			},
			expected: []string{"recv", "-v", "tank/restore"},
		},
		{
			name: "force receive with discard first name",
			options: ReceiveOptions{
				Dataset:          "tank/restore",
				Force:            true,
				DiscardFirstName: true,
				DryRun:           true,
			},
			expected: []string{"recv", "-n", "-F", "-d", "tank/restore"},
		},
		{
			name: "receive with use last name",
			options: ReceiveOptions{
				Dataset:     "tank/restore",
				UseLastName: true,
			},
			expected: []string{"recv", "-e", "tank/restore"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use strings.NewReader as a dummy input
			tt.options.Input = strings.NewReader("dummy data")

			result, err := executor.Receive(ctx, tt.options)
			require.NoError(t, err)

			// Check that the command contains the expected arguments
			for _, expectedArg := range tt.expected {
				assert.Contains(t, result.Command, expectedArg)
			}
		})
	}
}

func TestCommandExecutor_Timeout(t *testing.T) {
	executor := NewCommandExecutor()
	executor.Timeout = 100 * time.Millisecond // Very short timeout

	ctx := context.Background()
	
	// This would normally take longer than 100ms if it were real
	// but since we're using dry run, it should complete quickly
	executor.DryRun = true
	
	args := []string{"send", "tank/data@snapshot"}
	opts := &CommandOptions{
		Timeout: 1 * time.Second, // Override with longer timeout
	}

	result, err := executor.Execute(ctx, args, opts)
	require.NoError(t, err)
	assert.Contains(t, result.Command, "zfs send tank/data@snapshot")
}

func TestCommandExecutor_NewCommandExecutor(t *testing.T) {
	executor := NewCommandExecutor()

	assert.Equal(t, "zfs", executor.ZFSPath)
	assert.Equal(t, 5*time.Minute, executor.Timeout)
	assert.False(t, executor.DryRun)
}

func TestCommandOptions_Defaults(t *testing.T) {
	executor := NewCommandExecutor()
	executor.DryRun = true

	ctx := context.Background()
	args := []string{"list"}

	// Test with nil options
	result, err := executor.Execute(ctx, args, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Command)

	// Test with empty options
	result, err = executor.Execute(ctx, args, &CommandOptions{})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Command)
}