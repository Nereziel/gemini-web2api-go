package app

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertImageLimit(t *testing.T, e *ImageRateLimitError, reason string, wait time.Duration) {
	t.Helper()
	if e == nil {
		t.Fatalf("expected image limit %s", reason)
	}
	if e.Reason != reason || e.RetryAfter != wait {
		t.Fatalf("got %s (%v), want %s (%v)", e.Reason, e.RetryAfter, reason, wait)
	}
}

func TestImageRateLimitMinuteWindow(t *testing.T) {
	var l imageRateLimiter
	c := RuntimeConfig{ImageRPM: 3}
	now := time.Unix(10000, 0)
	for i := 0; i < 3; i++ {
		if e := l.startAttempt(c, now.Add(time.Duration(i)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	assertImageLimit(t, l.startAttempt(c, now.Add(3*time.Second)), "rpm", 57*time.Second)
	if e := l.startAttempt(c, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	assertImageLimit(t, l.startAttempt(c, now.Add(time.Minute)), "rpm", time.Second)
}

func TestImageRateLimitHourWindow(t *testing.T) {
	var l imageRateLimiter
	c := RuntimeConfig{ImageRPH: 30}
	now := time.Unix(10000, 0)
	for i := 0; i < 30; i++ {
		if e := l.startAttempt(c, now.Add(time.Duration(i)*time.Second)); e != nil {
			t.Fatal(e)
		}
	}
	assertImageLimit(t, l.startAttempt(c, now.Add(30*time.Second)), "rph", 3570*time.Second)
	if e := l.startAttempt(c, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
}

func TestImageRateLimitIntervalAndRetry(t *testing.T) {
	var l imageRateLimiter
	c := RuntimeConfig{ImageMinIntervalSec: 20}
	now := time.Unix(10000, 0)
	if e := l.acquire(c, now); e != nil {
		t.Fatal(e)
	}
	defer l.release()
	if e := l.startAttempt(c, now); e != nil {
		t.Fatal(e)
	}
	assertImageLimit(t, l.startAttempt(c, now.Add(2*time.Second)), "minimum_interval", 18*time.Second)
	if e := l.startAttempt(c, now.Add(20*time.Second)); e != nil {
		t.Fatal(e)
	}
}

func TestImageRateLimitConcurrency(t *testing.T) {
	var l imageRateLimiter
	c := RuntimeConfig{ImageMaxConcurrent: 1}
	now := time.Unix(10000, 0)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.acquire(c, now) == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted %d concurrent requests, want 1", got)
	}
	l.release()
	if e := l.acquire(c, now); e != nil {
		t.Fatal(e)
	}
	l.release()
}

func TestImageRateLimitCooldown(t *testing.T) {
	var l imageRateLimiter
	c := RuntimeConfig{ImageCooldownOn429Sec: 300}
	now := time.Unix(10000, 0)
	assertImageLimit(t, l.upstream429(c, now), "upstream_429", 300*time.Second)
	assertImageLimit(t, l.acquire(c, now.Add(30*time.Second)), "upstream_429_cooldown", 270*time.Second)
	if e := l.acquire(c, now.Add(300*time.Second)); e != nil {
		t.Fatal(e)
	}
	l.release()
	c.ImageCooldownOn429Sec = 0
	if e := l.acquire(c, now.Add(301*time.Second)); e != nil {
		t.Fatal(e)
	}
	l.release()
}

func TestImageRateLimitRuntimeChanges(t *testing.T) {
	var l imageRateLimiter
	now := time.Unix(10000, 0)
	if e := l.startAttempt(RuntimeConfig{}, now); e != nil {
		t.Fatal(e)
	}
	if e := l.startAttempt(RuntimeConfig{}, now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	// History is kept even when disabled so enabling a limit takes effect immediately.
	c := RuntimeConfig{ImageRPM: 1}
	assertImageLimit(t, l.acquire(c, now.Add(2*time.Second)), "rpm", 59*time.Second)
	c.ImageRPM = 0
	if e := l.acquire(c, now.Add(2*time.Second)); e != nil {
		t.Fatal(e)
	}
	l.release()
}

func TestImageRateLimitDefaultsAndMigration(t *testing.T) {
	c := defaultConfig()
	if c.ImageMaxConcurrent != 1 || c.ImageRPM != 3 || c.ImageRPH != 30 ||
		c.ImageMinIntervalSec != 20 || c.ImageCooldownOn429Sec != 300 {
		t.Fatalf("unexpected image limit defaults: %+v", c)
	}
	if err := json.Unmarshal([]byte(`{"multi_turn":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.ImageRPM != 3 {
		t.Fatal("missing field must preserve defaults")
	}
	if err := json.Unmarshal([]byte(`{"image_rpm":0}`), &c); err != nil || c.ImageRPM != 0 {
		t.Fatal("explicit zero must disable RPM")
	}
	if err := validateImageRateLimits(1, -1, 30, 20, 300); err == nil {
		t.Fatal("negative RPM must be rejected")
	}
	if err := validateImageRateLimits(0, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestImageRateLimitHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	writeImageRateLimitError(w, &ImageRateLimitError{Reason: "rpm", RetryAfter: 1500*time.Millisecond})
	if w.Code != 429 || w.Header().Get("Retry-After") != "2" {
		t.Fatalf("HTTP=%d retry-after=%q", w.Code, w.Header().Get("Retry-After"))
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "image_rate_limit_exceeded" {
		t.Fatalf("bad error response: %s", w.Body.String())
	}
}

func TestImageGenerationLimiterRejectsBeforeUpstream(t *testing.T) {
	old := rtCfg()
	rtMu.Lock()
	rtVal.ImageMaxConcurrent = 1
	rtVal.ImageRPM = 0
	rtVal.ImageRPH = 0
	rtVal.ImageMinIntervalSec = 0
	rtVal.ImageCooldownOn429Sec = 0
	rtMu.Unlock()
	t.Cleanup(func() {
		rtMu.Lock()
		rtVal = old
		rtMu.Unlock()
	})
	if e := imageLimits.acquire(rtCfg(), time.Now()); e != nil {
		t.Fatal(e)
	}
	defer imageLimits.release()
	_, err := streamGenerateWithFiles("image", "image", ModelConfig{Tool: toolImage}, nil, nil, nil)
	e, ok := err.(*ImageRateLimitError)
	if !ok || e.Reason != "concurrent" {
		t.Fatalf("expected image concurrency rejection before network, got %v", err)
	}
}
