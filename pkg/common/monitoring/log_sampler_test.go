package monitoring

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestLogSampler 测试日志采样器
func TestLogSampler(t *testing.T) {
	sampler := NewLogSampler(100)

	// 测试基本采样
	logCount := 0
	for i := 0; i < 1000; i++ {
		if sampler.ShouldLog() {
			logCount++
		}
	}

	// 应该记录约10次（1000/100）
	if logCount < 8 || logCount > 12 {
		t.Errorf("Expected ~10 logs, got %d", logCount)
	}

	// 验证计数器
	if sampler.Count() != 1000 {
		t.Errorf("Expected count 1000, got %d", sampler.Count())
	}
}

// TestRateLimiter 测试速率限制器
func TestRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(100 * time.Millisecond)

	// 第一次应该允许
	if !limiter.ShouldLog() {
		t.Error("First log should be allowed")
	}

	// 立即再次调用应该被拒绝
	if limiter.ShouldLog() {
		t.Error("Immediate second log should be denied")
	}

	// 等待超过间隔后应该允许
	time.Sleep(110 * time.Millisecond)
	if !limiter.ShouldLog() {
		t.Error("Log after interval should be allowed")
	}
}

// TestMetrics 测试指标收集
func TestMetrics(t *testing.T) {
	// 重置指标
	metrics := GetMetrics()
	atomic.StoreInt64(&metrics.ActiveConnections, 0)
	atomic.StoreInt64(&metrics.TotalConnections, 0)
	atomic.StoreInt64(&metrics.BytesSent, 0)
	atomic.StoreInt64(&metrics.BytesReceived, 0)
	atomic.StoreInt64(&metrics.ErrorCount, 0)

	// 测试连接指标
	IncrementActiveConnections()
	if atomic.LoadInt64(&metrics.ActiveConnections) != 1 {
		t.Errorf("Expected 1 active connection, got %d", atomic.LoadInt64(&metrics.ActiveConnections))
	}

	DecrementActiveConnections()
	if atomic.LoadInt64(&metrics.ActiveConnections) != 0 {
		t.Errorf("Expected 0 active connections, got %d", atomic.LoadInt64(&metrics.ActiveConnections))
	}

	// 测试数据传输指标
	AddBytesSent(1000)
	AddBytesReceived(2000)
	if atomic.LoadInt64(&metrics.BytesSent) != 1000 {
		t.Errorf("Expected 1000 bytes sent, got %d", atomic.LoadInt64(&metrics.BytesSent))
	}
	if atomic.LoadInt64(&metrics.BytesReceived) != 2000 {
		t.Errorf("Expected 2000 bytes received, got %d", atomic.LoadInt64(&metrics.BytesReceived))
	}

	// 测试错误指标
	IncrementErrors()
	if atomic.LoadInt64(&metrics.ErrorCount) != 1 {
		t.Errorf("Expected 1 error, got %d", atomic.LoadInt64(&metrics.ErrorCount))
	}
}

// BenchmarkLogSampler 基准测试日志采样器
func BenchmarkLogSampler(b *testing.B) {
	sampler := NewLogSampler(1000)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sampler.ShouldLog()
		}
	})
}

// BenchmarkMetrics 基准测试指标收集
func BenchmarkMetrics(b *testing.B) {
	b.Run("IncrementActiveConnections", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			IncrementActiveConnections()
		}
	})

	b.Run("AddBytesSent", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			AddBytesSent(1024)
		}
	})

	b.Run("IncrementErrors", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			IncrementErrors()
		}
	})
}
