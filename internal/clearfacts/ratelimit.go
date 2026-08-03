package clearfacts

import (
	"context"
	"sync"
	"time"
)

// rateLimiter enforces the two F-54 knobs inside the client itself, so
// nothing downstream has to remember to: max_concurrent in-flight requests
// (a buffered channel used as a semaphore) and a minimum interval between
// the start of successive requests (a timestamp guarded by a mutex).
type rateLimiter struct {
	sem         chan struct{}
	minInterval time.Duration

	mu   sync.Mutex
	last time.Time
}

func newRateLimiter(maxConcurrent int, minInterval time.Duration) *rateLimiter {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &rateLimiter{
		sem:         make(chan struct{}, maxConcurrent),
		minInterval: minInterval,
	}
}

// acquire blocks until it is this caller's turn to send a request, honouring
// both max_concurrent and min_interval, or until ctx is cancelled. The
// returned release func must be called exactly once when the request
// completes.
func (r *rateLimiter) acquire(ctx context.Context) (release func(), err error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	r.mu.Lock()
	var wait time.Duration
	if !r.last.IsZero() {
		if elapsed := time.Since(r.last); elapsed < r.minInterval {
			wait = r.minInterval - elapsed
		}
	}
	r.mu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			<-r.sem
			return nil, ctx.Err()
		}
	}

	r.mu.Lock()
	r.last = time.Now()
	r.mu.Unlock()

	return func() { <-r.sem }, nil
}
