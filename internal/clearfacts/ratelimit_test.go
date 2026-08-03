package clearfacts

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "Rate discipline: `max_concurrent` (default 1) and `min_interval` (default 2s) enforced inside the client (F-54)"
func TestRateLimiterEnforcesMinInterval(t *testing.T) {
	const minInterval = 40 * time.Millisecond
	rl := newRateLimiter(1, minInterval)
	ctx := context.Background()

	release1, err := rl.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	start := time.Now()
	release1()

	release2, err := rl.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	elapsed := time.Since(start)
	release2()

	if elapsed < minInterval {
		t.Errorf("second acquire arrived after %v, want at least the configured min interval %v", elapsed, minInterval)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "Rate discipline: `max_concurrent` (default 1) and `min_interval` (default 2s) enforced inside the client (F-54)"
func TestRateLimiterEnforcesMaxConcurrent(t *testing.T) {
	const maxConcurrent = 2
	rl := newRateLimiter(maxConcurrent, 0)

	var (
		mu       sync.Mutex
		inFlight int
		peak     int
	)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := rl.acquire(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()

	if peak == 0 {
		t.Fatal("no goroutine ever acquired the limiter — this test isn't exercising anything")
	}
	if peak > maxConcurrent {
		t.Errorf("observed peak concurrency %d, want at most %d", peak, maxConcurrent)
	}
}

func TestRateLimiterRespectsContextCancellation(t *testing.T) {
	rl := newRateLimiter(1, 0)
	release, err := rl.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if _, err := rl.acquire(ctx); err == nil {
		t.Fatal("acquire against an exhausted semaphore with a cancelled context should return an error")
	}
}

func TestRateLimiterDefaultsMaxConcurrentToOne(t *testing.T) {
	rl := newRateLimiter(0, 0)
	if cap(rl.sem) != 1 {
		t.Errorf("newRateLimiter(0, ...) semaphore capacity = %d, want 1 (F-54 default)", cap(rl.sem))
	}
}
