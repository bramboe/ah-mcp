package tools

import (
	"context"
	"strings"
	"time"
)

const (
	retryMaxAttempts = 3
	retryBaseDelay   = time.Second
)

// withRetry retries fn up to 3 times when AH returns a rate-limit error (429).
// Backoff: 1s → 2s → 4s (max 7s extra wait on top of fn execution time).
// Any non-rate-limit error is returned immediately without retrying.
func withRetry(ctx context.Context, tool string, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !isRateLimited(lastErr) {
			return lastErr
		}
		wait := retryBaseDelay * (1 << uint(attempt)) // 1s, 2s, 4s
		LogWarn(tool, "rate limited by AH, retrying in %v (attempt %d/%d)", wait, attempt+1, retryMaxAttempts)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}

func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "too many request")
}
