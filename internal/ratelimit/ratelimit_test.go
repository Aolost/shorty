package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiter_BurstAndRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	lim := New(2.0, 3)
	lim.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !lim.Allow("1.2.3.4") {
			t.Fatalf("expected burst request %d to be allowed", i)
		}
	}
	if lim.Allow("1.2.3.4") {
		t.Fatal("expected 4th request to be rate-limited")
	}
	// Different key has its own bucket.
	if !lim.Allow("5.6.7.8") {
		t.Fatal("expected separate key to be allowed")
	}

	// Advance 500ms -> 1 token refilled at 2 RPS.
	now = now.Add(500 * time.Millisecond)
	if !lim.Allow("1.2.3.4") {
		t.Fatal("expected refilled request to be allowed")
	}
	if lim.Allow("1.2.3.4") {
		t.Fatal("expected immediate follow-up to be rate-limited")
	}
}

func TestLimiter_BoundedMemory(t *testing.T) {
	lim := New(1.0, 1)
	lim.maxBuckets = 100
	now := time.Unix(1000, 0)
	lim.now = func() time.Time { return now }

	for i := 0; i < 250; i++ {
		now = now.Add(time.Millisecond)
		lim.Allow(fmt.Sprintf("10.0.0.%d", i))
	}
	if len(lim.buckets) > 100 {
		t.Fatalf("bucket count %d exceeded maxBuckets 100", len(lim.buckets))
	}
}
