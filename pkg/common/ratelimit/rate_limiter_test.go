package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// MockStorage implements Storage interface for testing
type MockStorage struct {
	mu            sync.RWMutex
	config        *Config
	data          map[string]*Data
	saveConfigErr error
	loadConfigErr error
	saveDataErr   error
	loadDataErr   error
	cleanupErr    error
}

func NewMockStorage() *MockStorage {
	return &MockStorage{
		data: make(map[string]*Data),
	}
}

func (ms *MockStorage) SaveRateLimitConfig(config *Config) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.saveConfigErr != nil {
		return ms.saveConfigErr
	}
	ms.config = config
	return nil
}

func (ms *MockStorage) LoadRateLimitConfig() (*Config, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if ms.loadConfigErr != nil {
		return nil, ms.loadConfigErr
	}
	if ms.config == nil {
		return &Config{Rules: make([]*Rule, 0)}, nil
	}
	return ms.config, nil
}

func (ms *MockStorage) SaveRateLimitData(data *Data) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.saveDataErr != nil {
		return ms.saveDataErr
	}
	ms.data[data.Identifier] = data
	return nil
}

func (ms *MockStorage) LoadRateLimitData(identifier string) (*Data, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if ms.loadDataErr != nil {
		return nil, ms.loadDataErr
	}
	data, exists := ms.data[identifier]
	if !exists {
		return nil, fmt.Errorf("data not found")
	}
	return data, nil
}

func (ms *MockStorage) CleanupExpiredRateLimitData() error {
	if ms.cleanupErr != nil {
		return ms.cleanupErr
	}
	return nil
}

func (ms *MockStorage) SetErrors(saveConfig, loadConfig, saveData, loadData, cleanup error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.saveConfigErr = saveConfig
	ms.loadConfigErr = loadConfig
	ms.saveDataErr = saveData
	ms.loadDataErr = loadData
	ms.cleanupErr = cleanup
}

func TestNewRateLimiter(t *testing.T) {
	tests := []struct {
		name    string
		storage Storage
	}{
		{
			name:    "with storage",
			storage: NewMockStorage(),
		},
		{
			name:    "without storage",
			storage: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := NewRateLimiter(tt.storage)

			if rl == nil {
				t.Fatal("NewRateLimiter should return non-nil rate limiter")
			}

			if rl.storage != tt.storage {
				t.Errorf("Expected storage %v, got %v", tt.storage, rl.storage)
			}

			if rl.limiters == nil {
				t.Error("Expected limiters map to be initialized")
			}

			if rl.config == nil {
				t.Error("Expected config to be initialized")
			}

			if len(rl.config.Rules) != 0 {
				t.Errorf("Expected empty rules initially, got %d", len(rl.config.Rules))
			}
		})
	}
}

func TestNewRateLimiterWithStorageError(t *testing.T) {
	storage := NewMockStorage()
	storage.SetErrors(nil, fmt.Errorf("load config error"), nil, nil, nil)

	rl := NewRateLimiter(storage)

	if rl == nil {
		t.Fatal("NewRateLimiter should return non-nil rate limiter even with storage error")
	}

	if len(rl.config.Rules) != 0 {
		t.Errorf("Expected empty rules when config load fails, got %d", len(rl.config.Rules))
	}
}

func TestRateLimiter_UpdateConfig(t *testing.T) {
	storage := NewMockStorage()
	rl := NewRateLimiter(storage)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "test1",
				Type:           "client",
				Identifier:     "client1",
				Enabled:        true,
				BandwidthLimit: 1000,
				BurstLimit:     2000,
				Action:         "block",
				Priority:       1,
			},
		},
	}

	err := rl.UpdateConfig(config)
	if err != nil {
		t.Errorf("UpdateConfig should not return error, got %v", err)
	}

	if rl.config != config {
		t.Error("Config should be updated")
	}

	if len(rl.limiters) != 0 {
		t.Error("Limiters should be cleared after config update")
	}
}

func TestRateLimiter_UpdateConfigWithStorageError(t *testing.T) {
	storage := NewMockStorage()
	storage.SetErrors(fmt.Errorf("save config error"), nil, nil, nil, nil)
	rl := NewRateLimiter(storage)

	config := &Config{Rules: []*Rule{}}

	err := rl.UpdateConfig(config)
	if err == nil {
		t.Error("UpdateConfig should return error when storage fails")
	}
}

func TestRateLimiter_GetConfig(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := rl.GetConfig()
	if config == nil {
		t.Error("GetConfig should return non-nil config")
	}

	if config != rl.config {
		t.Error("GetConfig should return the same config instance")
	}
}

func TestRateLimiter_CheckRateLimit_NoRules(t *testing.T) {
	rl := NewRateLimiter(nil)

	result := rl.CheckRateLimit("client1", "example.com", 1000, 1)

	if !result.Allowed {
		t.Error("Request should be allowed when no rules are configured")
	}

	if result.Action != "allow" {
		t.Errorf("Expected action 'allow', got '%s'", result.Action)
	}

	if result.Reason != "within limits" {
		t.Errorf("Expected reason 'within limits', got '%s'", result.Reason)
	}
}

func TestRateLimiter_CheckRateLimit_ClientLimit(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "client_rule",
				Type:           "client",
				Identifier:     "client1",
				Enabled:        true,
				BandwidthLimit: 1000, // 1000 bytes per second
				BurstLimit:     1000, // 1000 bytes burst
				Action:         "block",
			},
		},
	}
	rl.UpdateConfig(config)

	// First request should be allowed (within burst limit)
	result := rl.CheckRateLimit("client1", "example.com", 500, 1)
	if !result.Allowed {
		t.Error("First request should be allowed within burst limit")
	}

	// Second request that exceeds burst limit should be blocked
	result = rl.CheckRateLimit("client1", "example.com", 600, 1)
	if result.Allowed {
		t.Error("Request exceeding burst limit should be blocked")
	}

	if result.Action != "block" {
		t.Errorf("Expected action 'block', got '%s'", result.Action)
	}

	if result.LimitType != "client" {
		t.Errorf("Expected limit type 'client', got '%s'", result.LimitType)
	}
}

func TestRateLimiter_CheckRateLimit_DomainLimit(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:            "domain_rule",
				Type:          "domain",
				Identifier:    "example.com",
				Enabled:       true,
				RequestLimit:  2,
				RequestWindow: 1 * time.Second,
				Action:        "throttle",
			},
		},
	}
	rl.UpdateConfig(config)

	// First two requests should be allowed
	result := rl.CheckRateLimit("client1", "example.com", 100, 1)
	if !result.Allowed {
		t.Error("First request should be allowed")
	}

	result = rl.CheckRateLimit("client1", "example.com", 100, 1)
	if !result.Allowed {
		t.Error("Second request should be allowed")
	}

	// Third request should be blocked
	result = rl.CheckRateLimit("client1", "example.com", 100, 1)
	if result.Allowed {
		t.Error("Third request should be blocked by rate limit")
	}

	if result.Action != "throttle" {
		t.Errorf("Expected action 'throttle', got '%s'", result.Action)
	}

	if result.LimitType != "domain" {
		t.Errorf("Expected limit type 'domain', got '%s'", result.LimitType)
	}
}

func TestRateLimiter_CheckRateLimit_GlobalLimit(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:              "global_rule",
				Type:            "global",
				Identifier:      "*",
				Enabled:         true,
				ConcurrentLimit: 2,
				Action:          "block",
			},
		},
	}
	rl.UpdateConfig(config)

	// Request with 2 concurrent connections should be allowed
	result := rl.CheckRateLimit("client1", "example.com", 100, 2)
	if !result.Allowed {
		t.Error("Request with 2 concurrent connections should be allowed")
	}

	// Request with 3 concurrent connections should be blocked
	result = rl.CheckRateLimit("client1", "example.com", 100, 3)
	if result.Allowed {
		t.Error("Request with 3 concurrent connections should be blocked")
	}

	if result.LimitType != "global" {
		t.Errorf("Expected limit type 'global', got '%s'", result.LimitType)
	}
}

func TestRateLimiter_CheckRateLimit_WildcardIdentifier(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "wildcard_client",
				Type:           "client",
				Identifier:     "*", // Wildcard matches all clients
				Enabled:        true,
				BandwidthLimit: 500,
				BurstLimit:     500,
				Action:         "block",
			},
		},
	}
	rl.UpdateConfig(config)

	// Any client should be subject to the wildcard rule
	result := rl.CheckRateLimit("any_client", "example.com", 600, 1)
	if result.Allowed {
		t.Error("Request should be blocked by wildcard rule")
	}
}

func TestRateLimiter_CheckRateLimit_DisabledRule(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "disabled_rule",
				Type:           "client",
				Identifier:     "client1",
				Enabled:        false, // Rule is disabled
				BandwidthLimit: 1,
				BurstLimit:     1,
				Action:         "block",
			},
		},
	}
	rl.UpdateConfig(config)

	// Large request should be allowed because rule is disabled
	result := rl.CheckRateLimit("client1", "example.com", 10000, 1)
	if !result.Allowed {
		t.Error("Request should be allowed when rule is disabled")
	}
}

func TestTokenBucketLimiter_BandwidthLimit(t *testing.T) {
	rule := &Rule{
		BandwidthLimit: 1000, // 1000 bytes per second
		BurstLimit:     1000, // 1000 bytes burst (same as bandwidth for easier testing)
		Action:         "block",
	}

	limiter := &TokenBucketLimiter{
		rule:       rule,
		tokens:     float64(rule.BurstLimit), // Start with full tokens
		lastRefill: time.Now(),
	}

	// Request within burst limit should be allowed
	result := limiter.checkLimit(500, 1)
	if !result.Allowed {
		t.Error("Request within burst limit should be allowed")
	}

	// Now we have 500 tokens remaining
	// Request that exceeds remaining tokens should be blocked
	result = limiter.checkLimit(600, 1)
	if result.Allowed {
		t.Error("Request exceeding remaining tokens should be blocked")
	}

	if result.Reason != "bandwidth limit exceeded" {
		t.Errorf("Expected reason 'bandwidth limit exceeded', got '%s'", result.Reason)
	}

	// RetryAfter should be positive when bandwidth limit is exceeded
	if result.RetryAfter <= 0 {
		t.Errorf("RetryAfter should be positive when bandwidth limit is exceeded, got %v", result.RetryAfter)
	}
}

func TestTokenBucketLimiter_TokenRefill(t *testing.T) {
	rule := &Rule{
		BandwidthLimit: 1000, // 1000 bytes per second
		BurstLimit:     1000,
		Action:         "block",
	}

	limiter := &TokenBucketLimiter{
		rule:       rule,
		tokens:     100,                              // Start with low tokens
		lastRefill: time.Now().Add(-1 * time.Second), // 1 second ago
	}

	// After 1 second, tokens should be refilled
	result := limiter.checkLimit(1000, 1)
	if !result.Allowed {
		t.Error("Request should be allowed after token refill")
	}

	// Check that tokens were properly refilled
	if limiter.tokens < 0 {
		t.Error("Tokens should not be negative after refill")
	}
}

func TestTokenBucketLimiter_RequestRateLimit(t *testing.T) {
	rule := &Rule{
		RequestLimit:  2,
		RequestWindow: 1 * time.Second,
		Action:        "throttle",
	}

	limiter := &TokenBucketLimiter{
		rule:        rule,
		windowStart: time.Now(),
	}

	// First two requests should be allowed
	result := limiter.checkLimit(100, 1)
	if !result.Allowed {
		t.Error("First request should be allowed")
	}

	result = limiter.checkLimit(100, 1)
	if !result.Allowed {
		t.Error("Second request should be allowed")
	}

	// Third request should be blocked
	result = limiter.checkLimit(100, 1)
	if result.Allowed {
		t.Error("Third request should be blocked by rate limit")
	}

	if result.Reason != "request rate limit exceeded" {
		t.Errorf("Expected reason 'request rate limit exceeded', got '%s'", result.Reason)
	}
}

func TestTokenBucketLimiter_RequestRateWindowReset(t *testing.T) {
	rule := &Rule{
		RequestLimit:  1,
		RequestWindow: 100 * time.Millisecond,
		Action:        "block",
	}

	limiter := &TokenBucketLimiter{
		rule:        rule,
		windowStart: time.Now().Add(-200 * time.Millisecond), // Old window
	}

	// Request should be allowed because window has passed
	result := limiter.checkLimit(100, 1)
	if !result.Allowed {
		t.Error("Request should be allowed after window reset")
	}

	// Check that request count was reset
	if limiter.requestCount != 1 {
		t.Errorf("Request count should be 1 after reset, got %d", limiter.requestCount)
	}
}

func TestTokenBucketLimiter_ConcurrentLimit(t *testing.T) {
	rule := &Rule{
		ConcurrentLimit: 5,
		Action:          "block",
	}

	limiter := &TokenBucketLimiter{
		rule: rule,
	}

	// Request with 5 concurrent connections should be allowed
	result := limiter.checkLimit(100, 5)
	if !result.Allowed {
		t.Error("Request with 5 concurrent connections should be allowed")
	}

	// Request with 6 concurrent connections should be blocked
	result = limiter.checkLimit(100, 6)
	if result.Allowed {
		t.Error("Request with 6 concurrent connections should be blocked")
	}

	if result.Reason != "concurrent connection limit exceeded" {
		t.Errorf("Expected reason 'concurrent connection limit exceeded', got '%s'", result.Reason)
	}
}

func TestTokenBucketLimiter_DailyLimit(t *testing.T) {
	now := time.Now()
	rule := &Rule{
		DailyLimit: 1000,
		Action:     "block",
	}

	limiter := &TokenBucketLimiter{
		rule:       rule,
		dailyBytes: 800,
		dayStart:   time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
	}

	// Request within daily limit should be allowed
	result := limiter.checkLimit(100, 1)
	if !result.Allowed {
		t.Error("Request within daily limit should be allowed")
	}

	// Request exceeding daily limit should be blocked
	result = limiter.checkLimit(200, 1)
	if result.Allowed {
		t.Error("Request exceeding daily limit should be blocked")
	}

	if result.Reason != "daily limit exceeded" {
		t.Errorf("Expected reason 'daily limit exceeded', got '%s'", result.Reason)
	}
}

func TestTokenBucketLimiter_DailyLimitReset(t *testing.T) {
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	rule := &Rule{
		DailyLimit: 1000,
		Action:     "block",
	}

	limiter := &TokenBucketLimiter{
		rule:       rule,
		dailyBytes: 999, // Almost at limit
		dayStart:   time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, yesterday.Location()),
	}

	// Request should be allowed because daily counter should reset
	result := limiter.checkLimit(500, 1)
	if !result.Allowed {
		t.Error("Request should be allowed after daily reset")
	}

	// Check that daily counter was reset
	if limiter.dailyBytes != 500 {
		t.Errorf("Daily bytes should be 500 after reset, got %d", limiter.dailyBytes)
	}
}

func TestTokenBucketLimiter_MonthlyLimit(t *testing.T) {
	now := time.Now()
	rule := &Rule{
		MonthlyLimit: 10000,
		Action:       "block",
	}

	limiter := &TokenBucketLimiter{
		rule:         rule,
		monthlyBytes: 9500,
		monthStart:   time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	}

	// Request within monthly limit should be allowed
	result := limiter.checkLimit(300, 1)
	if !result.Allowed {
		t.Error("Request within monthly limit should be allowed")
	}

	// Request exceeding monthly limit should be blocked
	result = limiter.checkLimit(300, 1)
	if result.Allowed {
		t.Error("Request exceeding monthly limit should be blocked")
	}

	if result.Reason != "monthly limit exceeded" {
		t.Errorf("Expected reason 'monthly limit exceeded', got '%s'", result.Reason)
	}
}

func TestTokenBucketLimiter_MonthlyLimitReset(t *testing.T) {
	now := time.Now()
	lastMonth := now.AddDate(0, -1, 0)
	rule := &Rule{
		MonthlyLimit: 10000,
		Action:       "block",
	}

	limiter := &TokenBucketLimiter{
		rule:         rule,
		monthlyBytes: 9999, // Almost at limit
		monthStart:   time.Date(lastMonth.Year(), lastMonth.Month(), 1, 0, 0, 0, 0, lastMonth.Location()),
	}

	// Request should be allowed because monthly counter should reset
	result := limiter.checkLimit(5000, 1)
	if !result.Allowed {
		t.Error("Request should be allowed after monthly reset")
	}

	// Check that monthly counter was reset
	if limiter.monthlyBytes != 5000 {
		t.Errorf("Monthly bytes should be 5000 after reset, got %d", limiter.monthlyBytes)
	}
}

func TestTokenBucketLimiter_LoadFromStorage(t *testing.T) {
	now := time.Now()
	data := &Data{
		DailyBytes:   500,
		MonthlyBytes: 2000,
		DayStart:     time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
		MonthStart:   time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	}

	limiter := &TokenBucketLimiter{}
	limiter.loadFromStorage(data)

	if limiter.dailyBytes != 500 {
		t.Errorf("Expected daily bytes 500, got %d", limiter.dailyBytes)
	}

	if limiter.monthlyBytes != 2000 {
		t.Errorf("Expected monthly bytes 2000, got %d", limiter.monthlyBytes)
	}
}

func TestTokenBucketLimiter_LoadFromStorageWithExpiredData(t *testing.T) {
	now := time.Now()
	yesterday := now.Add(-25 * time.Hour) // More than 24 hours ago

	data := &Data{
		DailyBytes:   500,
		MonthlyBytes: 2000,
		DayStart:     yesterday,
		MonthStart:   time.Date(yesterday.Year(), yesterday.Month(), 1, 0, 0, 0, 0, yesterday.Location()),
	}

	limiter := &TokenBucketLimiter{}
	limiter.loadFromStorage(data)

	// Daily counter should be reset
	if limiter.dailyBytes != 0 {
		t.Errorf("Expected daily bytes 0 after expiry, got %d", limiter.dailyBytes)
	}

	// Monthly counter should be preserved if same month
	if now.Month() == yesterday.Month() && now.Year() == yesterday.Year() {
		if limiter.monthlyBytes != 2000 {
			t.Errorf("Expected monthly bytes 2000, got %d", limiter.monthlyBytes)
		}
	} else {
		if limiter.monthlyBytes != 0 {
			t.Errorf("Expected monthly bytes 0 after month change, got %d", limiter.monthlyBytes)
		}
	}
}

func TestTokenBucketLimiter_ToStorage(t *testing.T) {
	rule := &Rule{Type: "client"}
	limiter := &TokenBucketLimiter{
		identifier:      "test_limiter",
		rule:            rule,
		tokens:          500.5,
		requestCount:    10,
		concurrentConns: 5,
		dailyBytes:      1000,
		monthlyBytes:    5000,
		windowStart:     time.Now(),
		dayStart:        time.Now(),
		monthStart:      time.Now(),
	}

	data := limiter.toStorage()

	if data.Identifier != "test_limiter" {
		t.Errorf("Expected identifier 'test_limiter', got '%s'", data.Identifier)
	}

	if data.Type != "client" {
		t.Errorf("Expected type 'client', got '%s'", data.Type)
	}

	if data.BytesUsed != int64(limiter.tokens) {
		t.Errorf("Expected bytes used %d, got %d", int64(limiter.tokens), data.BytesUsed)
	}

	if data.RequestsUsed != 10 {
		t.Errorf("Expected requests used 10, got %d", data.RequestsUsed)
	}

	if data.ConcurrentConns != 5 {
		t.Errorf("Expected concurrent conns 5, got %d", data.ConcurrentConns)
	}

	if data.DailyBytes != 1000 {
		t.Errorf("Expected daily bytes 1000, got %d", data.DailyBytes)
	}

	if data.MonthlyBytes != 5000 {
		t.Errorf("Expected monthly bytes 5000, got %d", data.MonthlyBytes)
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "concurrent_test",
				Type:           "client",
				Identifier:     "*",
				Enabled:        true,
				BandwidthLimit: 10000,
				BurstLimit:     10000,
				Action:         "block",
			},
		},
	}
	rl.UpdateConfig(config)

	const numGoroutines = 10
	const numRequests = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			clientID := fmt.Sprintf("client_%d", id)

			for j := 0; j < numRequests; j++ {
				result := rl.CheckRateLimit(clientID, "example.com", 10, 1)
				if result == nil {
					t.Errorf("CheckRateLimit returned nil result")
					return
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify that limiters were created for each client
	rl.mu.RLock()
	limiterCount := len(rl.limiters)
	rl.mu.RUnlock()

	if limiterCount != numGoroutines {
		t.Errorf("Expected %d limiters, got %d", numGoroutines, limiterCount)
	}
}

func TestRateLimiter_GetLimiterWithStorage(t *testing.T) {
	storage := NewMockStorage()
	rl := NewRateLimiter(storage)

	rule := &Rule{
		ID:             "test_rule",
		Type:           "client",
		BandwidthLimit: 1000,
		BurstLimit:     1000,
	}

	// Add some data to storage
	testData := &Data{
		Identifier:   "test_key",
		DailyBytes:   500,
		MonthlyBytes: 2000,
		DayStart:     time.Now(),
		MonthStart:   time.Now(),
	}
	storage.SaveRateLimitData(testData)

	limiter := rl.getLimiter("test_key", rule)

	if limiter == nil {
		t.Fatal("getLimiter should return non-nil limiter")
	}

	// Check that data was loaded from storage
	if limiter.dailyBytes != 500 {
		t.Errorf("Expected daily bytes 500, got %d", limiter.dailyBytes)
	}

	if limiter.monthlyBytes != 2000 {
		t.Errorf("Expected monthly bytes 2000, got %d", limiter.monthlyBytes)
	}
}

func TestRateLimiter_GetLimiterWithStorageError(t *testing.T) {
	storage := NewMockStorage()
	storage.SetErrors(nil, nil, nil, fmt.Errorf("load data error"), nil)
	rl := NewRateLimiter(storage)

	rule := &Rule{
		ID:             "test_rule",
		Type:           "client",
		BandwidthLimit: 1000,
		BurstLimit:     1000,
	}

	limiter := rl.getLimiter("test_key", rule)

	if limiter == nil {
		t.Fatal("getLimiter should return non-nil limiter even with storage error")
	}

	// Should have default values when storage load fails
	if limiter.dailyBytes != 0 {
		t.Errorf("Expected daily bytes 0 when storage fails, got %d", limiter.dailyBytes)
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	storage := NewMockStorage()
	rl := NewRateLimiter(storage)

	rule := &Rule{
		ID:             "test_rule",
		Type:           "client",
		BandwidthLimit: 1000,
		BurstLimit:     1000,
	}

	// Create some limiters
	_ = rl.getLimiter("active_limiter", rule)
	limiter2 := rl.getLimiter("expired_limiter", rule)

	// Make one limiter appear expired
	limiter2.mu.Lock()
	limiter2.lastRefill = time.Now().Add(-2 * time.Hour)
	limiter2.mu.Unlock()

	// Run cleanup
	rl.cleanup()

	// Check that expired limiter was removed
	rl.mu.RLock()
	_, exists1 := rl.limiters["active_limiter"]
	_, exists2 := rl.limiters["expired_limiter"]
	rl.mu.RUnlock()

	if !exists1 {
		t.Error("Active limiter should not be removed")
	}

	if exists2 {
		t.Error("Expired limiter should be removed")
	}

	// Check that active limiter data was saved
	savedData, err := storage.LoadRateLimitData("active_limiter")
	if err != nil {
		t.Errorf("Active limiter data should be saved, got error: %v", err)
	}

	if savedData.Identifier != "active_limiter" {
		t.Errorf("Expected saved identifier 'active_limiter', got '%s'", savedData.Identifier)
	}
}

func TestRateLimiter_CleanupWithStorageErrors(t *testing.T) {
	storage := NewMockStorage()
	storage.SetErrors(nil, nil, fmt.Errorf("save data error"), nil, fmt.Errorf("cleanup error"))
	rl := NewRateLimiter(storage)

	rule := &Rule{
		ID:             "test_rule",
		Type:           "client",
		BandwidthLimit: 1000,
		BurstLimit:     1000,
	}

	// Create a limiter
	rl.getLimiter("test_limiter", rule)

	// Cleanup should not panic even with storage errors
	rl.cleanup()
}

func BenchmarkRateLimiter_CheckRateLimit(b *testing.B) {
	rl := NewRateLimiter(nil)

	config := &Config{
		Rules: []*Rule{
			{
				ID:             "bench_rule",
				Type:           "client",
				Identifier:     "*",
				Enabled:        true,
				BandwidthLimit: 1000000,
				BurstLimit:     1000000,
				RequestLimit:   1000000,
				RequestWindow:  1 * time.Second,
				Action:         "block",
			},
		},
	}
	rl.UpdateConfig(config)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rl.CheckRateLimit("client1", "example.com", 100, 1)
		}
	})
}

func BenchmarkTokenBucketLimiter_CheckLimit(b *testing.B) {
	rule := &Rule{
		BandwidthLimit: 1000000,
		BurstLimit:     1000000,
		RequestLimit:   1000000,
		RequestWindow:  1 * time.Second,
		Action:         "block",
	}

	limiter := &TokenBucketLimiter{
		rule:        rule,
		tokens:      float64(rule.BurstLimit),
		lastRefill:  time.Now(),
		windowStart: time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.checkLimit(100, 1)
	}
}
