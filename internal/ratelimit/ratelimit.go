package ratelimit

import (
	"sync"
	"time"
)

const defaultMaxBuckets = 10000

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter is an in-memory per-key token-bucket rate limiter with bounded memory.
type Limiter struct {
	mu         sync.Mutex
	rps        float64
	burst      float64
	maxBuckets int
	buckets    map[string]*bucket
	now        func() time.Time
}

// New creates a Limiter allowing rps tokens per second with burst capacity.
func New(rps float64, burst int) *Limiter {
	if rps <= 0 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		rps:        rps,
		burst:      float64(burst),
		maxBuckets: defaultMaxBuckets,
		buckets:    make(map[string]*bucket),
		now:        time.Now,
	}
}

// Allow reports whether a single request for key is permitted at the current time.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= l.maxBuckets {
			l.evictStaleLocked(now)
		}
		b = &bucket{
			tokens:   l.burst,
			lastSeen: now,
		}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rps
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.lastSeen = now
		}
	}

	if b.tokens < 1.0 {
		return false
	}
	b.tokens -= 1.0
	return true
}

func (l *Limiter) evictStaleLocked(now time.Time) {
	// First pass: remove idle buckets older than 5 minutes.
	cutoff := now.Add(-5 * time.Minute)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
	// If still at capacity, remove oldest buckets until under 75% capacity.
	target := (l.maxBuckets * 3) / 4
	for len(l.buckets) > target {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, b := range l.buckets {
			if first || b.lastSeen.Before(oldestTime) {
				oldestKey = k
				oldestTime = b.lastSeen
				first = false
			}
		}
		if first {
			break
		}
		delete(l.buckets, oldestKey)
	}
}
