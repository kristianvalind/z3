package compress

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// CompressorType represents the type of compression to use
type CompressorType string

const (
	// CompressorNone represents no compression
	CompressorNone CompressorType = "none"

	// CompressorPigz1 represents pigz compression level 1
	CompressorPigz1 CompressorType = "pigz1"

	// CompressorPigz4 represents pigz compression level 4
	CompressorPigz4 CompressorType = "pigz4"

	// CompressorGPG represents GPG encryption
	CompressorGPG CompressorType = "gpg"
)

// CompressorConfig holds configuration for a specific compressor
type CompressorConfig struct {
	Type          CompressorType
	CompressCmd   []string
	DecompressCmd []string
	GPGRecipient  string   // Only used for GPG - kept for backwards compatibility
	GPGRecipients []string // Multiple GPG recipients support
}

// GetRecipients returns the GPG recipients, maintaining backwards compatibility
func (c *CompressorConfig) GetRecipients() []string {
	if len(c.GPGRecipients) > 0 {
		return c.GPGRecipients
	}
	if c.GPGRecipient != "" {
		return []string{c.GPGRecipient}
	}
	return nil
}

// Pipeline represents a compression/encryption pipeline
type Pipeline struct {
	configs []CompressorConfig
	mutex   sync.RWMutex
}

// NewPipeline creates a new compression pipeline
func NewPipeline(configs ...CompressorConfig) *Pipeline {
	return &Pipeline{
		configs: configs,
	}
}

// NewDefaultPipeline creates a pipeline with default configurations
func NewDefaultPipeline(compressorTypes []CompressorType, gpgRecipient string) *Pipeline {
	// Parse comma-separated recipients for backwards compatibility
	recipients := parseRecipients(gpgRecipient)
	return NewDefaultPipelineWithRecipients(compressorTypes, recipients)
}

// NewDefaultPipelineWithRecipients creates a pipeline with multiple GPG recipients
func NewDefaultPipelineWithRecipients(compressorTypes []CompressorType, gpgRecipients []string) *Pipeline {
	var configs []CompressorConfig

	for _, compType := range compressorTypes {
		switch compType {
		case CompressorPigz1:
			configs = append(configs, CompressorConfig{
				Type:          CompressorPigz1,
				CompressCmd:   []string{"pigz", "-1", "--blocksize", "4096"},
				DecompressCmd: []string{"pigz", "-d"},
			})
		case CompressorPigz4:
			configs = append(configs, CompressorConfig{
				Type:          CompressorPigz4,
				CompressCmd:   []string{"pigz", "-4", "--blocksize", "4096"},
				DecompressCmd: []string{"pigz", "-d"},
			})
		case CompressorGPG:
			if len(gpgRecipients) == 0 {
				continue // Skip GPG if no recipients specified
			}

			// Build GPG command with multiple recipients
			compressCmd := []string{"gpg", "-e"}
			for _, recipient := range gpgRecipients {
				compressCmd = append(compressCmd, "-r", recipient)
			}

			config := CompressorConfig{
				Type:          CompressorGPG,
				CompressCmd:   compressCmd,
				DecompressCmd: []string{"gpg", "-d"},
				GPGRecipients: gpgRecipients,
			}

			// Set single recipient for backwards compatibility
			if len(gpgRecipients) > 0 {
				config.GPGRecipient = gpgRecipients[0]
			}

			configs = append(configs, config)
		case CompressorNone:
			// No compression - skip
			continue
		}
	}

	return NewPipeline(configs...)
}

// Compress creates a compression writer that wraps the provided writer
func (p *Pipeline) Compress(ctx context.Context, writer io.Writer) (io.WriteCloser, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if len(p.configs) == 0 {
		return &nopWriteCloser{writer}, nil
	}

	// Create a pipeline of compression commands
	var processes []*exec.Cmd
	var pipes []io.WriteCloser

	// Start from the end (final output) and work backwards
	currentWriter := writer

	for i := len(p.configs) - 1; i >= 0; i-- {
		config := p.configs[i]
		cmd := exec.CommandContext(ctx, config.CompressCmd[0], config.CompressCmd[1:]...)

		// Set up the command's output
		cmd.Stdout = currentWriter

		// Create stdin pipe for this command
		stdin, err := cmd.StdinPipe()
		if err != nil {
			// Clean up any previously created pipes
			for _, pipe := range pipes {
				pipe.Close()
			}
			return nil, fmt.Errorf("failed to create stdin pipe for %s: %w", config.Type, err)
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			stdin.Close()
			for _, pipe := range pipes {
				pipe.Close()
			}
			return nil, fmt.Errorf("failed to start %s command: %w", config.Type, err)
		}

		processes = append([]*exec.Cmd{cmd}, processes...)
		pipes = append([]io.WriteCloser{stdin}, pipes...)
		currentWriter = stdin
	}

	return &pipelineWriter{
		writer:    pipes[0], // First pipe is where we write input
		processes: processes,
		pipes:     pipes,
	}, nil
}

// Decompress creates a decompression reader that wraps the provided reader
func (p *Pipeline) Decompress(ctx context.Context, reader io.Reader) (io.ReadCloser, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if len(p.configs) == 0 {
		return io.NopCloser(reader), nil
	}

	// Create a pipeline of decompression commands in reverse order
	var processes []*exec.Cmd
	var pipes []io.ReadCloser

	// Start from the beginning (input) and work forwards
	currentReader := reader

	for _, config := range p.configs {
		cmd := exec.CommandContext(ctx, config.DecompressCmd[0], config.DecompressCmd[1:]...)

		// Set up the command's input
		cmd.Stdin = currentReader

		// Create stdout pipe for this command
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			// Clean up any previously created pipes
			for _, pipe := range pipes {
				pipe.Close()
			}
			return nil, fmt.Errorf("failed to create stdout pipe for %s: %w", config.Type, err)
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			stdout.Close()
			for _, pipe := range pipes {
				pipe.Close()
			}
			return nil, fmt.Errorf("failed to start %s decompression command: %w", config.Type, err)
		}

		processes = append(processes, cmd)
		pipes = append(pipes, stdout)
		currentReader = stdout
	}

	return &pipelineReader{
		reader:    pipes[len(pipes)-1], // Last pipe is where we read output
		processes: processes,
		pipes:     pipes,
	}, nil
}

// GetMetadata returns metadata about the compression pipeline
func (p *Pipeline) GetMetadata() map[string]string {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	metadata := make(map[string]string)

	var compressors []string
	for _, config := range p.configs {
		compressors = append(compressors, string(config.Type))
		if config.Type == CompressorGPG {
			recipients := config.GetRecipients()
			if len(recipients) > 0 {
				// Store both formats for backwards compatibility
				metadata["gpg_recipient"] = recipients[0]                  // Old format - first recipient only
				metadata["gpg_recipients"] = strings.Join(recipients, ",") // New format - all recipients
			}
		}
	}

	if len(compressors) > 0 {
		metadata["compressors"] = strings.Join(compressors, ",")
	}

	return metadata
}

// pipelineWriter handles writing through a compression pipeline
type pipelineWriter struct {
	writer    io.WriteCloser
	processes []*exec.Cmd
	pipes     []io.WriteCloser
	closed    bool
	mutex     sync.Mutex
}

func (pw *pipelineWriter) Write(p []byte) (n int, err error) {
	pw.mutex.Lock()
	defer pw.mutex.Unlock()

	if pw.closed {
		return 0, fmt.Errorf("pipeline writer is closed")
	}

	return pw.writer.Write(p)
}

func (pw *pipelineWriter) Close() error {
	pw.mutex.Lock()
	defer pw.mutex.Unlock()

	if pw.closed {
		return nil
	}

	pw.closed = true

	// Close the input pipe first
	if pw.writer != nil {
		pw.writer.Close()
	}

	// Wait for all processes to complete
	var lastErr error
	for i, process := range pw.processes {
		if err := process.Wait(); err != nil {
			lastErr = fmt.Errorf("compression process %d failed: %w", i, err)
		}
	}

	// Close remaining pipes
	for _, pipe := range pw.pipes[1:] { // Skip first pipe (already closed)
		pipe.Close()
	}

	return lastErr
}

// pipelineReader handles reading through a decompression pipeline
type pipelineReader struct {
	reader    io.ReadCloser
	processes []*exec.Cmd
	pipes     []io.ReadCloser
	closed    bool
	mutex     sync.Mutex
}

func (pr *pipelineReader) Read(p []byte) (n int, err error) {
	pr.mutex.Lock()
	defer pr.mutex.Unlock()

	if pr.closed {
		return 0, fmt.Errorf("pipeline reader is closed")
	}

	return pr.reader.Read(p)
}

func (pr *pipelineReader) Close() error {
	pr.mutex.Lock()
	defer pr.mutex.Unlock()

	if pr.closed {
		return nil
	}

	pr.closed = true

	// Close all pipes
	for _, pipe := range pr.pipes {
		pipe.Close()
	}

	// Wait for all processes to complete
	var lastErr error
	for i, process := range pr.processes {
		if err := process.Wait(); err != nil {
			lastErr = fmt.Errorf("decompression process %d failed: %w", i, err)
		}
	}

	return lastErr
}

// nopWriteCloser wraps a writer to implement WriteCloser
type nopWriteCloser struct {
	io.Writer
}

func (nwc *nopWriteCloser) Close() error {
	return nil
}

// ParseCompressorTypes parses a comma-separated string of compressor types
func ParseCompressorTypes(compressors string) []CompressorType {
	if compressors == "" {
		return nil
	}

	var types []CompressorType
	for _, comp := range strings.Split(compressors, ",") {
		comp = strings.TrimSpace(comp)
		switch comp {
		case "pigz1":
			types = append(types, CompressorPigz1)
		case "pigz4":
			types = append(types, CompressorPigz4)
		case "gpg":
			types = append(types, CompressorGPG)
		case "none", "":
			// Skip empty or none
			continue
		default:
			// Unknown compressor type - skip with a warning
			continue
		}
	}

	return types
}

// IsCompressionAvailable checks if the required compression tools are available
func IsCompressionAvailable(compType CompressorType) bool {
	switch compType {
	case CompressorPigz1, CompressorPigz4:
		_, err := exec.LookPath("pigz")
		return err == nil
	case CompressorGPG:
		_, err := exec.LookPath("gpg")
		return err == nil
	case CompressorNone:
		return true
	default:
		return false
	}
}

// parseRecipients parses a comma-separated string of GPG recipients
func parseRecipients(recipients string) []string {
	if recipients == "" {
		return nil
	}

	var result []string
	for _, r := range strings.Split(recipients, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			result = append(result, r)
		}
	}

	return result
}
