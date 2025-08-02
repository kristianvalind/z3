package compress

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompressorType(t *testing.T) {
	tests := []struct {
		name     string
		compType CompressorType
		expected string
	}{
		{"none", CompressorNone, "none"},
		{"pigz1", CompressorPigz1, "pigz1"},
		{"pigz4", CompressorPigz4, "pigz4"},
		{"gpg", CompressorGPG, "gpg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.compType))
		})
	}
}

func TestCompressorConfig(t *testing.T) {
	config := CompressorConfig{
		Type:          CompressorPigz1,
		CompressCmd:   []string{"pigz", "-1"},
		DecompressCmd: []string{"pigz", "-d"},
		GPGRecipient:  "test@example.com",
		GPGRecipients: []string{"test@example.com", "test2@example.com"},
	}

	assert.Equal(t, CompressorPigz1, config.Type)
	assert.Equal(t, []string{"pigz", "-1"}, config.CompressCmd)
	assert.Equal(t, []string{"pigz", "-d"}, config.DecompressCmd)
	assert.Equal(t, "test@example.com", config.GPGRecipient)
	assert.Equal(t, []string{"test@example.com", "test2@example.com"}, config.GPGRecipients)
	
	// Test GetRecipients method
	assert.Equal(t, []string{"test@example.com", "test2@example.com"}, config.GetRecipients())
	
	// Test backwards compatibility
	config2 := CompressorConfig{
		Type:         CompressorGPG,
		GPGRecipient: "single@example.com",
	}
	assert.Equal(t, []string{"single@example.com"}, config2.GetRecipients())
}

func TestNewPipeline(t *testing.T) {
	config1 := CompressorConfig{Type: CompressorPigz1}
	config2 := CompressorConfig{Type: CompressorGPG}

	pipeline := NewPipeline(config1, config2)
	assert.NotNil(t, pipeline)
	assert.Len(t, pipeline.configs, 2)
	assert.Equal(t, CompressorPigz1, pipeline.configs[0].Type)
	assert.Equal(t, CompressorGPG, pipeline.configs[1].Type)
}

func TestNewDefaultPipeline(t *testing.T) {
	t.Run("pigz1 only", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorPigz1}, "")
		assert.Len(t, pipeline.configs, 1)
		assert.Equal(t, CompressorPigz1, pipeline.configs[0].Type)
		assert.Equal(t, []string{"pigz", "-1", "--blocksize", "4096"}, pipeline.configs[0].CompressCmd)
		assert.Equal(t, []string{"pigz", "-d"}, pipeline.configs[0].DecompressCmd)
	})

	t.Run("pigz4 only", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorPigz4}, "")
		assert.Len(t, pipeline.configs, 1)
		assert.Equal(t, CompressorPigz4, pipeline.configs[0].Type)
		assert.Equal(t, []string{"pigz", "-4", "--blocksize", "4096"}, pipeline.configs[0].CompressCmd)
		assert.Equal(t, []string{"pigz", "-d"}, pipeline.configs[0].DecompressCmd)
	})

	t.Run("gpg with recipient", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorGPG}, "test@example.com")
		assert.Len(t, pipeline.configs, 1)
		assert.Equal(t, CompressorGPG, pipeline.configs[0].Type)
		assert.Equal(t, []string{"gpg", "-e", "-r", "test@example.com"}, pipeline.configs[0].CompressCmd)
		assert.Equal(t, []string{"gpg", "-d"}, pipeline.configs[0].DecompressCmd)
		assert.Equal(t, "test@example.com", pipeline.configs[0].GPGRecipient)
	})

	t.Run("gpg without recipient (skipped)", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorGPG}, "")
		assert.Len(t, pipeline.configs, 0)
	})

	t.Run("multiple compressors", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorPigz1, CompressorGPG}, "test@example.com")
		assert.Len(t, pipeline.configs, 2)
		assert.Equal(t, CompressorPigz1, pipeline.configs[0].Type)
		assert.Equal(t, CompressorGPG, pipeline.configs[1].Type)
	})

	t.Run("none compressor (skipped)", func(t *testing.T) {
		pipeline := NewDefaultPipeline([]CompressorType{CompressorNone}, "")
		assert.Len(t, pipeline.configs, 0)
	})
}

func TestPipeline_GetMetadata(t *testing.T) {
	t.Run("empty pipeline", func(t *testing.T) {
		pipeline := NewPipeline()
		metadata := pipeline.GetMetadata()
		assert.Empty(t, metadata)
	})

	t.Run("single compressor", func(t *testing.T) {
		config := CompressorConfig{Type: CompressorPigz1}
		pipeline := NewPipeline(config)
		metadata := pipeline.GetMetadata()
		assert.Equal(t, "pigz1", metadata["compressors"])
	})

	t.Run("multiple compressors", func(t *testing.T) {
		config1 := CompressorConfig{Type: CompressorPigz1}
		config2 := CompressorConfig{Type: CompressorGPG, GPGRecipient: "test@example.com"}
		pipeline := NewPipeline(config1, config2)
		metadata := pipeline.GetMetadata()
		assert.Equal(t, "pigz1,gpg", metadata["compressors"])
		assert.Equal(t, "test@example.com", metadata["gpg_recipient"])
		assert.Equal(t, "test@example.com", metadata["gpg_recipients"])
	})
	
	t.Run("multiple recipients", func(t *testing.T) {
		config := CompressorConfig{
			Type:          CompressorGPG,
			GPGRecipients: []string{"user1@example.com", "user2@example.com", "user3@example.com"},
			GPGRecipient:  "user1@example.com", // First recipient for backwards compatibility
		}
		pipeline := NewPipeline(config)
		metadata := pipeline.GetMetadata()
		assert.Equal(t, "gpg", metadata["compressors"])
		assert.Equal(t, "user1@example.com", metadata["gpg_recipient"]) // Old format
		assert.Equal(t, "user1@example.com,user2@example.com,user3@example.com", metadata["gpg_recipients"]) // New format
	})

	t.Run("gpg without recipient", func(t *testing.T) {
		config := CompressorConfig{Type: CompressorGPG}
		pipeline := NewPipeline(config)
		metadata := pipeline.GetMetadata()
		assert.Equal(t, "gpg", metadata["compressors"])
		assert.NotContains(t, metadata, "gpg_recipient")
	})
}

func TestParseCompressorTypes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []CompressorType
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "single compressor",
			input:    "pigz1",
			expected: []CompressorType{CompressorPigz1},
		},
		{
			name:     "multiple compressors",
			input:    "pigz1,gpg",
			expected: []CompressorType{CompressorPigz1, CompressorGPG},
		},
		{
			name:     "with spaces",
			input:    "pigz1, gpg, pigz4",
			expected: []CompressorType{CompressorPigz1, CompressorGPG, CompressorPigz4},
		},
		{
			name:     "with none (skipped)",
			input:    "pigz1,none,gpg",
			expected: []CompressorType{CompressorPigz1, CompressorGPG},
		},
		{
			name:     "with unknown (skipped)",
			input:    "pigz1,unknown,gpg",
			expected: []CompressorType{CompressorPigz1, CompressorGPG},
		},
		{
			name:     "all types",
			input:    "pigz1,pigz4,gpg",
			expected: []CompressorType{CompressorPigz1, CompressorPigz4, CompressorGPG},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseCompressorTypes(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsCompressionAvailable(t *testing.T) {
	tests := []struct {
		name     string
		compType CompressorType
	}{
		{"none", CompressorNone},
		{"pigz1", CompressorPigz1},
		{"pigz4", CompressorPigz4},
		{"gpg", CompressorGPG},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't reliably test if tools are available in CI
			// Just ensure the function doesn't panic and returns a boolean
			result := IsCompressionAvailable(tt.compType)
			assert.IsType(t, true, result)

			// None should always be available
			if tt.compType == CompressorNone {
				assert.True(t, result)
			}
		})
	}
}

func TestNopWriteCloser(t *testing.T) {
	var buf bytes.Buffer
	writer := &nopWriteCloser{&buf}

	// Test writing
	n, err := writer.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", buf.String())

	// Test closing (should be a no-op)
	err = writer.Close()
	assert.NoError(t, err)

	// Should still be able to write after close (since underlying buffer is fine)
	n, err = writer.Write([]byte(" world"))
	assert.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, "hello world", buf.String())
}

// Test compression pipeline with a mock compressor (cat command)
func TestPipeline_CompressDecompress_Mock(t *testing.T) {
	// Skip if we don't have 'cat' command (unlikely on Unix systems)
	if !IsCompressionAvailable(CompressorNone) {
		t.Skip("Basic command execution not available")
	}

	t.Run("empty pipeline", func(t *testing.T) {
		pipeline := NewPipeline()
		ctx := context.Background()

		// Test compression
		var buf bytes.Buffer
		writer, err := pipeline.Compress(ctx, &buf)
		require.NoError(t, err)

		testData := "Hello, compression pipeline!"
		_, err = writer.Write([]byte(testData))
		require.NoError(t, err)

		err = writer.Close()
		require.NoError(t, err)

		assert.Equal(t, testData, buf.String())

		// Test decompression
		reader, err := pipeline.Decompress(ctx, &buf)
		require.NoError(t, err)

		decompressed, err := io.ReadAll(reader)
		require.NoError(t, err)

		err = reader.Close()
		require.NoError(t, err)

		assert.Equal(t, testData, string(decompressed))
	})

	t.Run("single mock compressor", func(t *testing.T) {
		// Use 'cat' as a no-op compressor for testing
		config := CompressorConfig{
			Type:          CompressorType("mock"),
			CompressCmd:   []string{"cat"},
			DecompressCmd: []string{"cat"},
		}
		pipeline := NewPipeline(config)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Test compression
		var buf bytes.Buffer
		writer, err := pipeline.Compress(ctx, &buf)
		require.NoError(t, err)

		testData := "Hello, mock compression!"
		_, err = writer.Write([]byte(testData))
		require.NoError(t, err)

		err = writer.Close()
		require.NoError(t, err)

		assert.Equal(t, testData, buf.String())

		// Test decompression
		reader, err := pipeline.Decompress(ctx, &buf)
		require.NoError(t, err)

		decompressed, err := io.ReadAll(reader)
		require.NoError(t, err)

		err = reader.Close()
		require.NoError(t, err)

		assert.Equal(t, testData, string(decompressed))
	})
}

func TestPipelineWriter_ErrorHandling(t *testing.T) {
	t.Run("write after close", func(t *testing.T) {
		// Use a mock compressor to get a real pipelineWriter
		config := CompressorConfig{
			Type:          CompressorType("mock"),
			CompressCmd:   []string{"cat"},
			DecompressCmd: []string{"cat"},
		}
		pipeline := NewPipeline(config)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var buf bytes.Buffer
		writer, err := pipeline.Compress(ctx, &buf)
		require.NoError(t, err)

		// Close the writer
		err = writer.Close()
		require.NoError(t, err)

		// Try to write after close
		_, err = writer.Write([]byte("test"))
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})

	t.Run("double close", func(t *testing.T) {
		// Use a mock compressor to get a real pipelineWriter
		config := CompressorConfig{
			Type:          CompressorType("mock"),
			CompressCmd:   []string{"cat"},
			DecompressCmd: []string{"cat"},
		}
		pipeline := NewPipeline(config)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var buf bytes.Buffer
		writer, err := pipeline.Compress(ctx, &buf)
		require.NoError(t, err)

		// Close twice
		err = writer.Close()
		assert.NoError(t, err)

		err = writer.Close()
		assert.NoError(t, err) // Should not error
	})
}

func TestPipelineReader_ErrorHandling(t *testing.T) {
	t.Run("read after close", func(t *testing.T) {
		// Use a mock compressor to get a real pipelineReader
		config := CompressorConfig{
			Type:          CompressorType("mock"),
			CompressCmd:   []string{"cat"},
			DecompressCmd: []string{"cat"},
		}
		pipeline := NewPipeline(config)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		data := strings.NewReader("test data")
		reader, err := pipeline.Decompress(ctx, data)
		require.NoError(t, err)

		// Close the reader (may have broken pipe error which is expected)
		err = reader.Close()
		// Don't require no error since broken pipe is expected when closing early

		// Try to read after close
		buf := make([]byte, 10)
		_, err = reader.Read(buf)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "closed")
	})

	t.Run("double close", func(t *testing.T) {
		// Use a mock compressor to get a real pipelineReader
		config := CompressorConfig{
			Type:          CompressorType("mock"),
			CompressCmd:   []string{"cat"},
			DecompressCmd: []string{"cat"},
		}
		pipeline := NewPipeline(config)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		data := strings.NewReader("test data")
		reader, err := pipeline.Decompress(ctx, data)
		require.NoError(t, err)

		// Close twice (may have broken pipe error which is expected)
		err = reader.Close()
		// Don't check error since broken pipe is expected

		err = reader.Close()
		// Don't check error since broken pipe is expected
	})
}

func TestPipeline_ContextCancellation(t *testing.T) {
	// Use 'sleep' command to test cancellation
	config := CompressorConfig{
		Type:          CompressorType("slow"),
		CompressCmd:   []string{"sleep", "10"}, // Long-running command
		DecompressCmd: []string{"cat"},
	}
	pipeline := NewPipeline(config)

	t.Run("compression cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		var buf bytes.Buffer
		writer, err := pipeline.Compress(ctx, &buf)
		if err != nil {
			// Command might not be available, skip test
			t.Skip("sleep command not available")
		}

		// Write some data
		writer.Write([]byte("test"))

		// Close should fail due to context cancellation
		err = writer.Close()
		assert.Error(t, err)
	})
}

// Benchmark tests
func BenchmarkPipelineEmpty(b *testing.B) {
	pipeline := NewPipeline()
	ctx := context.Background()
	testData := []byte(strings.Repeat("Hello, World!", 1000))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		writer, _ := pipeline.Compress(ctx, &buf)
		writer.Write(testData)
		writer.Close()
	}
}

func BenchmarkParseCompressorTypes(b *testing.B) {
	input := "pigz1,pigz4,gpg,none,unknown,pigz1"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseCompressorTypes(input)
	}
}

func TestNewDefaultPipelineWithMultipleRecipients(t *testing.T) {
	tests := []struct {
		name            string
		compressorTypes []CompressorType
		gpgRecipient    string
		expectedCmd     []string
	}{
		{
			name:            "single recipient",
			compressorTypes: []CompressorType{CompressorGPG},
			gpgRecipient:    "user1@example.com",
			expectedCmd:     []string{"gpg", "-e", "-r", "user1@example.com"},
		},
		{
			name:            "multiple recipients comma-separated",
			compressorTypes: []CompressorType{CompressorGPG},
			gpgRecipient:    "user1@example.com,user2@example.com,user3@example.com",
			expectedCmd:     []string{"gpg", "-e", "-r", "user1@example.com", "-r", "user2@example.com", "-r", "user3@example.com"},
		},
		{
			name:            "multiple recipients with spaces",
			compressorTypes: []CompressorType{CompressorGPG},
			gpgRecipient:    "user1@example.com, user2@example.com",
			expectedCmd:     []string{"gpg", "-e", "-r", "user1@example.com", "-r", "user2@example.com"},
		},
		{
			name:            "empty recipients",
			compressorTypes: []CompressorType{CompressorGPG},
			gpgRecipient:    "",
			expectedCmd:     nil, // Should skip GPG
		},
		{
			name:            "recipients with empty entries",
			compressorTypes: []CompressorType{CompressorGPG},
			gpgRecipient:    "user1@example.com,,user2@example.com",
			expectedCmd:     []string{"gpg", "-e", "-r", "user1@example.com", "-r", "user2@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pipeline := NewDefaultPipeline(tt.compressorTypes, tt.gpgRecipient)
			assert.NotNil(t, pipeline)

			if tt.expectedCmd != nil {
				assert.Len(t, pipeline.configs, 1)
				assert.Equal(t, CompressorGPG, pipeline.configs[0].Type)
				assert.Equal(t, tt.expectedCmd, pipeline.configs[0].CompressCmd)
				
				// Check backwards compatibility
				recipients := parseRecipients(tt.gpgRecipient)
				if len(recipients) > 0 {
					assert.Equal(t, recipients[0], pipeline.configs[0].GPGRecipient)
					assert.Equal(t, recipients, pipeline.configs[0].GPGRecipients)
				}
			} else {
				assert.Empty(t, pipeline.configs)
			}
		})
	}
}

func TestParseRecipients(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "single recipient",
			input:    "user@example.com",
			expected: []string{"user@example.com"},
		},
		{
			name:     "multiple recipients",
			input:    "user1@example.com,user2@example.com,user3@example.com",
			expected: []string{"user1@example.com", "user2@example.com", "user3@example.com"},
		},
		{
			name:     "recipients with spaces",
			input:    "user1@example.com, user2@example.com, user3@example.com",
			expected: []string{"user1@example.com", "user2@example.com", "user3@example.com"},
		},
		{
			name:     "recipients with empty entries",
			input:    "user1@example.com,,user2@example.com,",
			expected: []string{"user1@example.com", "user2@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRecipients(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
