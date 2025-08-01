// Package zfs provides ZFS operations and snapshot management.
//
// This package implements the ZFS-specific functionality for the Z3 backup
// system, including command execution, snapshot parsing, and data streaming.
// It maintains compatibility with the original Python implementation while
// providing Go-specific improvements and better error handling.
package zfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kristianvalind/z3/pkg/snapshot"
)

// CommandExecutor handles execution of ZFS commands
type CommandExecutor struct {
	// ZFSPath is the path to the zfs binary (default: "zfs")
	ZFSPath string

	// Timeout is the default timeout for ZFS commands
	Timeout time.Duration

	// DryRun indicates whether to actually execute commands
	DryRun bool
}

// NewCommandExecutor creates a new ZFS command executor
func NewCommandExecutor() *CommandExecutor {
	return &CommandExecutor{
		ZFSPath: "zfs",
		Timeout: 5 * time.Minute, // Default timeout
		DryRun:  false,
	}
}

// CommandOptions configures command execution
type CommandOptions struct {
	// Timeout overrides the default timeout
	Timeout time.Duration

	// DryRun overrides the default dry run setting
	DryRun *bool

	// CaptureOutput determines whether to capture stdout/stderr
	CaptureOutput bool

	// Input provides stdin for the command
	Input io.Reader

	// Output provides stdout for the command
	Output io.Writer

	// StreamOutput enables streaming output for long-running commands
	StreamOutput bool
}

// CommandResult contains the result of a command execution
type CommandResult struct {
	// ExitCode is the exit code of the command
	ExitCode int

	// Stdout contains the captured stdout (if CaptureOutput was true)
	Stdout string

	// Stderr contains the captured stderr (if CaptureOutput was true)
	Stderr string

	// Duration is how long the command took to execute
	Duration time.Duration

	// Command is the full command that was executed
	Command string
}

// Execute runs a ZFS command with the given arguments
func (ce *CommandExecutor) Execute(ctx context.Context, args []string, opts *CommandOptions) (*CommandResult, error) {
	if opts == nil {
		opts = &CommandOptions{}
	}

	// Determine if this is a dry run
	dryRun := ce.DryRun
	if opts.DryRun != nil {
		dryRun = *opts.DryRun
	}

	// Build the full command
	fullArgs := append([]string{ce.ZFSPath}, args...)
	cmdStr := strings.Join(fullArgs, " ")

	result := &CommandResult{
		Command: cmdStr,
	}

	// Handle dry run
	if dryRun {
		fmt.Printf("DRY RUN: %s\n", cmdStr)
		return result, nil
	}

	// Set up the command
	cmd := exec.CommandContext(ctx, ce.ZFSPath, args...)

	// Configure timeout
	timeout := ce.Timeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	// Create a context with timeout if needed
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd = exec.CommandContext(ctx, ce.ZFSPath, args...)
	}

	// Configure input/output
	if opts.Input != nil {
		cmd.Stdin = opts.Input
	}

	var stdoutBuf, stderrBuf bytes.Buffer

	if opts.Output != nil && opts.StreamOutput {
		// Stream output directly
		cmd.Stdout = opts.Output
		cmd.Stderr = &stderrBuf
	} else if opts.CaptureOutput {
		// Capture output in buffers
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	} else if opts.Output != nil {
		// Direct output to provided writer
		cmd.Stdout = opts.Output
		cmd.Stderr = &stderrBuf
	} else {
		// Capture output by default for error reporting
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	// Execute the command
	start := time.Now()
	err := cmd.Run()
	result.Duration = time.Since(start)

	// Get exit code
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}

	// Capture output
	result.Stdout = stdoutBuf.String()
	result.Stderr = stderrBuf.String()

	// Handle errors
	if err != nil {
		zfsErr := &snapshot.ZFSError{
			Operation: args[0], // First argument is usually the operation
			Command:   cmdStr,
			ExitCode:  result.ExitCode,
			Stderr:    result.Stderr,
			Cause:     err,
		}

		// Try to extract dataset from arguments
		if dataset := extractDatasetFromArgs(args); dataset != "" {
			zfsErr.Dataset = dataset
		}

		// Try to extract snapshot from arguments
		if snapshotName := extractSnapshotFromArgs(args); snapshotName != "" {
			zfsErr.Snapshot = snapshotName
		}

		return result, zfsErr
	}

	return result, nil
}

// List executes 'zfs list' with the given options
func (ce *CommandExecutor) List(ctx context.Context, options ListOptions) (*CommandResult, error) {
	args := []string{"list"}

	// Add type filter
	if options.Type != "" {
		args = append(args, "-t", options.Type)
	}

	// Add output format
	if len(options.Properties) > 0 {
		args = append(args, "-o", strings.Join(options.Properties, ","))
	}

	// Add other flags
	if options.Recursive {
		args = append(args, "-r")
	}
	if options.Depth > 0 {
		args = append(args, "-d", strconv.Itoa(options.Depth))
	}
	if options.Parseable {
		args = append(args, "-H") // No headers
		args = append(args, "-p") // Parseable format
	}

	// Add sorting
	if options.Sort != "" {
		args = append(args, "-s", options.Sort)
	}

	// Add dataset filter
	if options.Dataset != "" {
		args = append(args, options.Dataset)
	}

	cmdOpts := &CommandOptions{
		CaptureOutput: true,
		Timeout:       options.Timeout,
	}

	return ce.Execute(ctx, args, cmdOpts)
}

// Send executes 'zfs send' with the given options
func (ce *CommandExecutor) Send(ctx context.Context, options SendOptions) (*CommandResult, error) {
	args := []string{"send"}

	// Add flags
	if options.DryRun {
		args = append(args, "-n") // Dry run
	}
	if options.Verbose {
		args = append(args, "-v") // Verbose
	}
	if options.ParseableOutput {
		args = append(args, "-P") // Parseable output
	}
	if options.Recursive {
		args = append(args, "-R") // Recursive
	}
	if options.IncludeProperties {
		args = append(args, "-p") // Include properties
	}

	// Add incremental options
	if options.FromSnapshot != "" {
		if options.Incremental {
			args = append(args, "-i", options.FromSnapshot)
		} else {
			args = append(args, "-I", options.FromSnapshot)
		}
	}

	// Add the target snapshot
	args = append(args, options.Snapshot)

	cmdOpts := &CommandOptions{
		Output:       options.Output,
		StreamOutput: options.Output != nil,
		CaptureOutput: options.Output == nil,
		Timeout:      options.Timeout,
	}

	return ce.Execute(ctx, args, cmdOpts)
}

// Receive executes 'zfs recv' with the given options
func (ce *CommandExecutor) Receive(ctx context.Context, options ReceiveOptions) (*CommandResult, error) {
	args := []string{"recv"}

	// Add flags
	if options.DryRun {
		args = append(args, "-n") // Dry run
	}
	if options.Verbose {
		args = append(args, "-v") // Verbose
	}
	if options.Force {
		args = append(args, "-F") // Force
	}
	if options.DiscardFirstName {
		args = append(args, "-d") // Discard first name
	}
	if options.UseLastName {
		args = append(args, "-e") // Use last name
	}

	// Add the target dataset
	if options.Dataset != "" {
		args = append(args, options.Dataset)
	}

	cmdOpts := &CommandOptions{
		Input:        options.Input,
		CaptureOutput: true,
		Timeout:      options.Timeout,
	}

	return ce.Execute(ctx, args, cmdOpts)
}

// GetSendSize estimates the size of a zfs send operation
func (ce *CommandExecutor) GetSendSize(ctx context.Context, snapshot string, fromSnapshot string) (int64, error) {
	args := []string{"send", "-n", "-v", "-P"}

	if fromSnapshot != "" {
		args = append(args, "-i", fromSnapshot)
	}

	args = append(args, snapshot)

	result, err := ce.Execute(ctx, args, &CommandOptions{
		CaptureOutput: true,
		Timeout:       30 * time.Second, // Size estimation should be quick
	})

	if err != nil {
		return 0, fmt.Errorf("failed to estimate send size: %w", err)
	}

	// Parse the output to extract size
	return parseSendSizeOutput(result.Stdout)
}

// ListOptions configures the 'zfs list' command
type ListOptions struct {
	Type       string        // Type filter (filesystem, volume, snapshot, etc.)
	Properties []string      // Properties to output
	Recursive  bool          // Recursive listing
	Depth      int           // Maximum depth
	Parseable  bool          // Parseable output format
	Sort       string        // Sort by property
	Dataset    string        // Dataset to list
	Timeout    time.Duration // Command timeout
}

// SendOptions configures the 'zfs send' command
type SendOptions struct {
	Snapshot           string        // Snapshot to send
	FromSnapshot       string        // Source snapshot for incremental send
	Incremental        bool          // Use -i (incremental) vs -I (intermediate)
	DryRun             bool          // Dry run mode
	Verbose            bool          // Verbose output
	ParseableOutput    bool          // Parseable output
	Recursive          bool          // Include child filesystems
	IncludeProperties  bool          // Include properties
	Output             io.Writer     // Output destination
	Timeout            time.Duration // Command timeout
}

// ReceiveOptions configures the 'zfs recv' command
type ReceiveOptions struct {
	Dataset          string        // Target dataset
	Input            io.Reader     // Input source
	DryRun           bool          // Dry run mode
	Verbose          bool          // Verbose output
	Force            bool          // Force receive
	DiscardFirstName bool          // Discard first name component
	UseLastName      bool          // Use last name component
	Timeout          time.Duration // Command timeout
}

// extractDatasetFromArgs tries to extract a dataset name from command arguments
func extractDatasetFromArgs(args []string) string {
	// Look for dataset patterns in arguments
	for _, arg := range args {
		if strings.Contains(arg, "/") || (strings.Contains(arg, "@") && !strings.HasPrefix(arg, "-")) {
			// Extract just the dataset part (before @)
			if idx := strings.Index(arg, "@"); idx != -1 {
				return arg[:idx]
			}
			return arg
		}
	}
	return ""
}

// extractSnapshotFromArgs tries to extract a snapshot name from command arguments
func extractSnapshotFromArgs(args []string) string {
	// Look for snapshot patterns in arguments
	for _, arg := range args {
		if strings.Contains(arg, "@") && !strings.HasPrefix(arg, "-") {
			// Extract just the snapshot part (after @)
			if idx := strings.Index(arg, "@"); idx != -1 {
				return arg[idx+1:]
			}
		}
	}
	return ""
}

// parseSendSizeOutput parses the output of 'zfs send -n -v -P' to extract size
func parseSendSizeOutput(output string) (int64, error) {
	// Look for lines containing size information
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Look for the size in the last column
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			// Try to parse the last field as a size
			if size, err := strconv.ParseInt(fields[len(fields)-1], 10, 64); err == nil {
				return size, nil
			}
		}
	}

	return 0, fmt.Errorf("could not parse send size from output: %s", output)
}