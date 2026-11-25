package app

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Bound the rolling history even when the limits are disabled. This is also
// the largest supported hourly limit, so all supported limits remain exact.
const maxImageRPH = 100000

// ImageRateLimitError is distinct from egress/IP-slot limits.
type ImageRateLimitError struct {
	Reason     string
	RetryAfter time.Duration
}

func (e *ImageRateLimitError) retrySeconds() int {
	n := int((e.RetryAfter + time.Second - 1) / time.Second)
	if n < 1 {
		return 1
	}
	return n
}

func (e *ImageRateLimitError) Error() string {
	return fmt.Sprintf("image generation rate limit: %s; retry after %d seconds", e.Reason, e.retrySeconds())
}

func (e *ImageRateLimitError) setRetryAfter(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(e.retrySeconds()))
}

func writeImageRateLimitError(w http.ResponseWriter, e *ImageRateLimitError) {
	e.setRetryAfter(w)
	writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{"error": map[string]string{
		"message": e.Error(),
		"type":    "rate_limit_error",
		"code":    "image_rate_limit_exceeded",
	}})
}

func validateImageRateLimits(concurrent, rpm, rph, interval, cooldown int) error {
	for _, field := range []struct {
		name     string
		value    int
		max      int
	}{
		{"image_max_concurrent", concurrent, 1000},
		{"image_rpm", rpm, 10000},
		{"image_rph", rph, maxImageRPH},
		{"image_min_interval_sec", interval, 86400},
		{"image_cooldown_on_429_sec", cooldown, 86400},
	} {
		if field.value < 0 || field.value > field.max {
			return fmt.Errorf("%s=%d outside allowed range [0, %d]", field.name, field.value, field.max)
		}
	}
	return nil
}

// One limiter per server process, not per account, proxy or API endpoint.
// Concurrency covers the entire request, including artifact retrieval.
// Window and interval limits count each actual upstream generation attempt.
type imageRateLimiter struct {
	mu        sync.Mutex
	inflight  int
	starts    []time.Time
	lastStart time.Time
	last429   time.Time
}

var imageLimits imageRateLimiter

// blockedLocked returns the longest active time restriction so Retry-After
// reflects all applicable windows. The caller holds l.mu.
func (l *imageRateLimiter) blockedLocked(c RuntimeConfig, now time.Time) *ImageRateLimitError {
	first := sort.Search(len(l.starts), func(i int) bool {
		return l.starts[i].After(now.Add(-time.Hour))
	})
	l.starts = l.starts[first:]
	var blocked *ImageRateLimitError
	check := func(reason string, wait time.Duration) {
		if wait > 0 && (blocked == nil || wait > blocked.RetryAfter) {
			blocked = &ImageRateLimitError{Reason: reason, RetryAfter: wait}
		}
	}
	if c.ImageCooldownOn429Sec > 0 && !l.last429.IsZero() {
		check("upstream_429_cooldown", l.last429.Add(time.Duration(c.ImageCooldownOn429Sec)*time.Second).Sub(now))
	}
	if c.ImageMinIntervalSec > 0 && !l.lastStart.IsZero() {
		check("minimum_interval", l.lastStart.Add(time.Duration(c.ImageMinIntervalSec)*time.Second).Sub(now))
	}
	if c.ImageRPM > 0 {
		i := sort.Search(len(l.starts), func(i int) bool {
			return l.starts[i].After(now.Add(-time.Minute))
		})
		if len(l.starts)-i >= c.ImageRPM {
			check("rpm", l.starts[len(l.starts)-c.ImageRPM].Add(time.Minute).Sub(now))
		}
	}
	if c.ImageRPH > 0 && len(l.starts) >= c.ImageRPH {
		check("rph", l.starts[len(l.starts)-c.ImageRPH].Add(time.Hour).Sub(now))
	}
	return blocked
}

func (l *imageRateLimiter) acquire(c RuntimeConfig, now time.Time) *ImageRateLimitError {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.blockedLocked(c, now); e != nil {
		return e
	}
	if c.ImageMaxConcurrent > 0 && l.inflight >= c.ImageMaxConcurrent {
		return &ImageRateLimitError{Reason: "concurrent", RetryAfter: time.Second}
	}
	l.inflight++
	return nil
}

func (l *imageRateLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inflight--
}

// startAttempt rechecks limits atomically immediately before sending the
// generation request, including automatic retries and account changes.
func (l *imageRateLimiter) startAttempt(c RuntimeConfig, now time.Time) *ImageRateLimitError {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Concurrent callers may take their clock snapshots before the lock
	// in the opposite order. Keep the rolling history sorted.
	if now.Before(l.lastStart) {
		now = l.lastStart
	}
	if e := l.blockedLocked(c, now); e != nil {
		return e
	}
	l.lastStart = now
	if len(l.starts) >= maxImageRPH {
		l.starts = l.starts[len(l.starts)-maxImageRPH+1:]
	}
	l.starts = append(l.starts, now)
	return nil
}

func (l *imageRateLimiter) upstream429(c RuntimeConfig, now time.Time) *ImageRateLimitError {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.last429) {
		now = l.last429
	}
	l.last429 = now
	wait := time.Duration(c.ImageCooldownOn429Sec) * time.Second
	if e := l.blockedLocked(c, now); e != nil && e.RetryAfter > wait {
		wait = e.RetryAfter
	}
	return &ImageRateLimitError{Reason: "upstream_429", RetryAfter: wait}
}
