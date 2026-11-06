package logger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/config"
)

func TestInit(t *testing.T) {
	tests := []struct {
		name    string
		config  *config.LogConfig
		wantErr bool
	}{
		{
			name: "default config",
			config: &config.LogConfig{
				Level:  "info",
				Format: "text",
				Output: "stdout",
			},
			wantErr: false,
		},
		{
			name: "json format",
			config: &config.LogConfig{
				Level:  "debug",
				Format: "json",
				Output: "stderr",
			},
			wantErr: false,
		},
		{
			name: "invalid level",
			config: &config.LogConfig{
				Level:  "invalid",
				Format: "text",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "invalid format",
			config: &config.LogConfig{
				Level:  "info",
				Format: "invalid",
				Output: "stdout",
			},
			wantErr: true,
		},
		{
			name: "file output without file path",
			config: &config.LogConfig{
				Level:  "info",
				Format: "text",
				Output: "file",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Init(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestFileLogging(t *testing.T) {
	// Create temporary directory for test
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	cfg := &config.LogConfig{
		Level:      "debug",
		Format:     "json",
		Output:     "file",
		File:       logFile,
		MaxSize:    1,
		MaxBackups: 2,
		MaxAge:     1,
		Compress:   false,
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}
	defer func() {
		_ = Close()
	}()

	// Test logging
	Info("test message", "key", "value")
	Debug("debug message", "debug_key", "debug_value")
	Warn("warning message", "warn_key", "warn_value")
	Error("error message", "error_key", "error_value")

	// Sync to ensure logs are flushed
	_ = Sync()

	// Check if log file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("Log file was not created: %s", logFile)
	}

	// Read log file content
	content, err := os.ReadFile(logFile) // nolint:gosec // Reading test log file that was just created
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if len(content) == 0 {
		t.Error("Log file is empty")
	}

	// Check if content contains expected messages
	contentStr := string(content)
	expectedMessages := []string{
		"test message",
		"debug message",
		"warning message",
		"error message",
	}

	for _, msg := range expectedMessages {
		if !contains(contentStr, msg) {
			t.Errorf("Log file does not contain expected message: %s", msg)
		}
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		level   string
		wantErr bool
	}{
		{"debug", false},
		{"info", false},
		{"warn", false},
		{"warning", false},
		{"error", false},
		{"invalid", true},
		{"DEBUG", false}, // Should be case insensitive
		{"INFO", false},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			_, err := parseLevel(tt.level)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseLevel(%s) error = %v, wantErr %v", tt.level, err, tt.wantErr)
			}
		})
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			containsAt(s, substr))))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
