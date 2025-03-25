package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimitConfig cho Rate Limiter
type RateLimitConfig struct {
	FixedLimit   int           // Số request tối đa cho Fixed Window
	SlidingLimit int           // Số request tối đa cho Sliding Window
	TokenLimit   int           // Số token tối đa cho Token Bucket
	TokenRefill  time.Duration // Tốc độ refill token (ví dụ: 1 token mỗi giây)
	Window       time.Duration // Kích thước cửa sổ thời gian
}

// RateLimiter struct
type RateLimiter struct {
	redisClient *redis.Client
	config      RateLimitConfig
}

func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	return &RateLimiter{
		redisClient: redis.NewClient(&redis.Options{
			Addr: "localhost:6379",
		}),
		config: cfg,
	}
}

// Fixed Window Rate Limiting
func (rl *RateLimiter) FixedWindow(ctx context.Context, userID string) bool {
	window := time.Now().Truncate(rl.config.Window).Unix()
	key := fmt.Sprintf("fixed_window:%s:%d", userID, window)

	count, err := rl.redisClient.Incr(ctx, key).Result()
	if err != nil {
		return false
	}
	if count == 1 {
		rl.redisClient.Expire(ctx, key, rl.config.Window)
	}
	return count <= int64(rl.config.FixedLimit)
}

// Sliding Window Rate Limiting
func (rl *RateLimiter) SlidingWindow(ctx context.Context, userID string) bool {
	key := fmt.Sprintf("sliding_window:%s", userID)
	now := time.Now().UnixNano()
	cutoff := now - rl.config.Window.Nanoseconds()

	// Xóa các request cũ
	rl.redisClient.ZRemRangeByScore(ctx, key, "0", fmt.Sprint(cutoff))

	// Thêm request mới
	rl.redisClient.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})

	// Đếm số request trong window
	count, err := rl.redisClient.ZCard(ctx, key).Result()
	if err != nil {
		return false
	}
	rl.redisClient.Expire(ctx, key, rl.config.Window)
	return count <= int64(rl.config.SlidingLimit)
}

func (rl *RateLimiter) TokenBucket(ctx context.Context, userID string) bool {
	key := fmt.Sprintf("token_bucket:%s", userID)
	now := time.Now()

	// Lấy dữ liệu từ Redis
	data, err := rl.redisClient.HGetAll(ctx, key).Result()
	if err != nil {
		log.Errorf("HGetAll failed: %v", err)
		return false
	}

	// Khởi tạo tokens
	tokens := rl.config.TokenLimit // 20
	lastRefill, _ := time.Parse(time.RFC3339Nano, data["last_refill"])
	if lastRefill.IsZero() {
		lastRefill = now
	} else {
		elapsed := now.Sub(lastRefill)
		newTokens := int(elapsed / rl.config.TokenRefill) // 10 giây/token
		if tokenStr, exists := data["tokens"]; exists {
			if parsedTokens, err := strconv.Atoi(tokenStr); err == nil {
				tokens = parsedTokens
			}
		}
		tokens = min(tokens+newTokens, rl.config.TokenLimit)
		log.Debugf("Before - User: %s, Tokens: %d, Elapsed: %v, NewTokens: %d", userID, tokens, elapsed, newTokens)
	}

	// Kiểm tra token
	if tokens < 1 {
		log.Debugf("No tokens left for %s", userID)
		return false
	}

	// Giảm token và ghi vào Redis
	tokens--
	err = rl.redisClient.HMSet(ctx, key,
		"tokens", strconv.Itoa(tokens),
		"last_refill", now.Format(time.RFC3339Nano),
	).Err()
	if err != nil {
		log.Errorf("HMSet failed: %v", err)
		return false
	}

	log.Debugf("Allowed - User: %s, Tokens left: %d", userID, tokens)
	return true
}

// Hàm min hỗ trợ Token Bucket
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
