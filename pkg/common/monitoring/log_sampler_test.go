package monitoring

import (
	"testing"
)

// TestLogSampler tests the log sampler
func TestLogSampler(t *testing.T) {
	sampler := NewLogSampler(100)

	logCount := 0
	for i := 0; i < 1000; i++ {
		if sampler.ShouldLog() {
			logCount++
		}
	}

	// Should log ~10 times (1000/100)
	if logCount < 8 || logCount > 12 {
		t.Errorf("Expected ~10 logs, got %d", logCount)
	}

	if sampler.Count() != 1000 {
		t.Errorf("Expected count 1000, got %d", sampler.Count())
	}
}

// BenchmarkLogSampler benchmarks the log sampler
func BenchmarkLogSampler(b *testing.B) {
	sampler := NewLogSampler(1000)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sampler.ShouldLog()
		}
	})
}
