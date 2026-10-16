package auth

import (
	"sync"
	"time"
)

// backoff slows down password guessing: after a few failures from one
// address or against one username, sign-ins there are refused for a while
// that doubles with each further failure. It lives in memory, so it is per
// process and forgets on restart; that is enough to make guessing slow,
// not a lockout policy.
type backoff struct {
	mu       sync.Mutex
	failures map[string]*failure
	now      func() time.Time
}

type failure struct {
	count int
	last  time.Time
	until time.Time
}

const (
	freeFailures = 5
	firstDelay   = 2 * time.Second
	maxDelay     = 15 * time.Minute
	forgetAfter  = time.Hour
)

func newBackoff() *backoff {
	return &backoff{failures: map[string]*failure{}, now: time.Now}
}

// wait is how long sign-ins under any of keys are refused still.
func (b *backoff) wait(keys ...string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var longest time.Duration
	for _, k := range keys {
		if f, ok := b.failures[k]; ok {
			longest = max(longest, f.until.Sub(now))
		}
	}
	return longest
}

// fail counts a failed sign-in under each of keys.
func (b *backoff) fail(keys ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if len(b.failures) > 10000 {
		for k, f := range b.failures {
			if now.Sub(f.last) > forgetAfter {
				delete(b.failures, k)
			}
		}
	}
	for _, k := range keys {
		f, ok := b.failures[k]
		if !ok || now.Sub(f.last) > forgetAfter {
			f = &failure{}
			b.failures[k] = f
		}
		f.count++
		f.last = now
		if f.count >= freeFailures {
			delay := firstDelay << min(f.count-freeFailures, 20)
			f.until = now.Add(min(delay, maxDelay))
		}
	}
}

// succeed forgets the failures under keys.
func (b *backoff) succeed(keys ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		delete(b.failures, k)
	}
}
