// Package logger provides high-performance structured logging functionality for AnyProxy.
// It uses uber-go/zap for optimal performance and supports multiple output formats
// (text, JSON) and destinations (stdout, stderr, file) with log rotation capabilities.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/buhuipao/anyproxy/pkg/config"
)

var (
	// defaultLogger is the global zap sugared logger
	defaultLogger *zap.SugaredLogger
	// rawLogger is the underlying zap.Logger for advanced use
	rawLogger *zap.Logger
	// logCloser keeps track of the writer that needs to be closed
	logCloser *lumberjack.Logger
	// mu protects logger initialization
	mu sync.RWMutex
)

// Init initializes the global logger based on configuration
func Init(cfg *config.LogConfig) error {
	mu.Lock()
	defer mu.Unlock()

	// Set default values if not provided
	if cfg.Level == "" {
		cfg.Level = "info"
	}
	if cfg.Format == "" {
		cfg.Format = "text"
	}
	if cfg.Output == "" {
		cfg.Output = "stdout"
	}

	// Parse log level
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return fmt.Errorf("invalid log level %s: %v", cfg.Level, err)
	}

	// Create output writer
	var writeSyncer zapcore.WriteSyncer
	switch strings.ToLower(cfg.Output) {
	case "stdout":
		writeSyncer = zapcore.AddSync(os.Stdout)
	case "stderr":
		writeSyncer = zapcore.AddSync(os.Stderr)
	case "file":
		if cfg.File == "" {
			return fmt.Errorf("log file path is required when output is 'file'")
		}

		// Create directory if it doesn't exist
		dir := filepath.Dir(cfg.File)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create log directory %s: %v", dir, err)
		}

		// Set default rotation values if not provided
		maxSize := cfg.MaxSize
		if maxSize == 0 {
			maxSize = 100 // 100MB default
		}
		maxBackups := cfg.MaxBackups
		if maxBackups == 0 {
			maxBackups = 3 // Keep 3 old files by default
		}
		maxAge := cfg.MaxAge
		if maxAge == 0 {
			maxAge = 28 // Keep files for 28 days by default
		}

		// Use lumberjack for log rotation
		lj := &lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   cfg.Compress,
		}
		writeSyncer = zapcore.AddSync(lj)
		logCloser = lj
	default:
		// Treat as file path
		if err := os.MkdirAll(filepath.Dir(cfg.Output), 0750); err != nil {
			return fmt.Errorf("failed to create log directory: %v", err)
		}

		file, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("failed to open log file %s: %v", cfg.Output, err)
		}
		writeSyncer = zapcore.AddSync(file)
	}

	// Create encoder config
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "", // Disable caller for performance (can enable if needed)
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// Create encoder based on format
	var encoder zapcore.Encoder
	switch strings.ToLower(cfg.Format) {
	case "json":
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	case "text":
		// Use console encoder for text format (human-readable)
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		encoderConfig.ConsoleSeparator = " "
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	default:
		return fmt.Errorf("unsupported log format: %s", cfg.Format)
	}

	// Create core
	core := zapcore.NewCore(encoder, writeSyncer, level)

	// Create logger with options
	rawLogger = zap.New(core,
		zap.AddStacktrace(zapcore.ErrorLevel), // Add stacktrace for errors
	)
	defaultLogger = rawLogger.Sugar()

	return nil
}

// Close closes the log writer to ensure all buffered logs are flushed.
// Should be called before program exit.
func Close() error {
	mu.Lock()
	defer mu.Unlock()

	// Sync the logger to flush any buffered logs
	if rawLogger != nil {
		_ = rawLogger.Sync() // Ignore sync errors (common on stdout/stderr)
	}

	if logCloser != nil {
		err := logCloser.Close()
		logCloser = nil
		return err
	}
	return nil
}

// parseLevel converts string level to zapcore.Level
func parseLevel(level string) (zapcore.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info":
		return zapcore.InfoLevel, nil
	case "warn", "warning":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	case "fatal":
		return zapcore.FatalLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("unknown level: %s", level)
	}
}

// getLogger returns the default sugared logger, creating a default one if needed
func getLogger() *zap.SugaredLogger {
	mu.RLock()
	l := defaultLogger
	mu.RUnlock()

	if l == nil {
		// Return a default logger if not initialized
		// This is a fallback for code that logs before Init() is called
		logger, _ := zap.NewProduction()
		return logger.Sugar()
	}
	return l
}

// GetLogger returns the underlying zap.Logger for advanced use
// Note: This returns the raw logger, not the sugared version
func GetLogger() *zap.Logger {
	mu.RLock()
	l := rawLogger
	mu.RUnlock()

	if l == nil {
		logger, _ := zap.NewProduction()
		return logger
	}
	return l
}

// GetSugaredLogger returns the sugared logger for key-value style logging
func GetSugaredLogger() *zap.SugaredLogger {
	return getLogger()
}

// Debug logs a debug message with key-value pairs
// Example: logger.Debug("message", "key1", value1, "key2", value2)
func Debug(msg string, args ...any) {
	getLogger().Debugw(msg, args...)
}

// Info logs an info message with key-value pairs
// Example: logger.Info("message", "key1", value1, "key2", value2)
func Info(msg string, args ...any) {
	getLogger().Infow(msg, args...)
}

// Warn logs a warning message with key-value pairs
// Example: logger.Warn("message", "key1", value1, "key2", value2)
func Warn(msg string, args ...any) {
	getLogger().Warnw(msg, args...)
}

// Error logs an error message with key-value pairs
// Example: logger.Error("message", "key1", value1, "key2", value2)
func Error(msg string, args ...any) {
	getLogger().Errorw(msg, args...)
}

// Fatal logs a fatal message with key-value pairs and then calls os.Exit(1)
// Example: logger.Fatal("message", "key1", value1, "key2", value2)
func Fatal(msg string, args ...any) {
	getLogger().Fatalw(msg, args...)
}

// With returns a logger with the given key-value pairs attached
// Returns a new SugaredLogger that includes the specified fields
func With(args ...any) *zap.SugaredLogger {
	return getLogger().With(args...)
}

// Named returns a logger with the given name as a prefix
func Named(name string) *zap.SugaredLogger {
	return getLogger().Named(name)
}

// Sync flushes any buffered log entries
// Applications should call Sync before exiting
func Sync() error {
	if rawLogger != nil {
		return rawLogger.Sync()
	}
	return nil
}

// =============================================================================
// Additional helper functions for common logging patterns
// =============================================================================

// DebugEnabled returns true if debug level is enabled
func DebugEnabled() bool {
	logger := GetLogger()
	return logger.Core().Enabled(zapcore.DebugLevel)
}

