package monitoring

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewMetricsReporter(t *testing.T) {
	// Test with valid interval
	interval := 1 * time.Second
	reporter := NewMetricsReporter(interval)

	if reporter == nil {
		t.Fatal("Reporter should not be nil")
	}

	if reporter.interval != interval {
		t.Errorf("Expected interval %v, got %v", interval, reporter.interval)
	}

	if reporter.ctx == nil {
		t.Error("Context should not be nil")
	}

	if reporter.cancel == nil {
		t.Error("Cancel function should not be nil")
	}

	// Test with zero interval (should use default)
	reporter2 := NewMetricsReporter(0)
	if reporter2.interval != 30*time.Second {
		t.Errorf("Expected default interval 30s, got %v", reporter2.interval)
	}

	// Test with negative interval (should use default)
	reporter3 := NewMetricsReporter(-5 * time.Second)
	if reporter3.interval != 30*time.Second {
		t.Errorf("Expected default interval 30s, got %v", reporter3.interval)
	}
}

func TestMetricsReporter_StartStop(t *testing.T) {
	reporter := NewMetricsReporter(100 * time.Millisecond)

	// Test that context is not cancelled initially
	select {
	case <-reporter.ctx.Done():
		t.Error("Context should not be cancelled initially")
	default:
		// OK
	}

	// Start the reporter
	reporter.Start()

	// Give it a moment to start
	time.Sleep(50 * time.Millisecond)

	// Context should still not be cancelled
	select {
	case <-reporter.ctx.Done():
		t.Error("Context should not be cancelled after start")
	default:
		// OK
	}

	// Stop the reporter
	reporter.Stop()

	// Give it a moment to stop
	time.Sleep(50 * time.Millisecond)

	// Context should now be cancelled
	select {
	case <-reporter.ctx.Done():
		// OK - context was cancelled
	default:
		t.Error("Context should be cancelled after stop")
	}
}

func TestMetricsReporter_ReportingInterval(t *testing.T) {
	// Create a new MetricsManager for this test to avoid race conditions
	oldManager := globalManager
	defer func() {
		globalManager = oldManager
	}()

	globalManager = &MetricsManager{
		global: &Metrics{
			StartTime: time.Now(),
		},
		clients:     make(map[string]*ClientMetrics),
		connections: make(map[string]*ConnectionMetrics),
	}

	// Add some activity so report() actually outputs something
	atomic.StoreInt64(&globalManager.global.TotalConnections, 1)
	atomic.StoreInt64(&globalManager.global.BytesSent, 1000)

	interval := 100 * time.Millisecond
	reporter := NewMetricsReporter(interval)

	// Start reporter
	reporter.Start()

	// Wait for a few intervals to pass
	time.Sleep(350 * time.Millisecond)

	// Stop reporter before cleanup to avoid race condition
	reporter.Stop()

	// The reporter should have run at least 3 times
	// This test mainly checks that the reporter runs without crashing
	// We can't easily test the exact number of reports without instrumenting the logger
}

func TestMetricsReporter_ReportWithNoActivity(t *testing.T) {
	// Create a new MetricsManager for this test to avoid race conditions
	oldManager := globalManager
	defer func() {
		globalManager = oldManager
	}()

	globalManager = &MetricsManager{
		global: &Metrics{
			StartTime: time.Now(),
		},
		clients:     make(map[string]*ClientMetrics),
		connections: make(map[string]*ConnectionMetrics),
	}

	reporter := NewMetricsReporter(50 * time.Millisecond)

	// Test report method directly
	// Should not output anything when there's no activity
	reporter.report() // Should return early without logging

	// This test mainly ensures no panic occurs when reporting with no activity
}

func TestMetricsReporter_ReportWithActivity(t *testing.T) {
	// Create a new MetricsManager for this test to avoid race conditions
	oldManager := globalManager
	defer func() {
		globalManager = oldManager
	}()

	globalManager = &MetricsManager{
		global: &Metrics{
			StartTime: time.Now(),
		},
		clients:     make(map[string]*ClientMetrics),
		connections: make(map[string]*ConnectionMetrics),
	}

	// Add some metrics data
	atomic.StoreInt64(&globalManager.global.TotalConnections, 10)
	atomic.StoreInt64(&globalManager.global.ActiveConnections, 5)
	atomic.StoreInt64(&globalManager.global.BytesSent, 1024000)
	atomic.StoreInt64(&globalManager.global.BytesReceived, 2048000)
	atomic.StoreInt64(&globalManager.global.ErrorCount, 2)

	reporter := NewMetricsReporter(50 * time.Millisecond)

	// Test report method directly
	reporter.report() // Should output metrics

	// This test mainly ensures no panic occurs when reporting with activity
	// The actual logging output is tested implicitly
}

func TestGlobalMetricsReporter(t *testing.T) {
	// Ensure no global reporter initially
	StopMetricsReporter()

	if globalReporter != nil {
		t.Error("Global reporter should be nil initially")
	}

	// Start global reporter
	interval := 100 * time.Millisecond
	StartMetricsReporter(interval)

	if globalReporter == nil {
		t.Error("Global reporter should exist after start")
	}

	if globalReporter.interval != interval {
		t.Errorf("Expected interval %v, got %v", interval, globalReporter.interval)
	}

	// Start again with different interval - should replace the old one
	newInterval := 200 * time.Millisecond
	StartMetricsReporter(newInterval)

	if globalReporter.interval != newInterval {
		t.Errorf("Expected new interval %v, got %v", newInterval, globalReporter.interval)
	}

	// Stop global reporter
	StopMetricsReporter()

	if globalReporter != nil {
		t.Error("Global reporter should be nil after stop")
	}
}

func TestMetricsReporter_ContextCancellation(t *testing.T) {
	reporter := NewMetricsReporter(10 * time.Millisecond)

	// Create a custom context that we can cancel
	ctx, cancel := context.WithCancel(context.Background())
	reporter.ctx = ctx
	reporter.cancel = cancel

	// Start the reporter
	reporter.Start()

	// Cancel the context after a short time
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	// Wait a bit longer
	time.Sleep(100 * time.Millisecond)

	// Context should be cancelled
	select {
	case <-reporter.ctx.Done():
		// OK - context was cancelled
	default:
		t.Error("Context should be cancelled")
	}
}

func TestMetricsReporter_ConcurrentStartStop(t *testing.T) {
	// Test concurrent start/stop operations on different reporters
	// This is more realistic than concurrent start/stop on the same reporter
	const numOps = 10
	done := make(chan bool, numOps*2)

	// Start operations on different reporters
	for i := 0; i < numOps; i++ {
		go func() {
			reporter := NewMetricsReporter(10 * time.Millisecond)
			reporter.Start()
			time.Sleep(5 * time.Millisecond) // Let it run briefly
			reporter.Stop()
			done <- true
		}()
	}

	// More start/stop operations on different reporters
	for i := 0; i < numOps; i++ {
		go func() {
			reporter := NewMetricsReporter(10 * time.Millisecond)
			reporter.Start()
			reporter.Stop() // Stop immediately
			done <- true
		}()
	}

	// Wait for all operations to complete
	for i := 0; i < numOps*2; i++ {
		select {
		case <-done:
			// Operation completed
		case <-time.After(2 * time.Second):
			t.Fatal("Concurrent operations timeout")
		}
	}

	// Should not panic or cause data races
}

func TestMetricsReporter_ReportMetricsCalculation(t *testing.T) {
	// Create a new MetricsManager for this test to avoid race conditions
	oldManager := globalManager
	defer func() {
		globalManager = oldManager
	}()

	globalManager = &MetricsManager{
		global: &Metrics{
			StartTime: time.Now().Add(-5 * time.Minute), // 5 minutes ago
		},
		clients:     make(map[string]*ClientMetrics),
		connections: make(map[string]*ConnectionMetrics),
	}

	// Set specific values for testing
	atomic.StoreInt64(&globalManager.global.TotalConnections, 100)
	atomic.StoreInt64(&globalManager.global.ActiveConnections, 50)
	atomic.StoreInt64(&globalManager.global.BytesSent, 1024*1024)       // 1MB
	atomic.StoreInt64(&globalManager.global.BytesReceived, 2*1024*1024) // 2MB
	atomic.StoreInt64(&globalManager.global.ErrorCount, 10)

	reporter := NewMetricsReporter(50 * time.Millisecond)

	// Test that report doesn't panic with these values
	reporter.report()

	// Test success rate calculation indirectly
	metrics := GetMetrics()
	successRate := metrics.SuccessRate()
	expectedRate := float64(90) // (100-10)/100 * 100

	if successRate != expectedRate {
		t.Errorf("Expected success rate %.1f, got %.1f", expectedRate, successRate)
	}

	// Test uptime calculation
	uptime := metrics.Uptime()
	if uptime < 4*time.Minute || uptime > 6*time.Minute {
		t.Errorf("Expected uptime around 5 minutes, got %v", uptime)
	}
}

func TestMetricsReporter_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func()
		expectPanic bool
	}{
		{
			name: "zero values",
			setupFunc: func() {
				globalManager = &MetricsManager{
					global: &Metrics{
						StartTime: time.Now(),
					},
					clients:     make(map[string]*ClientMetrics),
					connections: make(map[string]*ConnectionMetrics),
				}
			},
			expectPanic: false,
		},
		{
			name: "maximum values",
			setupFunc: func() {
				globalManager = &MetricsManager{
					global: &Metrics{
						StartTime: time.Now(),
					},
					clients:     make(map[string]*ClientMetrics),
					connections: make(map[string]*ConnectionMetrics),
				}
				atomic.StoreInt64(&globalManager.global.TotalConnections, 9223372036854775807) // max int64
				atomic.StoreInt64(&globalManager.global.ActiveConnections, 9223372036854775807)
				atomic.StoreInt64(&globalManager.global.BytesSent, 9223372036854775807)
				atomic.StoreInt64(&globalManager.global.BytesReceived, 9223372036854775807)
				atomic.StoreInt64(&globalManager.global.ErrorCount, 9223372036854775807)
			},
			expectPanic: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore global state
			oldManager := globalManager
			defer func() {
				globalManager = oldManager
			}()

			defer func() {
				if r := recover(); r != nil {
					if !tt.expectPanic {
						t.Errorf("Unexpected panic: %v", r)
					}
				} else if tt.expectPanic {
					t.Error("Expected panic but none occurred")
				}
			}()

			tt.setupFunc()
			reporter := NewMetricsReporter(50 * time.Millisecond)
			reporter.report()
		})
	}
}

func BenchmarkMetricsReporter_Report(b *testing.B) {
	// Save and restore global state
	oldManager := globalManager
	defer func() {
		globalManager = oldManager
	}()

	// Setup metrics with some data
	globalManager = &MetricsManager{
		global: &Metrics{
			StartTime: time.Now(),
		},
		clients:     make(map[string]*ClientMetrics),
		connections: make(map[string]*ConnectionMetrics),
	}

	atomic.StoreInt64(&globalManager.global.TotalConnections, 1000)
	atomic.StoreInt64(&globalManager.global.ActiveConnections, 500)
	atomic.StoreInt64(&globalManager.global.BytesSent, 1024*1024*100)     // 100MB
	atomic.StoreInt64(&globalManager.global.BytesReceived, 1024*1024*200) // 200MB
	atomic.StoreInt64(&globalManager.global.ErrorCount, 50)

	reporter := NewMetricsReporter(1 * time.Second)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reporter.report()
	}
}

func BenchmarkNewMetricsReporter(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		reporter := NewMetricsReporter(1 * time.Second)
		reporter.Stop() // Clean up
	}
}
