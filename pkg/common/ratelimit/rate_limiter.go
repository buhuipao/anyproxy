// Package ratelimit provides rate limiting functionality for AnyProxy.
// It supports multiple dimensions including client, domain, and global rate limiting.
package ratelimit

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Storage interface for rate limiting persistence
type Storage interface {
	SaveRateLimitConfig(config *Config) error
	LoadRateLimitConfig() (*Config, error)
	SaveRateLimitData(data *Data) error
	LoadRateLimitData(identifier string) (*Data, error)
	CleanupExpiredRateLimitData() error
}

// Config rate limiting configuration
type Config struct {
	Rules []*Rule `json:"rules"`
}

// Rule rate limiting rule
type Rule struct {
	ID         string `json:"id"`
	Type       string `json:"type"`       // client, domain, global
	Identifier string `json:"identifier"` // client_id, domain, or "*" for global
	Enabled    bool   `json:"enabled"`

	// Bandwidth limits
	BandwidthLimit int64 `json:"bandwidth_limit"` // bytes per second
	BurstLimit     int64 `json:"burst_limit"`     // max burst bytes

	// Request limits
	RequestLimit  int64         `json:"request_limit"`  // requests per window
	RequestWindow time.Duration `json:"request_window"` // time window

	// Connection limits
	ConcurrentLimit int64 `json:"concurrent_limit"` // max concurrent connections

	// Time-based limits
	DailyLimit   int64 `json:"daily_limit"`   // daily bandwidth limit
	MonthlyLimit int64 `json:"monthly_limit"` // monthly bandwidth limit

	// Behavior
	Action   string `json:"action"`   // block, throttle, log
	Priority int    `json:"priority"` // rule priority (higher = more important)

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Data runtime rate limiting data
type Data struct {
	Identifier string `json:"identifier"`
	Type       string `json:"type"`

	// Current counters
	BytesUsed       int64 `json:"bytes_used"`
	RequestsUsed    int64 `json:"requests_used"`
	ConcurrentConns int64 `json:"concurrent_conns"`

	// Daily/Monthly counters
	DailyBytes   int64 `json:"daily_bytes"`
	MonthlyBytes int64 `json:"monthly_bytes"`

	// Time windows
	WindowStart time.Time `json:"window_start"`
	DayStart    time.Time `json:"day_start"`
	MonthStart  time.Time `json:"month_start"`

	// Metadata
	LastAccess time.Time `json:"last_access"`
	LastReset  time.Time `json:"last_reset"`
}

// RateLimiter manages rate limiting across multiple dimensions
type RateLimiter struct {
	storage     Storage
	mu          sync.RWMutex
	limiters    map[string]*TokenBucketLimiter
	config      *Config
	lastCleanup time.Time
}

// TokenBucketLimiter implements token bucket rate limiting algorithm
type TokenBucketLimiter struct {
	identifier string
	rule       *Rule

	// Token bucket for bandwidth limiting
	tokens     float64
	lastRefill time.Time

	// Request rate limiting
	requestCount int64
	windowStart  time.Time

	// Connection limiting
	concurrentConns int64

	// Daily/Monthly counters
	dailyBytes   int64
	monthlyBytes int64
	dayStart     time.Time
	monthStart   time.Time

	mu sync.Mutex
}

// LimitResult result of rate limiting check
type LimitResult struct {
	Allowed        bool          `json:"allowed"`
	Action         string        `json:"action"` // allow, throttle, block
	Reason         string        `json:"reason"`
	RetryAfter     time.Duration `json:"retry_after"`
	RemainingQuota int64         `json:"remaining_quota"`
	LimitType      string        `json:"limit_type"` // bandwidth, request, connection, daily, monthly
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(storage Storage) *RateLimiter {
	limiter := &RateLimiter{
		storage:     storage,
		limiters:    make(map[string]*TokenBucketLimiter),
		lastCleanup: time.Now(),
	}

	// Load configuration from storage if available
	var config *Config
	if storage != nil {
		loadedConfig, err := storage.LoadRateLimitConfig()
		if err != nil {
			logger.Warn("Failed to load rate limit config, using empty config", "err", err)
			config = &Config{Rules: make([]*Rule, 0)}
		} else {
			config = loadedConfig
		}
	} else {
		// Create empty config for memory-only mode
		config = &Config{Rules: make([]*Rule, 0)}
	}
	limiter.config = config

	// Start background cleanup
	go limiter.cleanupRoutine()

	return limiter
}

// CheckRateLimit checks if request should be rate limited
func (rl *RateLimiter) CheckRateLimit(clientID, domain string, requestSize int64, connCount int64) *LimitResult {
	// Check client-level limits
	if result := rl.checkClientLimit(clientID, requestSize, connCount); !result.Allowed {
		return result
	}

	// Check domain-level limits
	if domain != "" {
		if result := rl.checkDomainLimit(domain, requestSize, connCount); !result.Allowed {
			return result
		}
	}

	// Check global limits
	if result := rl.checkGlobalLimit(requestSize, connCount); !result.Allowed {
		return result
	}

	return &LimitResult{
		Allowed: true,
		Action:  "allow",
		Reason:  "within limits",
	}
}

// checkClientLimit checks client-specific rate limits
func (rl *RateLimiter) checkClientLimit(clientID string, requestSize int64, connCount int64) *LimitResult {
	rules := rl.getRulesByType("client")

	for _, rule := range rules {
		if rule.Identifier == clientID || rule.Identifier == "*" {
			limiter := rl.getLimiter(fmt.Sprintf("client_%s", clientID), rule)
			if result := limiter.checkLimit(requestSize, connCount); !result.Allowed {
				result.LimitType = "client"
				return result
			}
		}
	}

	return &LimitResult{Allowed: true}
}

// checkDomainLimit checks domain-specific rate limits
func (rl *RateLimiter) checkDomainLimit(domain string, requestSize int64, connCount int64) *LimitResult {
	rules := rl.getRulesByType("domain")

	for _, rule := range rules {
		if rule.Identifier == domain || rule.Identifier == "*" {
			limiter := rl.getLimiter(fmt.Sprintf("domain_%s", domain), rule)
			if result := limiter.checkLimit(requestSize, connCount); !result.Allowed {
				result.LimitType = "domain"
				return result
			}
		}
	}

	return &LimitResult{Allowed: true}
}

// checkGlobalLimit checks global rate limits
func (rl *RateLimiter) checkGlobalLimit(requestSize int64, connCount int64) *LimitResult {
	rules := rl.getRulesByType("global")

	for _, rule := range rules {
		limiter := rl.getLimiter("global", rule)
		if result := limiter.checkLimit(requestSize, connCount); !result.Allowed {
			result.LimitType = "global"
			return result
		}
	}

	return &LimitResult{Allowed: true}
}

// getRulesByType gets rate limiting rules by type
func (rl *RateLimiter) getRulesByType(ruleType string) []*Rule {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	var rules []*Rule
	for _, rule := range rl.config.Rules {
		if rule.Type == ruleType && rule.Enabled {
			rules = append(rules, rule)
		}
	}

	return rules
}

// getLimiter gets or creates a token bucket limiter
func (rl *RateLimiter) getLimiter(key string, rule *Rule) *TokenBucketLimiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limiters[key]
	if !exists {
		now := time.Now()
		limiter = &TokenBucketLimiter{
			identifier:  key,
			rule:        rule,
			tokens:      float64(rule.BurstLimit),
			lastRefill:  now,
			windowStart: now,
			dayStart:    time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()),
			monthStart:  time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
		}
		rl.limiters[key] = limiter

		// Try to load persisted data if storage is available
		if rl.storage != nil {
			if data, err := rl.storage.LoadRateLimitData(key); err == nil {
				limiter.loadFromStorage(data)
			}
		}
	}

	return limiter
}

// UpdateConfig updates rate limiting configuration
func (rl *RateLimiter) UpdateConfig(config *Config) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.config = config

	// Clear existing limiters to force reload with new config
	rl.limiters = make(map[string]*TokenBucketLimiter)

	// Save to storage if available
	if rl.storage != nil {
		return rl.storage.SaveRateLimitConfig(config)
	}

	return nil
}

// GetConfig gets current rate limiting configuration
func (rl *RateLimiter) GetConfig() *Config {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	return rl.config
}

// cleanupRoutine runs periodic cleanup
func (rl *RateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		rl.cleanup()
	}
}

// cleanup removes expired limiters and saves data
func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	expiredKeys := make([]string, 0)

	for key, limiter := range rl.limiters {
		limiter.mu.Lock()

		// Remove limiters that haven't been used for more than 1 hour
		if now.Sub(limiter.lastRefill) > time.Hour {
			expiredKeys = append(expiredKeys, key)
		} else if rl.storage != nil {
			// Save active limiter data only if storage is available
			data := limiter.toStorage()
			if err := rl.storage.SaveRateLimitData(data); err != nil {
				logger.Warn("Failed to save rate limit data", "key", key, "err", err)
			}
		}

		limiter.mu.Unlock()
	}

	// Remove expired limiters
	for _, key := range expiredKeys {
		delete(rl.limiters, key)
	}

	if len(expiredKeys) > 0 {
		logger.Debug("Cleaned up expired rate limiters", "count", len(expiredKeys))
	}

	// Cleanup expired data in storage if available
	if rl.storage != nil {
		if err := rl.storage.CleanupExpiredRateLimitData(); err != nil {
			logger.Warn("Failed to cleanup expired rate limit data", "err", err)
		}
	}
}

// checkLimit checks if request should be rate limited using token bucket algorithm
func (tbl *TokenBucketLimiter) checkLimit(requestSize int64, connCount int64) *LimitResult {
	tbl.mu.Lock()
	defer tbl.mu.Unlock()

	now := time.Now()

	// Refill tokens based on bandwidth limit
	if tbl.rule.BandwidthLimit > 0 {
		elapsed := now.Sub(tbl.lastRefill)
		tokensToAdd := float64(tbl.rule.BandwidthLimit) * elapsed.Seconds()
		tbl.tokens = math.Min(float64(tbl.rule.BurstLimit), tbl.tokens+tokensToAdd)
		tbl.lastRefill = now

		// Check if we have enough tokens
		if float64(requestSize) > tbl.tokens {
			return &LimitResult{
				Allowed:    false,
				Action:     tbl.rule.Action,
				Reason:     "bandwidth limit exceeded",
				RetryAfter: time.Duration(float64(requestSize-int64(tbl.tokens)) / float64(tbl.rule.BandwidthLimit) * float64(time.Second)),
			}
		}

		// Consume tokens
		tbl.tokens -= float64(requestSize)
	}

	// Check request rate limit
	if tbl.rule.RequestLimit > 0 {
		// Reset window if needed
		if now.Sub(tbl.windowStart) >= tbl.rule.RequestWindow {
			tbl.requestCount = 0
			tbl.windowStart = now
		}

		if tbl.requestCount >= tbl.rule.RequestLimit {
			return &LimitResult{
				Allowed:    false,
				Action:     tbl.rule.Action,
				Reason:     "request rate limit exceeded",
				RetryAfter: tbl.rule.RequestWindow - now.Sub(tbl.windowStart),
			}
		}

		tbl.requestCount++
	}

	// Check concurrent connection limit
	if tbl.rule.ConcurrentLimit > 0 && connCount > tbl.rule.ConcurrentLimit {
		return &LimitResult{
			Allowed: false,
			Action:  tbl.rule.Action,
			Reason:  "concurrent connection limit exceeded",
		}
	}

	// Update daily counter
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if !dayStart.Equal(tbl.dayStart) {
		tbl.dailyBytes = 0
		tbl.dayStart = dayStart
	}

	// Check daily limit
	if tbl.rule.DailyLimit > 0 && tbl.dailyBytes+requestSize > tbl.rule.DailyLimit {
		return &LimitResult{
			Allowed: false,
			Action:  tbl.rule.Action,
			Reason:  "daily limit exceeded",
		}
	}

	// Update monthly counter
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if !monthStart.Equal(tbl.monthStart) {
		tbl.monthlyBytes = 0
		tbl.monthStart = monthStart
	}

	// Check monthly limit
	if tbl.rule.MonthlyLimit > 0 && tbl.monthlyBytes+requestSize > tbl.rule.MonthlyLimit {
		return &LimitResult{
			Allowed: false,
			Action:  tbl.rule.Action,
			Reason:  "monthly limit exceeded",
		}
	}

	// Update counters
	tbl.dailyBytes += requestSize
	tbl.monthlyBytes += requestSize
	tbl.concurrentConns = connCount

	return &LimitResult{
		Allowed: true,
		Action:  "allow",
		Reason:  "within limits",
	}
}

// loadFromStorage loads limiter state from storage
func (tbl *TokenBucketLimiter) loadFromStorage(data *Data) {
	now := time.Now()

	tbl.dailyBytes = data.DailyBytes
	tbl.monthlyBytes = data.MonthlyBytes
	tbl.dayStart = data.DayStart
	tbl.monthStart = data.MonthStart

	// Reset counters if time windows have passed
	if now.Sub(data.DayStart) >= 24*time.Hour {
		tbl.dailyBytes = 0
		tbl.dayStart = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}

	if now.Year() != data.MonthStart.Year() || now.Month() != data.MonthStart.Month() {
		tbl.monthlyBytes = 0
		tbl.monthStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	}
}

// toStorage converts limiter state to storage format
func (tbl *TokenBucketLimiter) toStorage() *Data {
	return &Data{
		Identifier:      tbl.identifier,
		Type:            tbl.rule.Type,
		BytesUsed:       int64(tbl.tokens),
		RequestsUsed:    tbl.requestCount,
		ConcurrentConns: tbl.concurrentConns,
		DailyBytes:      tbl.dailyBytes,
		MonthlyBytes:    tbl.monthlyBytes,
		WindowStart:     tbl.windowStart,
		DayStart:        tbl.dayStart,
		MonthStart:      tbl.monthStart,
		LastAccess:      time.Now(),
	}
}
