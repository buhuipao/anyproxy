// Package monitoring provides logging, metrics, and monitoring utilities for the anyproxy system.
// It includes log sampling, performance monitoring, and other observability features.
package monitoring

import (
	"sync/atomic"
)

// LogSampler log sampler for reducing high-frequency logs
type LogSampler struct {
	counter  uint64
	interval uint64 // How often to record logs
}

// NewLogSampler creates a log sampler
func NewLogSampler(interval uint64) *LogSampler {
	if interval == 0 {
		interval = 1000 // Default: record once every 1000 times
	}
	return &LogSampler{
		interval: interval,
	}
}

// ShouldLog determines whether to record logs
func (s *LogSampler) ShouldLog() bool {
	count := atomic.AddUint64(&s.counter, 1)
	return count%s.interval == 0
}

// Count gets the current count
func (s *LogSampler) Count() uint64 {
	return atomic.LoadUint64(&s.counter)
}

// Predefined global samplers
var dataSampler = NewLogSampler(1000) // Data transfer logs: record once every 1000 times

// ShouldLogData determines whether to record data transfer logs
func ShouldLogData() bool {
	return dataSampler.ShouldLog()
}
