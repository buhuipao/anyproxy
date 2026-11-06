package monitoring

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// MetricsReporter metrics reporter
type MetricsReporter struct {
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewMetricsReporter creates metrics reporter
func NewMetricsReporter(interval time.Duration) *MetricsReporter {
	if interval <= 0 {
		interval = 30 * time.Second // Default 30 seconds
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MetricsReporter{
		interval: interval,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Start starts periodic reporting
func (r *MetricsReporter) Start() {
	r.wg.Add(1)
	go r.run()
}

// Stop stops reporting and waits for the goroutine to finish
func (r *MetricsReporter) Stop() {
	r.cancel()
	r.wg.Wait()
}

// run runs reporting loop
func (r *MetricsReporter) run() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			r.report()
		}
	}
}

// report generates and outputs report
func (r *MetricsReporter) report() {
	metrics := GetMetrics()

	// Counters are updated atomically by the data path, so read them atomically.
	active := atomic.LoadInt64(&metrics.ActiveConnections)
	total := atomic.LoadInt64(&metrics.TotalConnections)
	sent := atomic.LoadInt64(&metrics.BytesSent)
	recv := atomic.LoadInt64(&metrics.BytesReceived)

	// Only output when there's activity
	if total == 0 && sent == 0 && recv == 0 {
		return
	}

	// Concise one-line output
	logger.Info("Performance",
		"uptime", fmt.Sprintf("%dm", int(metrics.Uptime().Minutes())),
		"conns", fmt.Sprintf("%d/%d", active, total),
		"success", fmt.Sprintf("%.0f%%", metrics.SuccessRate()),
		"sent", humanizeBytes(sent),
		"recv", humanizeBytes(recv),
		"errors", atomic.LoadInt64(&metrics.ErrorCount),
	)
}

// Global reporter instance
var globalReporter *MetricsReporter

// StartMetricsReporter starts global metrics reporter
func StartMetricsReporter(interval time.Duration) {
	if globalReporter != nil {
		globalReporter.Stop()
	}
	globalReporter = NewMetricsReporter(interval)
	globalReporter.Start()
}

// StopMetricsReporter stops global metrics reporter
func StopMetricsReporter() {
	if globalReporter != nil {
		globalReporter.Stop()
		globalReporter = nil
	}
}
